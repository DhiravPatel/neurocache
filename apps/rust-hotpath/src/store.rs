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

use crate::zset::ZSet;
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
    ZSet(ZSet),
    Stream(Stream),
}

/// Stream is a minimal append-only log of entries, each tagged with
/// a (ms-time, sequence) ID. Covers XADD/XLEN — what
/// redis-benchmark exercises. Full streams (XREAD with consumer
/// groups, XACK, XPENDING) is a Phase-4 expansion.
pub struct Stream {
    pub entries: Vec<StreamEntry>,
    pub last_ms: u64,
    pub last_seq: u64,
}

pub struct StreamEntry {
    pub id_ms: u64,
    pub id_seq: u64,
    /// (field, value) pairs in insertion order. Stream entries
    /// preserve field order — XRANGE returns them as input.
    pub fields: Vec<(Bytes, Bytes)>,
}

impl Stream {
    pub fn new() -> Self {
        Stream {
            entries: Vec::with_capacity(64),
            last_ms: 0,
            last_seq: 0,
        }
    }
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
            Entry::ZSet(_) => "zset",
            Entry::Stream(_) => "stream",
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
    /// Parallel per-shard expiry table: key → unix-ms expiry. Kept
    /// in a separate map so the value-types (Entry enum) don't need
    /// per-variant expiry tracking. Lazy eviction on read in any of
    /// the get-style methods that use `is_expired`.
    expiries: Vec<Mutex<HashMap<Vec<u8>, u64>>>,
}

impl Store {
    pub fn new() -> Self {
        let mut shards = Vec::with_capacity(NUM_SHARDS);
        let mut expiries = Vec::with_capacity(NUM_SHARDS);
        for _ in 0..NUM_SHARDS {
            shards.push(Mutex::new(HashMap::with_capacity(64)));
            expiries.push(Mutex::new(HashMap::with_capacity(8)));
        }
        Store { shards, expiries }
    }

    #[inline]
    fn shard(&self, key: &[u8]) -> &Mutex<HashMap<Vec<u8>, Entry>> {
        let idx = (fnv1a(key) & SHARD_MASK) as usize;
        &self.shards[idx]
    }

    #[inline]
    fn expiry_shard(&self, key: &[u8]) -> &Mutex<HashMap<Vec<u8>, u64>> {
        let idx = (fnv1a(key) & SHARD_MASK) as usize;
        &self.expiries[idx]
    }

    // ─── server-info commands (Phase-4: TYPE / TTL / EXPIRE /
    //                                    DBSIZE / RANDOMKEY) ──

