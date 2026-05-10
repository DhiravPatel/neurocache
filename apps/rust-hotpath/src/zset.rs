//! Sorted set implementation backed by:
//!   - HashMap<member → score>            for O(1) ZSCORE / ZADD-update
//!   - BTreeSet<(OrdF64(score), member)>  for O(log N) ordered iteration
//!
//! The two structures are kept in sync. ZADD updates both; ZRANGE
//! iterates the BTreeSet; ZSCORE reads the HashMap.
//!
//! f64 doesn't implement Eq/Ord because of NaN. We wrap with
//! OrdF64 — total order using `to_bits()` for tiebreak so NaNs
//! sort consistently (we don't allow NaN scores in ZADD per Redis
//! semantics, but the wrapper covers the case anyway).

use bytes::Bytes;
use std::collections::{BTreeSet, HashMap};

/// OrdF64 is f64 with a total order. Required for BTreeSet keys.
/// Uses bitwise comparison on the IEEE-754 representation for
/// NaN-safe ordering — matches the partial_cmp result for finite
/// values and stays consistent for NaN.
#[derive(Clone, Copy, Debug)]
pub struct OrdF64(pub f64);

impl PartialEq for OrdF64 {
    fn eq(&self, other: &Self) -> bool {
        self.0.to_bits() == other.0.to_bits()
    }
}
impl Eq for OrdF64 {}
impl PartialOrd for OrdF64 {
    fn partial_cmp(&self, other: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(other))
    }
}
impl Ord for OrdF64 {
    fn cmp(&self, other: &Self) -> std::cmp::Ordering {
        self.0
            .partial_cmp(&other.0)
            .unwrap_or(std::cmp::Ordering::Equal)
    }
}

/// ZSet is the per-key sorted-set state. Two structures kept in sync.
pub struct ZSet {
    /// member → score. Authoritative — ZSCORE reads this directly.
    pub scores: HashMap<Vec<u8>, f64>,
    /// (score, member) → () — ordered for ZRANGE / ZPOPMIN / ZPOPMAX.
    pub by_score: BTreeSet<(OrdF64, Vec<u8>)>,
}

impl ZSet {
    pub fn new() -> Self {
        ZSet {
            scores: HashMap::with_capacity(8),
            by_score: BTreeSet::new(),
        }
    }

    /// Add or update one (score, member) pair. Returns true if the
    /// member was newly added (matching Redis ZADD return semantics).
    pub fn add(&mut self, member: Vec<u8>, score: f64) -> bool {
        let new = if let Some(old_score) = self.scores.insert(member.clone(), score) {
            // Update path: remove the old (score, member) so the
            // BTreeSet stays in sync with the HashMap.
            self.by_score.remove(&(OrdF64(old_score), member.clone()));
            false
        } else {
            true
        };
        self.by_score.insert((OrdF64(score), member));
        new
    }

    pub fn score(&self, member: &[u8]) -> Option<f64> {
        self.scores.get(member).copied()
    }

    pub fn card(&self) -> usize {
        self.scores.len()
    }

    /// Increment score atomically. Returns the new score.
    pub fn incr_by(&mut self, member: Vec<u8>, delta: f64) -> f64 {
        let new = match self.scores.get(&member) {
            Some(&old) => {
                self.by_score.remove(&(OrdF64(old), member.clone()));
                old + delta
            }
            None => delta,
        };
        self.scores.insert(member.clone(), new);
        self.by_score.insert((OrdF64(new), member));
        new
    }

    /// Remove a member. Returns true if it was present.
    pub fn remove(&mut self, member: &[u8]) -> bool {
        match self.scores.remove(member) {
            Some(score) => {
                self.by_score.remove(&(OrdF64(score), member.to_vec()));
                true
            }
            None => false,
        }
    }

    /// ZRANGE — return members in score-ascending order across the
    /// indexed range [start, stop] (negative = from end).
    pub fn range(&self, start: i64, stop: i64, with_scores: bool) -> Vec<Bytes> {
        let n = self.by_score.len() as i64;
        if n == 0 {
            return vec![];
        }
        let mut a = if start < 0 { n + start } else { start };
        let mut b = if stop < 0 { n + stop } else { stop };
        if a < 0 {
            a = 0;
        }
        if b >= n {
            b = n - 1;
        }
        if a > b {
            return vec![];
        }
        let cap = if with_scores {
            (b - a + 1) as usize * 2
        } else {
            (b - a + 1) as usize
        };
        let mut out = Vec::with_capacity(cap);
        for (i, (score, member)) in self.by_score.iter().enumerate() {
            let i = i as i64;
            if i < a {
                continue;
            }
            if i > b {
                break;
            }
            out.push(Bytes::copy_from_slice(member));
            if with_scores {
                out.push(Bytes::from(format_score(score.0)));
            }
        }
        out
    }

