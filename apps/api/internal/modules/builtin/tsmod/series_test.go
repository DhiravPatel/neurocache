package tsmod

import "testing"

func TestSeriesAddAndRange(t *testing.T) {
	s := NewSeries(map[string]string{"sensor": "temp"}, 0)
	for i := int64(1); i <= 10; i++ {
		if _, err := s.Add(i*1000, float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Range(3000, 7000, false, 0)
	if len(got) != 5 {
		t.Fatalf("range got %d, want 5", len(got))
	}
	if got[0].TS != 3000 || got[4].TS != 7000 {
		t.Fatalf("range bounds wrong: %+v", got)
	}
}

func TestSeriesRetentionEvicts(t *testing.T) {
	s := NewSeries(nil, 5000) // 5s retention
	s.Add(1000, 1)
	s.Add(2000, 2)
	s.Add(4000, 3) // 4000 - 5000 = -1000 → no eviction yet
	if s.Len() != 3 {
		t.Fatalf("premature eviction: len=%d", s.Len())
	}
	s.Add(8000, 4) // cutoff = 3000, keeps samples with TS >= 3000 → 4000 + 8000
	if s.Len() != 2 {
		t.Fatalf("retention eviction wrong: len=%d", s.Len())
	}
	if s.FirstTS() != 4000 {
		t.Fatalf("FirstTS=%d, want 4000", s.FirstTS())
	}
}

func TestSeriesDuplicatePolicies(t *testing.T) {
	cases := []struct {
		policy DuplicatePolicy
		first  float64
		second float64
		want   float64
	}{
		{DupLast, 5, 7, 7},
		{DupFirst, 5, 7, 5},
		{DupMin, 5, 7, 5},
		{DupMax, 5, 7, 7},
		{DupSum, 5, 7, 12},
	}
	for _, tc := range cases {
		s := NewSeries(nil, 0)
		s.DuplicateMode = tc.policy
		s.Add(1000, tc.first)
		if _, err := s.Add(1000, tc.second); err != nil {
			t.Fatalf("%v: unexpected err %v", tc.policy, err)
		}
		last, _ := s.Get()
		if last.Value != tc.want {
			t.Errorf("%v: got %v want %v", tc.policy, last.Value, tc.want)
		}
	}
}

func TestAggregateAvg(t *testing.T) {
	samples := []Sample{
		{1000, 10}, {1500, 20}, {2000, 30}, {2500, 40},
	}
	out := aggregate(samples, AggAvg, 1000, 0)
	if len(out) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(out))
	}
	// Bucket 1000-1999: avg(10, 20) = 15
	// Bucket 2000-2999: avg(30, 40) = 35
	if out[0].Value != 15 || out[1].Value != 35 {
		t.Fatalf("aggregations wrong: %+v", out)
	}
}

func TestAggregateMinMaxRange(t *testing.T) {
	samples := []Sample{{0, 1}, {500, 5}, {900, 3}}
	if got := aggregate(samples, AggMin, 1000, 0); got[0].Value != 1 {
		t.Fatalf("min got %v", got)
	}
	if got := aggregate(samples, AggMax, 1000, 0); got[0].Value != 5 {
		t.Fatalf("max got %v", got)
	}
	if got := aggregate(samples, AggRange, 1000, 0); got[0].Value != 4 {
		t.Fatalf("range got %v", got)
	}
}

func TestSeriesMarshalRoundTrip(t *testing.T) {
	s := NewSeries(map[string]string{"sensor": "temp", "loc": "kitchen"}, 60000)
	s.DuplicateMode = DupLast
	for i := int64(1); i <= 5; i++ {
		s.Add(i*1000, float64(i*10))
	}
	s.Rules = []*Rule{{DestKey: "agg", Aggregator: AggAvg, BucketMs: 5000}}
	blob, err := s.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Unmarshal(blob)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Len() != 5 {
		t.Fatalf("roundtrip lost samples: %d", s2.Len())
	}
	if s2.Labels["sensor"] != "temp" || s2.Labels["loc"] != "kitchen" {
		t.Fatalf("labels lost: %+v", s2.Labels)
	}
	if len(s2.Rules) != 1 || s2.Rules[0].DestKey != "agg" {
		t.Fatalf("rules lost: %+v", s2.Rules)
	}
}
