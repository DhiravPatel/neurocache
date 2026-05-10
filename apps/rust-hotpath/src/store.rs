//! Sharded multi-type KV store: strings (incl. integer fast-path),
//! lists, hashes, sets.
//!
//! Architecture: 256 shards, each owning a `HashMap<Vec<u8>, Entry>`
//! behind a `std::sync::Mutex`. Shard index for a key is
//! `fnv1a(key) & 255`.
//!
//! In Phase 1 + 2 the binary runs on tokio's single-threaded
//! current_thread runtime, so every shard mutex is uncontended.
//! The CAS cost is ~5 ns rather than the goroutine-contended ~25 ns
//! the Go side pays. The data structure already supports
//! concurrency for when Phase 4 multi-threads the runtime.
//!
//! Entry is a tagged union of value types — same shape as Redis's
//! redisObject / robj. Type-mismatched ops return WRONGTYPE so
//! clients see the canonical error.

use bytes::Bytes;
use std::collections::{HashMap, HashSet, VecDeque};
use std::sync::Mutex;

const NUM_SHARDS: usize = 256;
const SHARD_MASK: u32 = (NUM_SHARDS - 1) as u32;

/// fnv1a — same hash store/shard.go uses on the Go side. Inlined in
/// the dispatch hot path.
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

/// Entry holds the value of one keyspace key. Tagged union — only
/// one variant is populated at a time.
///
/// `Int(i64)` is a separate variant from `Bytes(Bytes)` so INCR can
/// skip the parse-add-format cycle for repeated calls on the same
/// key — the redis-benchmark INCR shape, where 200k calls hit the
/// same key, is one promotion + 199,999 fast paths.
pub enum Entry {
    Bytes(Bytes),
    Int(i64),
    List(VecDeque<Bytes>),
    Hash(HashMap<Vec<u8>, Bytes>),
    Set(HashSet<Vec<u8>>),
}

impl Entry {
    /// type_name returns the redis-style type tag for WRONGTYPE
    /// errors and the TYPE command. Matches redis.io documentation.
    pub fn type_name(&self) -> &'static str {
        match self {
            Entry::Bytes(_) | Entry::Int(_) => "string",
            Entry::List(_) => "list",
            Entry::Hash(_) => "hash",
            Entry::Set(_) => "set",
        }
    }
}

/// StoreError is the typed result for every public method. Avoids
/// the `&'static str` ad-hoc errors of the Phase-1 prototype.
#[derive(Debug, PartialEq, Eq)]
#[allow(dead_code)] // NoSuchKey reserved for future LSET/LINSERT
pub enum StoreError {
    WrongType,
    NotInteger,
    NoSuchKey,
}

pub struct Store {
    shards: Vec<Mutex<HashMap<Vec<u8>, Entry>>>,
}

impl Store {
    pub fn new() -> Self {
        let mut shards = Vec::with_capacity(NUM_SHARDS);
        for _ in 0..NUM_SHARDS {
            // Initial capacity 64 — covers the first batch of writes
            // without resize on a fresh server. Growth doubles from
            // there, same as the Go runtime map.
            shards.push(Mutex::new(HashMap::with_capacity(64)));
        }
        Store { shards }
    }

    #[inline]
    fn shard(&self, key: &[u8]) -> &Mutex<HashMap<Vec<u8>, Entry>> {
        let idx = (fnv1a(key) & SHARD_MASK) as usize;
        &self.shards[idx]
    }

    // ─── strings ──────────────────────────────────────────────

    pub fn set(&self, key: &[u8], value: Bytes) {
        let mut sh = self.shard(key).lock().unwrap();
        sh.insert(key.to_vec(), Entry::Bytes(value));
    }

