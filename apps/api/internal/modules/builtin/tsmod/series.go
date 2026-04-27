// Package tsmod implements the RedisTimeSeries (TS.*) command surface
// as a NeuroCache module. Each key holds an ordered ring of (timestamp,
// value) samples plus a label map for filterable queries.
//
// Storage shape:
//
//   - Samples are kept sorted by timestamp in a slice. Reasonable for
//     workloads up to a few million points; a chunked layout (matching
//     RedisTimeSeries' compressed chunks) is a future optimisation.
//   - Retention is a soft maximum age in milliseconds; older samples
//     are evicted on insert.
//   - Labels are a map[string]string indexed at the module level so
//     TS.QUERYINDEX / TS.MRANGE can resolve filters without scanning
//     every key.
//   - Downsampling rules link a source series to a destination series
//     plus an aggregation type + bucket size.
package tsmod

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// DuplicatePolicy mirrors Redis's "DUPLICATE_POLICY". It governs what
// happens when an insert has the same timestamp as an existing sample.
type DuplicatePolicy int

const (
	DupBlock   DuplicatePolicy = iota // reject the insert (default in Redis)
	DupFirst                          // keep the existing value
	DupLast                           // overwrite with the new value
	DupMin                            // keep the smaller
	DupMax                            // keep the larger
	DupSum                            // accumulate
)

// String renders the policy in the same wire form Redis uses.
func (p DuplicatePolicy) String() string {
	switch p {
	case DupFirst:
		return "FIRST"
	case DupLast:
		return "LAST"
	case DupMin:
		return "MIN"
	case DupMax:
		return "MAX"
	case DupSum:
		return "SUM"
	}
	return "BLOCK"
}

// ParseDuplicatePolicy decodes the textual form into the enum.
func ParseDuplicatePolicy(s string) (DuplicatePolicy, error) {
	switch s {
	case "BLOCK":
		return DupBlock, nil
	case "FIRST":
		return DupFirst, nil
	case "LAST":
		return DupLast, nil
	case "MIN":
		return DupMin, nil
	case "MAX":
		return DupMax, nil
	case "SUM":
		return DupSum, nil
	}
	return 0, errors.New("invalid duplicate policy")
}

// Sample is one (timestamp, value) point. Timestamp is unix-ms.
type Sample struct {
	TS    int64
	Value float64
}

// Rule links this series (the source) to a destination series with
// a downsampling aggregation. One source can have many rules; one
// destination is targeted by exactly one rule.
type Rule struct {
	DestKey    string
	Aggregator AggType
	BucketMs   int64
	AlignTS    int64 // timestamp alignment for buckets (0 = epoch)
	curBucket  int64 // most-recently-flushed bucket start
	acc        accumulator
}

// Series is the per-key data. Mutable under mu — every TS command on
// the same key serializes through it. Reads can take RLock.
type Series struct {
	mu sync.RWMutex

	Samples       []Sample
	Labels        map[string]string
	RetentionMs   int64 // 0 = forever
	ChunkSize     int64 // advisory; we don't chunk yet
	DuplicateMode DuplicatePolicy

	// Downsampling rules feeding this series' compactions.
	Rules []*Rule

	// Source pointer for compaction targets. nil for plain series.
	SourceKey string
}

// NewSeries builds an empty series with sane defaults.
func NewSeries(labels map[string]string, retentionMs int64) *Series {
	if labels == nil {
		labels = map[string]string{}
	}
	return &Series{
		Labels: labels, RetentionMs: retentionMs,
		ChunkSize: 4096, DuplicateMode: DupBlock,
	}
}

// Add inserts a sample. Returns the timestamp actually stored (handy
// when callers pass "*" → current ms).
func (s *Series) Add(ts int64, val float64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ts < 0 {
		ts = nowMillis()
	}
	if len(s.Samples) > 0 {
		last := s.Samples[len(s.Samples)-1]
		if ts < last.TS {
			return 0, errors.New("TSDB: timestamp is older than the latest sample")
		}
		if ts == last.TS {
			switch s.DuplicateMode {
			case DupBlock:
				return 0, errors.New("TSDB: Error at upsert, sample timestamp is older than retention or duplicate")
			case DupFirst:
				return ts, nil
			case DupLast:
				s.Samples[len(s.Samples)-1].Value = val
				return ts, nil
			case DupMin:
				if val < last.Value {
					s.Samples[len(s.Samples)-1].Value = val
				}
				return ts, nil
			case DupMax:
				if val > last.Value {
					s.Samples[len(s.Samples)-1].Value = val
				}
				return ts, nil
			case DupSum:
				s.Samples[len(s.Samples)-1].Value = last.Value + val
				return ts, nil
			}
		}
	}
	s.Samples = append(s.Samples, Sample{TS: ts, Value: val})
	s.evictOld()
	return ts, nil
}