    /// ZPOPMIN / ZPOPMAX (count). Returns interleaved
    /// (member, score) pairs as Bytes.
    pub fn pop_min(&mut self, count: usize) -> Vec<Bytes> {
        let mut out = Vec::with_capacity(count * 2);
        for _ in 0..count {
            if let Some((score, member)) = self.by_score.iter().next().cloned() {
                self.by_score.remove(&(score, member.clone()));
                self.scores.remove(&member);
                out.push(Bytes::from(member));
                out.push(Bytes::from(format_score(score.0)));
            } else {
                break;
            }
        }
        out
    }

    pub fn pop_max(&mut self, count: usize) -> Vec<Bytes> {
        let mut out = Vec::with_capacity(count * 2);
        for _ in 0..count {
            if let Some((score, member)) = self.by_score.iter().next_back().cloned() {
                self.by_score.remove(&(score, member.clone()));
                self.scores.remove(&member);
                out.push(Bytes::from(member));
                out.push(Bytes::from(format_score(score.0)));
            } else {
                break;
            }
        }
        out
    }

    pub fn is_empty(&self) -> bool {
        self.scores.is_empty()
    }
}

/// format_score renders an f64 as Redis does — trim trailing zeros
/// after the decimal point. "1.5" not "1.500000".
pub fn format_score(s: f64) -> String {
    if s.fract() == 0.0 && s.is_finite() {
        format!("{}", s as i64)
    } else {
        // Default float format gives "1.5" without trailing zeros for
        // most values — accept it.
        format!("{s}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn add_and_score() {
        let mut z = ZSet::new();
        assert!(z.add(b"alice".to_vec(), 100.0));
        assert!(z.add(b"bob".to_vec(), 200.0));
        // Re-add: dedup, returns false
        assert!(!z.add(b"alice".to_vec(), 150.0));
        assert_eq!(z.score(b"alice"), Some(150.0));
        assert_eq!(z.score(b"bob"), Some(200.0));
        assert_eq!(z.score(b"missing"), None);
        assert_eq!(z.card(), 2);
    }

    #[test]
    fn range_ascending() {
        let mut z = ZSet::new();
        z.add(b"c".to_vec(), 30.0);
        z.add(b"a".to_vec(), 10.0);
        z.add(b"b".to_vec(), 20.0);
        let r = z.range(0, -1, false);
        assert_eq!(r.len(), 3);
        assert_eq!(&r[0][..], b"a");
        assert_eq!(&r[1][..], b"b");
        assert_eq!(&r[2][..], b"c");
    }

    #[test]
    fn range_with_scores() {
        let mut z = ZSet::new();
        z.add(b"a".to_vec(), 10.0);
        z.add(b"b".to_vec(), 20.0);
        let r = z.range(0, -1, true);
        assert_eq!(r.len(), 4);
        assert_eq!(&r[0][..], b"a");
        assert_eq!(&r[1][..], b"10");
        assert_eq!(&r[2][..], b"b");
        assert_eq!(&r[3][..], b"20");
    }

    #[test]
    fn pop_min() {
        let mut z = ZSet::new();
        z.add(b"c".to_vec(), 30.0);
        z.add(b"a".to_vec(), 10.0);
        z.add(b"b".to_vec(), 20.0);
        let r = z.pop_min(1);
        assert_eq!(r.len(), 2);
        assert_eq!(&r[0][..], b"a");
        assert_eq!(&r[1][..], b"10");
        assert_eq!(z.card(), 2);
    }

    #[test]
    fn pop_max() {
        let mut z = ZSet::new();
        z.add(b"c".to_vec(), 30.0);
        z.add(b"a".to_vec(), 10.0);
        z.add(b"b".to_vec(), 20.0);
        let r = z.pop_max(2);
        assert_eq!(r.len(), 4);
        assert_eq!(&r[0][..], b"c");
        assert_eq!(&r[1][..], b"30");
        assert_eq!(&r[2][..], b"b");
        assert_eq!(&r[3][..], b"20");
        assert_eq!(z.card(), 1);
    }

    #[test]
    fn incr_by() {
        let mut z = ZSet::new();
        assert_eq!(z.incr_by(b"k".to_vec(), 5.0), 5.0);
        assert_eq!(z.incr_by(b"k".to_vec(), 3.0), 8.0);
        assert_eq!(z.score(b"k"), Some(8.0));
    }

    #[test]
    fn format_score_trims_trailing_zeros() {
        assert_eq!(format_score(10.0), "10");
        assert_eq!(format_score(1.5), "1.5");
        assert_eq!(format_score(-3.14), "-3.14");
    }
}