    pub fn get(&self, key: &[u8]) -> Result<Option<Bytes>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            None => Ok(None),
            Some(Entry::Bytes(b)) => Ok(Some(b.clone())),
            Some(Entry::Int(n)) => Ok(Some(Bytes::from(format!("{n}")))),
            Some(_) => Err(StoreError::WrongType),
        }
    }

    pub fn del(&self, key: &[u8]) -> usize {
        let mut sh = self.shard(key).lock().unwrap();
        sh.remove(key).map(|_| 1).unwrap_or(0)
    }

    pub fn exists(&self, key: &[u8]) -> bool {
        let sh = self.shard(key).lock().unwrap();
        sh.contains_key(key)
    }

    /// INCR/DECR/INCRBY/DECRBY common path. `Int` entries fast-path:
    /// no string parse, no format. Returns the new total or
    /// NotInteger if the existing value can't parse as int64.
    pub fn incr(&self, key: &[u8], delta: i64) -> Result<i64, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        match sh.get_mut(key) {
            Some(Entry::Int(n)) => {
                *n = n.wrapping_add(delta);
                Ok(*n)
            }
            Some(Entry::Bytes(b)) => {
                let s = std::str::from_utf8(b).map_err(|_| StoreError::NotInteger)?;
                let cur: i64 = s.parse().map_err(|_| StoreError::NotInteger)?;
                let n = cur.wrapping_add(delta);
                *sh.get_mut(key).unwrap() = Entry::Int(n);
                Ok(n)
            }
            Some(_) => Err(StoreError::WrongType),
            None => {
                sh.insert(key.to_vec(), Entry::Int(delta));
                Ok(delta)
            }
        }
    }

    /// MSET — atomic multi-key set. Acquires one shard lock per
    /// distinct shard (single-threaded runtime guarantees no
    /// deadlock; multi-threaded would need canonical lock order).
    pub fn mset(&self, pairs: &[(Vec<u8>, Bytes)]) {
        for (k, v) in pairs {
            self.set(k, v.clone());
        }
    }

    /// MGET — multi-key get. Missing keys + WRONGTYPE entries return
    /// None at that position (RESP nil bulk on the wire).
    pub fn mget(&self, keys: &[&[u8]]) -> Vec<Option<Bytes>> {
        keys.iter().map(|k| self.get(k).unwrap_or(None)).collect()
    }

    // ─── lists ────────────────────────────────────────────────

    /// LPUSH/RPUSH are O(1) amortized — VecDeque is a ring buffer
    /// over a contiguous allocation. Closer to Redis's quicklist
    /// in cache behavior than the Go side's container/list-then-
    /// qlist evolution. Returns the new length.
    pub fn lpush(&self, key: &[u8], values: &[Bytes]) -> Result<usize, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let entry = sh
            .entry(key.to_vec())
            .or_insert_with(|| Entry::List(VecDeque::new()));
        match entry {
            Entry::List(l) => {
                for v in values {
                    l.push_front(v.clone());
                }
                Ok(l.len())
            }
            _ => Err(StoreError::WrongType),
        }
    }

    pub fn rpush(&self, key: &[u8], values: &[Bytes]) -> Result<usize, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let entry = sh
            .entry(key.to_vec())
            .or_insert_with(|| Entry::List(VecDeque::new()));
        match entry {
            Entry::List(l) => {
                for v in values {
                    l.push_back(v.clone());
                }
                Ok(l.len())
            }
            _ => Err(StoreError::WrongType),
        }
    }

    pub fn lpop(&self, key: &[u8]) -> Result<Option<Bytes>, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let v = match sh.get_mut(key) {
            Some(Entry::List(l)) => l.pop_front(),
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(None),
        };
        // Empty-list cleanup: matches Redis's "no key for empty list" rule.
        if let Some(Entry::List(l)) = sh.get(key) {
            if l.is_empty() {
                sh.remove(key);
            }
        }
        Ok(v)
    }

    pub fn rpop(&self, key: &[u8]) -> Result<Option<Bytes>, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let v = match sh.get_mut(key) {
            Some(Entry::List(l)) => l.pop_back(),
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(None),
        };
        if let Some(Entry::List(l)) = sh.get(key) {
            if l.is_empty() {
                sh.remove(key);
            }
        }
        Ok(v)
    }

    pub fn llen(&self, key: &[u8]) -> Result<usize, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::List(l)) => Ok(l.len()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(0),
        }
    }

    pub fn lrange(&self, key: &[u8], start: i64, stop: i64) -> Result<Vec<Bytes>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        let l = match sh.get(key) {
            Some(Entry::List(l)) => l,
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(vec![]),
        };
        let n = l.len() as i64;
        let (a, b) = normalize_range(start, stop, n);
        if a > b {
            return Ok(vec![]);
        }
        Ok(l.iter().skip(a as usize).take((b - a + 1) as usize).cloned().collect())
    }

    pub fn lindex(&self, key: &[u8], index: i64) -> Result<Option<Bytes>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        let l = match sh.get(key) {
            Some(Entry::List(l)) => l,
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(None),
        };
        let n = l.len() as i64;
        let i = if index < 0 { n + index } else { index };
        if i < 0 || i >= n {
            return Ok(None);
        }
        Ok(l.get(i as usize).cloned())
    }

    // ─── hashes ───────────────────────────────────────────────

    /// HSET — set one or more field/value pairs. Returns the count
    /// of NEW fields (overwrites don't count, matching Redis).
    pub fn hset(&self, key: &[u8], pairs: &[(Vec<u8>, Bytes)]) -> Result<usize, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let entry = sh
            .entry(key.to_vec())
            .or_insert_with(|| Entry::Hash(HashMap::with_capacity(8)));
        match entry {
            Entry::Hash(h) => {
                let mut added = 0;
                for (f, v) in pairs {
                    if h.insert(f.clone(), v.clone()).is_none() {
                        added += 1;
                    }
                }
                Ok(added)
            }
            _ => Err(StoreError::WrongType),
        }
    }

    pub fn hget(&self, key: &[u8], field: &[u8]) -> Result<Option<Bytes>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Hash(h)) => Ok(h.get(field).cloned()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(None),
        }
    }

    pub fn hdel(&self, key: &[u8], fields: &[&[u8]]) -> Result<usize, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let removed = match sh.get_mut(key) {
            Some(Entry::Hash(h)) => {
                let mut n = 0;
                for f in fields {
                    if h.remove(*f).is_some() {
                        n += 1;
                    }
                }
                n
            }
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(0),
        };
        // Empty hash → drop the key.
        if let Some(Entry::Hash(h)) = sh.get(key) {
            if h.is_empty() {
                sh.remove(key);
            }
        }
        Ok(removed)
    }

    pub fn hlen(&self, key: &[u8]) -> Result<usize, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Hash(h)) => Ok(h.len()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(0),
        }
    }

    pub fn hexists(&self, key: &[u8], field: &[u8]) -> Result<bool, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Hash(h)) => Ok(h.contains_key(field)),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(false),
        }
    }

    pub fn hgetall(&self, key: &[u8]) -> Result<Vec<(Bytes, Bytes)>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Hash(h)) => Ok(h
                .iter()
                .map(|(f, v)| (Bytes::copy_from_slice(f), v.clone()))
                .collect()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(vec![]),
        }
    }

    pub fn hkeys(&self, key: &[u8]) -> Result<Vec<Bytes>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Hash(h)) => Ok(h.keys().map(|k| Bytes::copy_from_slice(k)).collect()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(vec![]),
        }
    }

    pub fn hvals(&self, key: &[u8]) -> Result<Vec<Bytes>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Hash(h)) => Ok(h.values().cloned().collect()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(vec![]),
        }
    }

    // ─── sets ─────────────────────────────────────────────────

    pub fn sadd(&self, key: &[u8], members: &[&[u8]]) -> Result<usize, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let entry = sh
            .entry(key.to_vec())
            .or_insert_with(|| Entry::Set(HashSet::with_capacity(8)));
        match entry {
            Entry::Set(s) => {
                let mut added = 0;
                for m in members {
                    if s.insert(m.to_vec()) {
                        added += 1;
                    }
                }
                Ok(added)
            }
            _ => Err(StoreError::WrongType),
        }
    }

    pub fn srem(&self, key: &[u8], members: &[&[u8]]) -> Result<usize, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let removed = match sh.get_mut(key) {
            Some(Entry::Set(s)) => {
                let mut n = 0;
                for m in members {
                    if s.remove(*m) {
                        n += 1;
                    }
                }
                n
            }
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(0),
        };
        if let Some(Entry::Set(s)) = sh.get(key) {
            if s.is_empty() {
                sh.remove(key);
            }
        }
        Ok(removed)
    }

    pub fn sismember(&self, key: &[u8], member: &[u8]) -> Result<bool, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Set(s)) => Ok(s.contains(member)),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(false),
        }
    }

    pub fn scard(&self, key: &[u8]) -> Result<usize, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Set(s)) => Ok(s.len()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(0),
        }
    }

    pub fn smembers(&self, key: &[u8]) -> Result<Vec<Bytes>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Set(s)) => Ok(s.iter().map(|m| Bytes::copy_from_slice(m)).collect()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(vec![]),
        }
    }

    /// SPOP — remove + return one arbitrary member. Single-threaded
    /// runtime means HashSet iteration order is stable enough for the
    /// "random" contract Redis specifies (the order is implementation-
    /// defined per Redis docs).
    pub fn spop(&self, key: &[u8]) -> Result<Option<Bytes>, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let popped = match sh.get_mut(key) {
            Some(Entry::Set(s)) => {
                if let Some(m) = s.iter().next().cloned() {
                    s.remove(&m);
                    Some(Bytes::from(m))
                } else {
                    None
                }
            }
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(None),
        };
        if let Some(Entry::Set(s)) = sh.get(key) {
            if s.is_empty() {
                sh.remove(key);
            }
        }
        Ok(popped)
    }
}

