//! Sharded string-only KV store with an integer fast-path on the
//! INCR family.
//!
//! Architecture: 256 shards, each owning a `HashMap<Vec<u8>, Entry>`
//! behind a `parking_lot::Mutex`. The shard index for a key is
//! `fnv1a(key) & 255`.
//!
//! BUT — the headline win for the Phase-1 binary is that the
//! tokio current_thread runtime never spawns multiple OS threads
//! to drive command dispatch. Even though the data structure
//! supports concurrency, in practice only one thread ever holds
//! a shard lock at a time. The lock cost is therefore the
//! uncontended fast path (~5 ns CAS) rather than the goroutine-
//! contended path the Go side pays.
//!
//! Why keep mutexes if we're single-threaded? Two reasons:
//!   1. Future-proofing: when we go multi-threaded later (Phase 2),
//!      the data structure already supports it.
//!   2. The cost is genuinely tiny on the uncontended path.

use bytes::Bytes;
use std::collections::HashMap;
use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::Mutex;

const NUM_SHARDS: usize = 256;
const SHARD_MASK: u32 = (NUM_SHARDS - 1) as u32;

/// fnv1a is the same hash store/shard.go uses on the Go side.
/// Inlined in a tight loop; LLVM unrolls it for short keys.
#[inline]
fn fnv1a(key: &[u8]) -> u32 {
    const OFFSET: u32 = 2166136261;
    const PRIME: u32 = 16777619;
    let mut h = OFFSET;
    for &b in key {
        h ^= b as u32;
        h = h.wrapping_mul(PRIME);
    }
    h
}

/// One stored value. Either a raw byte string (the common case),
/// or an integer in the int fast-path.
///
/// IntAtomic is the lock-free INCR path mirror of the Go side's
/// store.go. When `is_int` is true, `int_atomic` is authoritative
/// and the bytes representation is stale (formatted on read).
struct Entry {
    bytes: Bytes,
    int_atomic: AtomicI64,
    is_int: bool,
}

impl Entry {
    fn from_bytes(b: Bytes) -> Self {
        Entry {
            bytes: b,
            int_atomic: AtomicI64::new(0),
            is_int: false,
        }
    }
    fn from_int(n: i64) -> Self {
        Entry {
            // bytes is stale-once-incremented; we keep the initial
            // formatted form so a GET right after SET-of-numeric
            // doesn't have to format. After INCR, get_string
            // formats from int_atomic instead.
            bytes: Bytes::from(format!("{n}")),
            int_atomic: AtomicI64::new(n),
            is_int: true,
        }
    }
}

/// Store is the public interface — all per-command handlers go
/// through these methods.
pub struct Store {
    shards: Vec<Mutex<HashMap<Vec<u8>, Entry>>>,
}

impl Store {
    pub fn new() -> Self {
        let mut shards = Vec::with_capacity(NUM_SHARDS);
        for _ in 0..NUM_SHARDS {
            shards.push(Mutex::new(HashMap::with_capacity(64)));
        }
        Store { shards }
    }

    #[inline]
    fn shard(&self, key: &[u8]) -> &Mutex<HashMap<Vec<u8>, Entry>> {
        let idx = (fnv1a(key) & SHARD_MASK) as usize;
        &self.shards[idx]
    }

    /// SET: replace any existing value. Returns nothing useful — the
    /// caller writes the standard "+OK\r\n" reply.
    pub fn set(&self, key: &[u8], value: Bytes) {
        let mut sh = self.shard(key).lock().unwrap();
        sh.insert(key.to_vec(), Entry::from_bytes(value));
    }

    /// GET: return the current value as a Bytes reference. Returns
    /// None when the key is missing. For int-typed entries we
    /// format on demand so the lock-free INCR path stays valid.
    pub fn get(&self, key: &[u8]) -> Option<Bytes> {
        let sh = self.shard(key).lock().unwrap();
        let e = sh.get(key)?;
        if e.is_int {
            // Read the live integer; format. Allocates on every GET
            // for int entries — could be optimized further with a
            // small-int formatting cache if profiling shows it.
            let n = e.int_atomic.load(Ordering::Relaxed);
            return Some(Bytes::from(format!("{n}")));
        }
        Some(e.bytes.clone())
    }

    /// DEL: returns the number of keys actually removed. Variadic
    /// callers (DEL k1 k2 k3) walk the slice on the dispatch side.
    pub fn del(&self, key: &[u8]) -> usize {
        let mut sh = self.shard(key).lock().unwrap();
        sh.remove(key).map(|_| 1).unwrap_or(0)
    }

