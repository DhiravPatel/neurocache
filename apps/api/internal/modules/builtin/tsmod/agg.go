package tsmod

import (
	"errors"
	"math"
	"sort"
	"strings"
)

// AggType enumerates every aggregation TS.RANGE / TS.CREATERULE accept.
// Names match the Redis wire form so callers can pass through verbatim.
type AggType int

const (
	AggNone AggType = iota
	AggAvg
	AggSum
	AggMin
	AggMax
	AggRange
	AggCount
	AggFirst
	AggLast
	AggStdP
	AggStdS
	AggVarP
	AggVarS
)

// String renders the aggregator in canonical form.
func (a AggType) String() string {
	switch a {
	case AggAvg:
		return "AVG"
	case AggSum:
		return "SUM"
	case AggMin:
		return "MIN"
	case AggMax:
		return "MAX"
	case AggRange:
		return "RANGE"
	case AggCount:
		return "COUNT"
	case AggFirst:
		return "FIRST"
	case AggLast:
		return "LAST"
	case AggStdP:
		return "STD.P"
	case AggStdS:
		return "STD.S"
	case AggVarP:
		return "VAR.P"
	case AggVarS:
		return "VAR.S"
	}
	return "NONE"
}

// ParseAggType resolves the textual form. Unknown returns an error so
// callers can surface "unknown aggregator" cleanly.
func ParseAggType(s string) (AggType, error) {
	switch strings.ToUpper(s) {
	case "AVG":
		return AggAvg, nil
	case "SUM":
		return AggSum, nil
	case "MIN":
		return AggMin, nil
	case "MAX":
		return AggMax, nil
	case "RANGE":
		return AggRange, nil
	case "COUNT":
		return AggCount, nil
	case "FIRST":
		return AggFirst, nil
	case "LAST":
		return AggLast, nil
	case "STD.P":
		return AggStdP, nil
	case "STD.S":
		return AggStdS, nil
	case "VAR.P":
		return AggVarP, nil
	case "VAR.S":
		return AggVarS, nil
	}
	return 0, errors.New("invalid aggregator")
}

// Bucket samples by bucketMs and reduce per-bucket via agg. Bucket
// alignment is `from - (from % bucketMs) + (alignTS % bucketMs)`.
// Returns synthesised samples whose timestamps are the bucket starts.
func aggregate(samples []Sample, agg AggType, bucketMs int64, alignTS int64) []Sample {
	if agg == AggNone || bucketMs <= 0 || len(samples) == 0 {
		return samples
	}
	out := []Sample{}
	var (
		curBucket int64 = math.MinInt64
		acc       accumulator
	)
	for _, s := range samples {
		bucket := alignBucket(s.TS, bucketMs, alignTS)
		if bucket != curBucket {
			if curBucket != math.MinInt64 {
				out = append(out, Sample{TS: curBucket, Value: acc.Result(agg)})
			}
			curBucket = bucket
			acc = accumulator{}
		}
		acc.Add(s.Value)
	}
	if curBucket != math.MinInt64 {
		out = append(out, Sample{TS: curBucket, Value: acc.Result(agg)})
	}
	return out
}

func alignBucket(ts, bucketMs, alignTS int64) int64 {
	if bucketMs <= 0 {
		return ts
	}
	off := ((ts - alignTS) / bucketMs) * bucketMs
	return alignTS + off
}

// accumulator holds the running stats one bucket needs. We keep enough
// state for every supported aggregator so a single pass covers all of
// them — matters because TS.MRANGE may aggregate huge ranges.
type accumulator struct {
	count       int64
	sum         float64
	min, max    float64
	first, last float64
	hasMin      bool
	// online variance via Welford's algorithm
	mean float64
	m2   float64
}

func (a *accumulator) Add(v float64) {
	if a.count == 0 {
		a.min, a.max = v, v
		a.first = v
	} else {
		if v < a.min {
			a.min = v
		}
		if v > a.max {
			a.max = v
		}
	}
	a.last = v
	a.sum += v
	a.count++
	delta := v - a.mean
	a.mean += delta / float64(a.count)
	a.m2 += delta * (v - a.mean)
	a.hasMin = true
}

func (a *accumulator) Result(agg AggType) float64 {
	if a.count == 0 {
		return 0
	}
	switch agg {
	case AggAvg:
		return a.sum / float64(a.count)
	case AggSum:
		return a.sum
	case AggMin:
		return a.min
	case AggMax:
		return a.max
	case AggRange:
		return a.max - a.min
	case AggCount:
		return float64(a.count)
	case AggFirst:
		return a.first
	case AggLast:
		return a.last
	case AggVarP:
		return a.m2 / float64(a.count)
	case AggVarS:
		if a.count <= 1 {
			return 0
		}
		return a.m2 / float64(a.count-1)
	case AggStdP:
		return math.Sqrt(a.m2 / float64(a.count))
	case AggStdS:
		if a.count <= 1 {
			return 0
		}
		return math.Sqrt(a.m2 / float64(a.count-1))
	}
	return 0
}

// silence unused-sort import if a future refactor inlines the search
var _ = sort.Search