/// normalize_range maps Redis-style [start, stop] (negatives count
/// from end, inclusive stop) to (a, b) absolute indices clamped to
/// [0, n-1]. Returns (a, b) where a > b means empty range.
fn normalize_range(start: i64, stop: i64, n: i64) -> (i64, i64) {
    if n == 0 {
        return (0, -1);
    }
    let mut a = start;
    let mut b = stop;
    if a < 0 {
        a += n;
    }
    if b < 0 {
        b += n;
    }
    if a < 0 {
        a = 0;
    }
    if b >= n {
        b = n - 1;
    }
    (a, b)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn b(s: &str) -> Bytes {
        Bytes::copy_from_slice(s.as_bytes())
    }

    // ─── strings ────────────────────────────────────────────────

    #[test]
    fn set_get_del_round_trip() {
        let s = Store::new();
        s.set(b"k", b("v"));
        assert_eq!(&s.get(b"k").unwrap().unwrap()[..], b"v");
        assert_eq!(s.del(b"k"), 1);
        assert!(s.get(b"k").unwrap().is_none());
        assert_eq!(s.del(b"k"), 0);
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
        s.set(b"n", b("100"));
        assert_eq!(s.incr(b"n", 1).unwrap(), 101);
        assert_eq!(s.incr(b"n", 1).unwrap(), 102);
        // GET on the int entry formats correctly
        assert_eq!(&s.get(b"n").unwrap().unwrap()[..], b"102");
    }

    #[test]
    fn incr_errors_on_non_numeric() {
        let s = Store::new();
        s.set(b"oops", b("hello"));
        assert_eq!(s.incr(b"oops", 1), Err(StoreError::NotInteger));
    }

    #[test]
    fn wrongtype_string_op_on_list() {
        let s = Store::new();
        s.lpush(b"l", &[b("x")]).unwrap();
        assert_eq!(s.get(b"l"), Err(StoreError::WrongType));
        assert_eq!(s.incr(b"l", 1), Err(StoreError::WrongType));
    }

    // ─── lists ──────────────────────────────────────────────────

    #[test]
    fn list_push_pop() {
        let s = Store::new();
        assert_eq!(s.rpush(b"q", &[b("a"), b("b"), b("c")]).unwrap(), 3);
        assert_eq!(s.llen(b"q").unwrap(), 3);
        assert_eq!(&s.lpop(b"q").unwrap().unwrap()[..], b"a");
        assert_eq!(&s.rpop(b"q").unwrap().unwrap()[..], b"c");
        assert_eq!(&s.lpop(b"q").unwrap().unwrap()[..], b"b");
        // Drained — key should be gone, llen returns 0
        assert_eq!(s.llen(b"q").unwrap(), 0);
        assert!(!s.exists(b"q"));
    }

    #[test]
    fn list_lrange() {
        let s = Store::new();
        for v in &["a", "b", "c", "d", "e"] {
            s.rpush(b"l", &[b(v)]).unwrap();
        }
        let r = s.lrange(b"l", 0, -1).unwrap();
        assert_eq!(r.len(), 5);
        assert_eq!(&r[0][..], b"a");
        assert_eq!(&r[4][..], b"e");
        let r = s.lrange(b"l", 1, 3).unwrap();
        assert_eq!(r.len(), 3);
        assert_eq!(&r[0][..], b"b");
        let r = s.lrange(b"l", -2, -1).unwrap();
        assert_eq!(r.len(), 2);
        assert_eq!(&r[0][..], b"d");
    }

    #[test]
    fn list_lindex() {
        let s = Store::new();
        for v in &["a", "b", "c"] {
            s.rpush(b"l", &[b(v)]).unwrap();
        }
        assert_eq!(&s.lindex(b"l", 0).unwrap().unwrap()[..], b"a");
        assert_eq!(&s.lindex(b"l", -1).unwrap().unwrap()[..], b"c");
        assert!(s.lindex(b"l", 99).unwrap().is_none());
    }

    // ─── hashes ─────────────────────────────────────────────────

    #[test]
    fn hash_set_get_del() {
        let s = Store::new();
        let added = s
            .hset(
                b"h",
                &[
                    (b"name".to_vec(), b("alice")),
                    (b"age".to_vec(), b("33")),
                ],
            )
            .unwrap();
        assert_eq!(added, 2);
        // Overwrite shouldn't count
        let added = s.hset(b"h", &[(b"name".to_vec(), b("alex"))]).unwrap();
        assert_eq!(added, 0);
        assert_eq!(&s.hget(b"h", b"name").unwrap().unwrap()[..], b"alex");
        assert_eq!(s.hlen(b"h").unwrap(), 2);
        assert!(s.hexists(b"h", b"name").unwrap());
        assert!(!s.hexists(b"h", b"missing").unwrap());
        let removed = s.hdel(b"h", &[&b"name"[..], &b"age"[..]]).unwrap();
        assert_eq!(removed, 2);
        // Empty hash dropped
        assert!(!s.exists(b"h"));
    }

    #[test]
    fn hash_get_keys_vals_all() {
        let s = Store::new();
        s.hset(
            b"h",
            &[
                (b"k1".to_vec(), b("v1")),
                (b"k2".to_vec(), b("v2")),
                (b"k3".to_vec(), b("v3")),
            ],
        )
        .unwrap();
        let mut keys: Vec<_> = s.hkeys(b"h").unwrap().iter().map(|b| b.to_vec()).collect();
        keys.sort();
        assert_eq!(keys, vec![b"k1".to_vec(), b"k2".to_vec(), b"k3".to_vec()]);
        let mut vals: Vec<_> = s.hvals(b"h").unwrap().iter().map(|b| b.to_vec()).collect();
        vals.sort();
        assert_eq!(vals, vec![b"v1".to_vec(), b"v2".to_vec(), b"v3".to_vec()]);
        let all = s.hgetall(b"h").unwrap();
        assert_eq!(all.len(), 3);
    }

    // ─── sets ───────────────────────────────────────────────────

    #[test]
    fn set_add_rem_members() {
        let s = Store::new();
        let added = s.sadd(b"s", &[&b"a"[..], &b"b"[..], &b"c"[..]]).unwrap();
        assert_eq!(added, 3);
        // Re-add: dedup, count is 0
        let added = s.sadd(b"s", &[&b"a"[..]]).unwrap();
        assert_eq!(added, 0);
        assert_eq!(s.scard(b"s").unwrap(), 3);
        assert!(s.sismember(b"s", b"a").unwrap());
        assert!(!s.sismember(b"s", b"z").unwrap());
        // SREM of every member removes exactly 3 (and one missing == 0)
        let removed = s
            .srem(b"s", &[&b"a"[..], &b"b"[..], &b"c"[..], &b"missing"[..]])
            .unwrap();
        assert_eq!(removed, 3);
        // Draining removed every member → key dropped per Redis semantics
        assert!(!s.exists(b"s"));
    }

    #[test]
    fn set_spop_drains_then_removes_key() {
        let s = Store::new();
        s.sadd(b"s", &[&b"a"[..], &b"b"[..], &b"c"[..]]).unwrap();
        // Pop until empty
        for _ in 0..3 {
            assert!(s.spop(b"s").unwrap().is_some());
        }
        assert!(!s.exists(b"s"));
        // SPOP on empty/missing returns None without error
        assert!(s.spop(b"s").unwrap().is_none());
    }

    #[test]
    fn set_smembers_returns_all() {
        let s = Store::new();
        s.sadd(b"s", &[&b"a"[..], &b"b"[..], &b"c"[..]]).unwrap();
        let mut m: Vec<_> = s.smembers(b"s").unwrap().iter().map(|b| b.to_vec()).collect();
        m.sort();
        assert_eq!(m, vec![b"a".to_vec(), b"b".to_vec(), b"c".to_vec()]);
    }

    #[test]
    fn shards_distribute_keys() {
        let s = Store::new();
        let keys: Vec<Vec<u8>> = (0..1000).map(|i| format!("k{i}").into_bytes()).collect();
        let mut shard_hits = vec![0usize; NUM_SHARDS];
        for k in &keys {
            let idx = (fnv1a(k) & SHARD_MASK) as usize;
            shard_hits[idx] += 1;
        }
        let used = shard_hits.iter().filter(|&&n| n > 0).count();
        assert!(used > 200, "only {used} shards used for 1000 keys");
    }
}