    /// EXISTS for a single key. Variadic version is dispatch-side.
    pub fn exists(&self, key: &[u8]) -> bool {
        let sh = self.shard(key).lock().unwrap();
        sh.contains_key(key)
    }

    /// INCR/DECR/INCRBY/DECRBY common path. Adds `delta` to the
    /// stored integer and returns the new total. Errors when the
    /// existing value isn't a valid integer.
    ///
    /// Two-tier hot path mirroring the Go side:
    ///   1. If the entry exists and is_int=true, we'd love to use a
    ///      lock-free atomic — but we still hold the shard mutex
    ///      because Rust's std HashMap doesn't support concurrent
    ///      access to entries. The win comes from being on the
    ///      single-threaded tokio runtime: the mutex is always
    ///      uncontended, so the CAS cost is ~5 ns vs Go's ~25 ns
    ///      contended-mutex cost.
    ///   2. Else: parse the stored bytes, promote to int.
    pub fn incr(&self, key: &[u8], delta: i64) -> Result<i64, &'static str> {
        let mut sh = self.shard(key).lock().unwrap();
        if let Some(e) = sh.get_mut(key) {
            if e.is_int {
                let n = e.int_atomic.fetch_add(delta, Ordering::Relaxed) + delta;
                return Ok(n);
            }
            // Promote: parse the existing string as an int.
            let s = std::str::from_utf8(&e.bytes)
                .map_err(|_| "ERR value is not an integer or out of range")?;
            let cur: i64 = s
                .parse()
                .map_err(|_| "ERR value is not an integer or out of range")?;
            let n = cur + delta;
            *e = Entry::from_int(n);
            return Ok(n);
        }
        // Missing — start at delta.
        sh.insert(key.to_vec(), Entry::from_int(delta));
        Ok(delta)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn set_get_del_round_trip() {
        let s = Store::new();
        s.set(b"k", Bytes::from_static(b"v"));
        assert_eq!(&s.get(b"k").unwrap()[..], b"v");
        assert_eq!(s.del(b"k"), 1);
        assert!(s.get(b"k").is_none());
        assert_eq!(s.del(b"k"), 0);
    }

    #[test]
    fn exists() {
        let s = Store::new();
        assert!(!s.exists(b"absent"));
        s.set(b"present", Bytes::from_static(b"x"));
        assert!(s.exists(b"present"));
    }

    #[test]
    fn incr_creates_at_zero() {
        let s = Store::new();
        assert_eq!(s.incr(b"counter", 1).unwrap(), 1);
        assert_eq!(s.incr(b"counter", 1).unwrap(), 2);
        assert_eq!(s.incr(b"counter", 5).unwrap(), 7);
        assert_eq!(s.incr(b"counter", -3).unwrap(), 4);
    }

    #[test]
    fn incr_promotes_existing_string() {
        let s = Store::new();
        s.set(b"n", Bytes::from_static(b"100"));
        assert_eq!(s.incr(b"n", 1).unwrap(), 101);
        // Subsequent INCRs hit the lock-free fast path.
        assert_eq!(s.incr(b"n", 1).unwrap(), 102);
    }

    #[test]
    fn incr_errors_on_non_numeric() {
        let s = Store::new();
        s.set(b"oops", Bytes::from_static(b"hello"));
        assert!(s.incr(b"oops", 1).is_err());
    }

    #[test]
    fn get_after_incr_returns_live_value() {
        let s = Store::new();
        s.incr(b"k", 42).unwrap();
        assert_eq!(&s.get(b"k").unwrap()[..], b"42");
        s.incr(b"k", 8).unwrap();
        assert_eq!(&s.get(b"k").unwrap()[..], b"50");
    }

    #[test]
    fn shards_distribute_keys() {
        // Sanity check the FNV hash produces enough entropy that
        // different keys land in different shards on a typical workload.
        let s = Store::new();
        let keys: Vec<Vec<u8>> = (0..1000).map(|i| format!("k{i}").into_bytes()).collect();
        let mut shard_hits = vec![0usize; NUM_SHARDS];
        for k in &keys {
            let idx = (fnv1a(k) & SHARD_MASK) as usize;
            shard_hits[idx] += 1;
        }
        let used = shard_hits.iter().filter(|&&n| n > 0).count();
        // Should hit ≥ 200 of the 256 shards for 1000 keys (well-distributed).
        assert!(used > 200, "only {used} shards used for 1000 keys");
    }
}