// IncrBy / DecrBy add to the most recent sample (or insert a fresh one
// at the given timestamp). Mirrors Redis's TS.INCRBY behaviour.
func (s *Series) IncrBy(ts int64, delta float64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ts < 0 {
		ts = nowMillis()
	}
	if len(s.Samples) > 0 {
		last := &s.Samples[len(s.Samples)-1]
		if last.TS == ts {
			last.Value += delta
			return ts, nil
		}
		if ts < last.TS {
			return 0, errors.New("TSDB: timestamp is older than the latest sample")
		}
		// carry the running value forward
		s.Samples = append(s.Samples, Sample{TS: ts, Value: last.Value + delta})
	} else {
		s.Samples = append(s.Samples, Sample{TS: ts, Value: delta})
	}
	s.evictOld()
	return ts, nil
}

// Get returns the latest sample, or false when the series is empty.
func (s *Series) Get() (Sample, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.Samples) == 0 {
		return Sample{}, false
	}
	return s.Samples[len(s.Samples)-1], true
}

// Range returns samples in [from, to] inclusive (ms timestamps). count
// caps the result; <=0 means unlimited. reverse walks from the tail.
func (s *Series) Range(from, to int64, reverse bool, count int) []Sample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	lo := sort.Search(len(s.Samples), func(i int) bool { return s.Samples[i].TS >= from })
	hi := sort.Search(len(s.Samples), func(i int) bool { return s.Samples[i].TS > to })
	if lo == hi {
		return nil
	}
	src := s.Samples[lo:hi]
	out := make([]Sample, len(src))
	if reverse {
		for i := range src {
			out[i] = src[len(src)-1-i]
		}
	} else {
		copy(out, src)
	}
	if count > 0 && count < len(out) {
		out = out[:count]
	}
	return out
}

// DeleteRange removes samples in [from, to] inclusive and returns the
// number actually removed.
func (s *Series) DeleteRange(from, to int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	lo := sort.Search(len(s.Samples), func(i int) bool { return s.Samples[i].TS >= from })
	hi := sort.Search(len(s.Samples), func(i int) bool { return s.Samples[i].TS > to })
	if lo == hi {
		return 0
	}
	removed := hi - lo
	s.Samples = append(s.Samples[:lo], s.Samples[hi:]...)
	return removed
}

// Len reports the live sample count after retention.
func (s *Series) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.Samples)
}

// MemUsage is an approximate byte count for the series — used by the
// engine's MEMORY accounting and eviction scorer.
func (s *Series) MemUsage() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := int64(16 * len(s.Samples)) // 8 bytes ts + 8 bytes value
	for k, v := range s.Labels {
		n += int64(len(k)) + int64(len(v))
	}
	return n + 64
}

// FirstTS / LastTS return the boundary timestamps (or 0 when empty).
func (s *Series) FirstTS() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.Samples) == 0 {
		return 0
	}
	return s.Samples[0].TS
}

func (s *Series) LastTS() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.Samples) == 0 {
		return 0
	}
	return s.Samples[len(s.Samples)-1].TS
}

// AddRule attaches a downsampling rule to this source series. Returns
// an error if a rule for the same destination already exists.
func (s *Series) AddRule(r *Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.Rules {
		if ex.DestKey == r.DestKey {
			return errors.New("TSDB: the destination key already has a rule")
		}
	}
	s.Rules = append(s.Rules, r)
	return nil
}

// DeleteRule removes a rule by destination key. Returns true on hit.
func (s *Series) DeleteRule(destKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ex := range s.Rules {
		if ex.DestKey == destKey {
			s.Rules = append(s.Rules[:i], s.Rules[i+1:]...)
			return true
		}
	}
	return false
}

// LabelEquals reports whether label k has value v on this series.
func (s *Series) LabelEquals(k, v string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	got, ok := s.Labels[k]
	return ok && got == v
}

// HasLabel returns whether the label exists, regardless of value.
func (s *Series) HasLabel(k string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.Labels[k]
	return ok
}

// LabelValueIn returns whether the label value matches any of vs.
func (s *Series) LabelValueIn(k string, vs []string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	got, ok := s.Labels[k]
	if !ok {
		return false
	}
	for _, v := range vs {
		if got == v {
			return true
		}
	}
	return false
}

// evictOld drops samples older than now-RetentionMs. Caller holds mu.
func (s *Series) evictOld() {
	if s.RetentionMs <= 0 || len(s.Samples) == 0 {
		return
	}
	cutoff := s.Samples[len(s.Samples)-1].TS - s.RetentionMs
	if cutoff <= 0 {
		return
	}
	idx := sort.Search(len(s.Samples), func(i int) bool { return s.Samples[i].TS >= cutoff })
	if idx > 0 {
		s.Samples = append(s.Samples[:0], s.Samples[idx:]...)
	}
}

// nowMillis is a tiny seam so tests can pin the clock.
var nowMillis = func() int64 { return time.Now().UnixMilli() }