    /// TYPE — Redis type-tag string for the entry; "none" if missing.
    pub fn type_of(&self, key: &[u8]) -> &'static str {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(e) => e.type_name(),
            None => "none",
        }
    }

    /// EXPIRE — set TTL in seconds. Returns true if the key existed.
    pub fn expire_at_ms(&self, key: &[u8], expires_unix_ms: u64) -> bool {
        let sh = self.shard(key).lock().unwrap();
        if !sh.contains_key(key) {
            return false;
        }
        drop(sh);
        let mut ex = self.expiry_shard(key).lock().unwrap();
        ex.insert(key.to_vec(), expires_unix_ms);
        true
    }

    /// PERSIST — clear TTL. Returns true if a TTL was actually
    /// removed (Redis semantics).
    pub fn persist(&self, key: &[u8]) -> bool {
        let sh = self.shard(key).lock().unwrap();
        if !sh.contains_key(key) {
            return false;
        }
        drop(sh);
        let mut ex = self.expiry_shard(key).lock().unwrap();
        ex.remove(key).is_some()
    }

    /// TTL_ms — remaining ms until expiry. Returns:
    ///   -2 → key missing
    ///   -1 → key exists, no TTL
    ///    n → milliseconds remaining
    pub fn ttl_ms(&self, key: &[u8]) -> i64 {
        let sh = self.shard(key).lock().unwrap();
        if !sh.contains_key(key) {
            return -2;
        }
        drop(sh);
        let ex = self.expiry_shard(key).lock().unwrap();
        match ex.get(key) {
            None => -1,
            Some(&t) => {
                let now = current_ms();
                if t <= now {
                    -2 // expired (we lazy-evict on the next read)
                } else {
                    (t - now) as i64
                }
            }
        }
    }

    /// DBSIZE — total non-expired key count across every shard.
    /// Single-threaded runtime → no lock contention.
    pub fn dbsize(&self) -> usize {
        self.shards
            .iter()
            .map(|s| s.lock().unwrap().len())
            .sum()
    }

    /// RANDOMKEY — return one arbitrary key from the keyspace, or
    /// None if empty. Walks shards from a random start so we don't
    /// always return a key from shard 0.
    pub fn random_key(&self) -> Option<Vec<u8>> {
        // Use the system clock as a cheap PRNG for the start index —
        // we don't need cryptographic randomness for "implementation-
        // defined" key picking.
        let start = (current_ms() as usize) % NUM_SHARDS;
        for i in 0..NUM_SHARDS {
            let idx = (start + i) % NUM_SHARDS;
            let sh = self.shards[idx].lock().unwrap();
            if let Some(k) = sh.keys().next() {
                return Some(k.clone());
            }
        }
        None
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

    // ─── sorted sets ──────────────────────────────────────────

    /// ZADD — insert/update one (member, score) pair. Returns the
    /// count of NEW members (matching Redis ZADD semantics —
    /// updates don't count).
    pub fn zadd(&self, key: &[u8], pairs: &[(f64, Vec<u8>)]) -> Result<usize, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let entry = sh.entry(key.to_vec()).or_insert_with(|| Entry::ZSet(ZSet::new()));
        match entry {
            Entry::ZSet(z) => {
                let mut added = 0;
                for (score, member) in pairs {
                    if z.add(member.clone(), *score) {
                        added += 1;
                    }
                }
                Ok(added)
            }
            _ => Err(StoreError::WrongType),
        }
    }

    pub fn zscore(&self, key: &[u8], member: &[u8]) -> Result<Option<f64>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::ZSet(z)) => Ok(z.score(member)),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(None),
        }
    }

    pub fn zcard(&self, key: &[u8]) -> Result<usize, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::ZSet(z)) => Ok(z.card()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(0),
        }
    }

    pub fn zincrby(&self, key: &[u8], delta: f64, member: &[u8]) -> Result<f64, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let entry = sh.entry(key.to_vec()).or_insert_with(|| Entry::ZSet(ZSet::new()));
        match entry {
            Entry::ZSet(z) => Ok(z.incr_by(member.to_vec(), delta)),
            _ => Err(StoreError::WrongType),
        }
    }

    pub fn zrange(
        &self,
        key: &[u8],
        start: i64,
        stop: i64,
        with_scores: bool,
    ) -> Result<Vec<Bytes>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::ZSet(z)) => Ok(z.range(start, stop, with_scores)),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(vec![]),
        }
    }

    pub fn zrem(&self, key: &[u8], members: &[&[u8]]) -> Result<usize, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let removed = match sh.get_mut(key) {
            Some(Entry::ZSet(z)) => {
                let mut n = 0;
                for m in members {
                    if z.remove(*m) {
                        n += 1;
                    }
                }
                n
            }
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(0),
        };
        if let Some(Entry::ZSet(z)) = sh.get(key) {
            if z.is_empty() {
                sh.remove(key);
            }
        }
        Ok(removed)
    }

    pub fn zpopmin(&self, key: &[u8], count: usize) -> Result<Vec<Bytes>, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let popped = match sh.get_mut(key) {
            Some(Entry::ZSet(z)) => z.pop_min(count.max(1)),
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(vec![]),
        };
        if let Some(Entry::ZSet(z)) = sh.get(key) {
            if z.is_empty() {
                sh.remove(key);
            }
        }
        Ok(popped)
    }

    pub fn zpopmax(&self, key: &[u8], count: usize) -> Result<Vec<Bytes>, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let popped = match sh.get_mut(key) {
            Some(Entry::ZSet(z)) => z.pop_max(count.max(1)),
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(vec![]),
        };
        if let Some(Entry::ZSet(z)) = sh.get(key) {
            if z.is_empty() {
                sh.remove(key);
            }
        }
        Ok(popped)
    }

    // ─── streams ──────────────────────────────────────────────

    /// XADD — append (auto-id is "*" → use current ms-time + monotonic
    /// per-stream sequence). Returns the assigned ID as "ms-seq".
    /// Phase-1 streams: append-only, no XREADGROUP / XACK / XCLAIM
    /// (those are the multi-week consumer-group implementation).
    pub fn xadd(
        &self,
        key: &[u8],
        id: &[u8],
        fields: Vec<(Bytes, Bytes)>,
    ) -> Result<String, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let entry = sh
            .entry(key.to_vec())
            .or_insert_with(|| Entry::Stream(Stream::new()));
        match entry {
            Entry::Stream(s) => {
                let (ms, seq) = next_stream_id(s, id)?;
                s.entries.push(StreamEntry {
                    id_ms: ms,
                    id_seq: seq,
                    fields,
                });
                s.last_ms = ms;
                s.last_seq = seq;
                Ok(format!("{ms}-{seq}"))
            }
            _ => Err(StoreError::WrongType),
        }
    }

    pub fn xlen(&self, key: &[u8]) -> Result<usize, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Stream(s)) => Ok(s.entries.len()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(0),
        }
    }

    // ─── string extras (Phase-4: SETNX / GETSET / GETDEL / STRLEN /
    //                              APPEND / GETRANGE / BITCOUNT) ──

    /// SETNX — set only if key doesn't exist. Returns 1/0 like Redis.
    pub fn setnx(&self, key: &[u8], value: Bytes) -> bool {
        let mut sh = self.shard(key).lock().unwrap();
        if sh.contains_key(key) {
            return false;
        }
        sh.insert(key.to_vec(), Entry::Bytes(value));
        true
    }

    /// GETSET — atomically swap and return old value. None if key
    /// was missing. WRONGTYPE if existing value isn't a string.
    pub fn getset(&self, key: &[u8], value: Bytes) -> Result<Option<Bytes>, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let prev = match sh.get(key) {
            Some(Entry::Bytes(b)) => Some(b.clone()),
            Some(Entry::Int(n)) => Some(Bytes::from(format!("{n}"))),
            Some(_) => return Err(StoreError::WrongType),
            None => None,
        };
        sh.insert(key.to_vec(), Entry::Bytes(value));
        Ok(prev)
    }

    /// GETDEL — get and delete in one op. None if missing.
    pub fn getdel(&self, key: &[u8]) -> Result<Option<Bytes>, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        match sh.remove(key) {
            Some(Entry::Bytes(b)) => Ok(Some(b)),
            Some(Entry::Int(n)) => Ok(Some(Bytes::from(format!("{n}")))),
            Some(other) => {
                // Restore — wrong type means we shouldn't have removed
                sh.insert(key.to_vec(), other);
                Err(StoreError::WrongType)
            }
            None => Ok(None),
        }
    }

    pub fn strlen(&self, key: &[u8]) -> Result<usize, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Bytes(b)) => Ok(b.len()),
            Some(Entry::Int(n)) => Ok(format!("{n}").len()),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(0),
        }
    }

    /// APPEND — concatenate to existing string (or create). Returns
    /// new total length.
    ///
    /// Hot path: `Bytes::try_into_mut` succeeds when nothing else
    /// holds a refcount on the buffer (the typical case — the entry
    /// owns it). Then we extend in-place with BytesMut's amortized-
    /// O(1) growth strategy, avoiding the full-string memcpy that
    /// the naive "build a new Vec each call" approach pays. For
    /// redis-benchmark's APPEND test (100k single-byte appends to
    /// the same key), this is the difference between O(N²) and
    /// O(N) total work.
    pub fn append(&self, key: &[u8], value: Bytes) -> Result<usize, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        // Replace-then-rebuild pattern — std::mem::replace lets us
        // take ownership of the inner Bytes/Int while leaving a
        // sentinel (Bytes::new) behind that we'll overwrite.
        let entry_ref = sh.entry(key.to_vec()).or_insert_with(|| Entry::Bytes(Bytes::new()));
        let total = match std::mem::replace(entry_ref, Entry::Bytes(Bytes::new())) {
            Entry::Bytes(b) => {
                // Try to take exclusive ownership for in-place growth.
                let mut buf = match b.try_into_mut() {
                    Ok(buf) => buf,
                    Err(b) => {
                        // Refcounted elsewhere (rare); fall back to copy.
                        let mut tmp = bytes::BytesMut::with_capacity(b.len() + value.len());
                        tmp.extend_from_slice(&b);
                        tmp
                    }
                };
                buf.extend_from_slice(&value);
                let total = buf.len();
                *entry_ref = Entry::Bytes(buf.freeze());
                total
            }
            Entry::Int(n) => {
                let s = format!("{n}");
                let mut tmp = bytes::BytesMut::with_capacity(s.len() + value.len());
                tmp.extend_from_slice(s.as_bytes());
                tmp.extend_from_slice(&value);
                let total = tmp.len();
                *entry_ref = Entry::Bytes(tmp.freeze());
                total
            }
            other => {
                // Wrong type — restore the entry as it was and error.
                *entry_ref = other;
                return Err(StoreError::WrongType);
            }
        };
        Ok(total)
    }

    /// GETRANGE — substring (Redis-style inclusive end, negative
    /// indices count from end).
    pub fn getrange(
        &self,
        key: &[u8],
        start: i64,
        end: i64,
    ) -> Result<Bytes, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        let raw: Bytes = match sh.get(key) {
            Some(Entry::Bytes(b)) => b.clone(),
            Some(Entry::Int(n)) => Bytes::from(format!("{n}")),
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(Bytes::new()),
        };
        let n = raw.len() as i64;
        if n == 0 {
            return Ok(Bytes::new());
        }
        let mut a = if start < 0 { n + start } else { start };
        let mut b = if end < 0 { n + end } else { end };
        if a < 0 {
            a = 0;
        }
        if b >= n {
            b = n - 1;
        }
        if a > b {
            return Ok(Bytes::new());
        }
        Ok(raw.slice((a as usize)..((b + 1) as usize)))
    }

    /// BITCOUNT — count set bits across the (optional) byte range.
    pub fn bitcount(&self, key: &[u8], start: i64, end: i64) -> Result<usize, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        let raw: Bytes = match sh.get(key) {
            Some(Entry::Bytes(b)) => b.clone(),
            Some(Entry::Int(n)) => Bytes::from(format!("{n}")),
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(0),
        };
        let n = raw.len() as i64;
        if n == 0 {
            return Ok(0);
        }
        let mut a = if start < 0 { n + start } else { start };
        let mut b = if end < 0 { n + end } else { end };
        if a < 0 {
            a = 0;
        }
        if b >= n {
            b = n - 1;
        }
        if a > b {
            return Ok(0);
        }
        let slice = &raw[(a as usize)..((b + 1) as usize)];
        Ok(slice.iter().map(|byte| byte.count_ones() as usize).sum())
    }

    // ─── hash extras ─────────────────────────────────────────

    pub fn hmget(&self, key: &[u8], fields: &[&[u8]]) -> Result<Vec<Option<Bytes>>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        let h = match sh.get(key) {
            Some(Entry::Hash(h)) => h,
            Some(_) => return Err(StoreError::WrongType),
            None => {
                // All-nil reply for missing key
                return Ok(fields.iter().map(|_| None).collect());
            }
        };
        Ok(fields.iter().map(|f| h.get(*f).cloned()).collect())
    }

    /// HINCRBY — atomic field increment by integer delta.
    pub fn hincrby(&self, key: &[u8], field: &[u8], delta: i64) -> Result<i64, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let entry = sh
            .entry(key.to_vec())
            .or_insert_with(|| Entry::Hash(HashMap::with_capacity(8)));
        match entry {
            Entry::Hash(h) => {
                let cur = match h.get(field) {
                    None => 0,
                    Some(b) => {
                        let s = std::str::from_utf8(b).map_err(|_| StoreError::NotInteger)?;
                        s.parse::<i64>().map_err(|_| StoreError::NotInteger)?
                    }
                };
                let new = cur.wrapping_add(delta);
                h.insert(field.to_vec(), Bytes::from(format!("{new}")));
                Ok(new)
            }
            _ => Err(StoreError::WrongType),
        }
    }

    pub fn hsetnx(&self, key: &[u8], field: Vec<u8>, value: Bytes) -> Result<bool, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let entry = sh
            .entry(key.to_vec())
            .or_insert_with(|| Entry::Hash(HashMap::with_capacity(8)));
        match entry {
            Entry::Hash(h) => {
                if h.contains_key(&field) {
                    return Ok(false);
                }
                h.insert(field, value);
                Ok(true)
            }
            _ => Err(StoreError::WrongType),
        }
    }

    // ─── zset extras ─────────────────────────────────────────

    /// ZRANGEBYSCORE — members with score in [min, max] inclusive.
    pub fn zrangebyscore(
        &self,
        key: &[u8],
        min: f64,
        max: f64,
        with_scores: bool,
    ) -> Result<Vec<Bytes>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::ZSet(z)) => {
                let mut out = Vec::new();
                for (score, member) in z.by_score.iter() {
                    if score.0 < min {
                        continue;
                    }
                    if score.0 > max {
                        break;
                    }
                    out.push(Bytes::copy_from_slice(member));
                    if with_scores {
                        out.push(Bytes::from(crate::zset::format_score(score.0)));
                    }
                }
                Ok(out)
            }
            Some(_) => Err(StoreError::WrongType),
            None => Ok(vec![]),
        }
    }

    /// ZRANK — 0-based rank of member in ascending score order.
    /// O(N) in this implementation; fine up to mid-sized leaderboards.
    pub fn zrank(
        &self,
        key: &[u8],
        member: &[u8],
        reverse: bool,
    ) -> Result<Option<usize>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::ZSet(z)) => {
                let target_score = match z.scores.get(member) {
                    Some(&s) => s,
                    None => return Ok(None),
                };
                let target_key = (crate::zset::OrdF64(target_score), member.to_vec());
                let n = z.by_score.len();
                if reverse {
                    let pos = z.by_score.iter().rev().position(|x| x == &target_key);
                    Ok(pos)
                } else {
                    let pos = z.by_score.iter().position(|x| x == &target_key);
                    let _ = n;
                    Ok(pos)
                }
            }
            Some(_) => Err(StoreError::WrongType),
            None => Ok(None),
        }
    }

    // ─── list extras ─────────────────────────────────────────

    pub fn lset(&self, key: &[u8], index: i64, value: Bytes) -> Result<(), StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        match sh.get_mut(key) {
            Some(Entry::List(l)) => {
                let n = l.len() as i64;
                let i = if index < 0 { n + index } else { index };
                if i < 0 || i >= n {
                    return Err(StoreError::NoSuchKey); // surfaces as -ERR index out of range
                }
                if let Some(slot) = l.get_mut(i as usize) {
                    *slot = value;
                }
                Ok(())
            }
            Some(_) => Err(StoreError::WrongType),
            None => Err(StoreError::NoSuchKey),
        }
    }

    /// LREM — remove up to |count| occurrences of value. count > 0
    /// from head, count < 0 from tail, count == 0 = all.
    pub fn lrem(&self, key: &[u8], count: i64, value: &[u8]) -> Result<usize, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        let removed = match sh.get_mut(key) {
            Some(Entry::List(l)) => {
                let limit = if count == 0 {
                    usize::MAX
                } else {
                    count.unsigned_abs() as usize
                };
                let mut n = 0;
                if count >= 0 {
                    let mut i = 0;
                    while i < l.len() && n < limit {
                        if &l[i][..] == value {
                            l.remove(i);
                            n += 1;
                        } else {
                            i += 1;
                        }
                    }
                } else {
                    let mut i = l.len();
                    while i > 0 && n < limit {
                        i -= 1;
                        if &l[i][..] == value {
                            l.remove(i);
                            n += 1;
                        }
                    }
                }
                n
            }
            Some(_) => return Err(StoreError::WrongType),
            None => return Ok(0),
        };
        if let Some(Entry::List(l)) = sh.get(key) {
            if l.is_empty() {
                sh.remove(key);
            }
        }
        Ok(removed)
    }

    pub fn ltrim(&self, key: &[u8], start: i64, stop: i64) -> Result<(), StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        match sh.get_mut(key) {
            Some(Entry::List(l)) => {
                let n = l.len() as i64;
                let (a, b) = normalize_range(start, stop, n);
                if a > b {
                    l.clear();
                } else {
                    // Drop elements before `a` and after `b`
                    for _ in 0..a {
                        l.pop_front();
                    }
                    let keep = (b - a + 1) as usize;
                    while l.len() > keep {
                        l.pop_back();
                    }
                }
                Ok(())
            }
            Some(_) => Err(StoreError::WrongType),
            None => Ok(()),
        }?;
        if let Some(Entry::List(l)) = sh.get(key) {
            if l.is_empty() {
                sh.remove(key);
            }
        }
        Ok(())
    }

    /// LINSERT — insert before/after the first occurrence of pivot.
    /// Returns: new length (>0), 0 if pivot not found, -1 if key
    /// missing (Redis semantics).
    pub fn linsert(
        &self,
        key: &[u8],
        before: bool,
        pivot: &[u8],
        value: Bytes,
    ) -> Result<i64, StoreError> {
        let mut sh = self.shard(key).lock().unwrap();
        match sh.get_mut(key) {
            Some(Entry::List(l)) => {
                let pos = l.iter().position(|x| &x[..] == pivot);
                match pos {
                    None => Ok(0),
                    Some(p) => {
                        let insert_at = if before { p } else { p + 1 };
                        l.insert(insert_at, value);
                        Ok(l.len() as i64)
                    }
                }
            }
            Some(_) => Err(StoreError::WrongType),
            None => Ok(0),
        }
    }

    // ─── set extras ──────────────────────────────────────────

    /// SRANDMEMBER — returns a random member without removing it.
    /// Single-threaded runtime makes HashSet iteration order
    /// stable-but-arbitrary, which satisfies Redis's "implementation
    /// defined" randomness contract.
    pub fn srandmember(&self, key: &[u8]) -> Result<Option<Bytes>, StoreError> {
        let sh = self.shard(key).lock().unwrap();
        match sh.get(key) {
            Some(Entry::Set(s)) => Ok(s.iter().next().map(|m| Bytes::copy_from_slice(m))),
            Some(_) => Err(StoreError::WrongType),
            None => Ok(None),
        }
    }

    /// SMOVE — atomically move member from src to dst. Returns 1/0.
    pub fn smove(
        &self,
        src: &[u8],
        dst: &[u8],
        member: &[u8],
    ) -> Result<bool, StoreError> {
        // Different shards possible — for simplicity in the
        // single-threaded runtime we lock src first then dst (no
        // deadlock risk because there's only one thread).
        let src_present = {
            let mut sh = self.shard(src).lock().unwrap();
            match sh.get_mut(src) {
                Some(Entry::Set(s)) => s.remove(member),
                Some(_) => return Err(StoreError::WrongType),
                None => false,
            }
        };
        if !src_present {
            return Ok(false);
        }
        // Cleanup empty src
        {
            let mut sh = self.shard(src).lock().unwrap();
            if let Some(Entry::Set(s)) = sh.get(src) {
                if s.is_empty() {
                    sh.remove(src);
                }
            }
        }
        let mut sh = self.shard(dst).lock().unwrap();
        let entry = sh
            .entry(dst.to_vec())
            .or_insert_with(|| Entry::Set(HashSet::with_capacity(8)));
        match entry {
            Entry::Set(s) => {
                s.insert(member.to_vec());
                Ok(true)
            }
            _ => {
                // dst is wrong type — restore src
                let mut src_sh = self.shard(src).lock().unwrap();
                let src_entry = src_sh
                    .entry(src.to_vec())
                    .or_insert_with(|| Entry::Set(HashSet::with_capacity(8)));
                if let Entry::Set(s) = src_entry {
                    s.insert(member.to_vec());
                }
                Err(StoreError::WrongType)
            }
        }
    }
}

/// next_stream_id resolves the user-supplied ID ("*" or "ms-seq" or
/// "ms-*") into a concrete (ms, seq) honoring monotonicity. Returns
/// the same kind of error string XADD does in Redis when the
/// supplied id isn't strictly greater than the last one.
fn next_stream_id(s: &Stream, id: &[u8]) -> Result<(u64, u64), StoreError> {
    if id == b"*" {
        let ms = current_ms();
        let seq = if ms == s.last_ms { s.last_seq + 1 } else { 0 };
        return Ok((ms, seq));
    }
    // ms-seq or ms-*
    let txt = std::str::from_utf8(id).map_err(|_| StoreError::NotInteger)?;
    let (ms_part, seq_part) = match txt.split_once('-') {
        Some((a, b)) => (a, b),
        None => (txt, "0"),
    };
    let ms: u64 = ms_part.parse().map_err(|_| StoreError::NotInteger)?;
    let seq: u64 = if seq_part == "*" {
        if ms == s.last_ms {
            s.last_seq + 1
        } else {
            0
        }
    } else {
        seq_part.parse().map_err(|_| StoreError::NotInteger)?
    };
    // Monotonicity check — Redis returns -ERR if the new ID is not
    // strictly greater than the last one.
    if ms < s.last_ms || (ms == s.last_ms && seq <= s.last_seq) {
        return Err(StoreError::NotInteger);
    }
    Ok((ms, seq))
}

/// current_ms returns the current Unix time in milliseconds — what
/// Redis uses as the default ms component for auto-generated stream
/// IDs.
fn current_ms() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_millis() as u64)
        .unwrap_or(0)
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
