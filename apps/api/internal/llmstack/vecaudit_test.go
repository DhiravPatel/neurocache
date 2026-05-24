package llmstack

import (
	"math/rand"
	"testing"
)

// makeNormalVec returns a random unit vector with the given seed.
func makeNormalVec(seed int64, dim int) []float64 {
	r := rand.New(rand.NewSource(seed))
	out := make([]float64, dim)
	for i := range out {
		out[i] = r.NormFloat64()
	}
	l2NormaliseInPlace(out)
	return out
}

func TestVecAuditNoBaseline(t *testing.T) {
	a := NewVectorAudit()
	r, _ := a.Check("docs", makeNormalVec(1, 16))
	if r.Verdict != "no_baseline" {
		t.Fatalf("verdict = %s", r.Verdict)
	}
}

func TestVecAuditNormalVectorStable(t *testing.T) {
	a := NewVectorAudit()
	const dim = 16
	baseline := make([][]float64, 30)
	for i := range baseline {
		baseline[i] = makeNormalVec(int64(i+1), dim)
	}
	a.Baseline("docs", baseline)
	// A new vector with same statistics should be stable
	r, _ := a.Check("docs", makeNormalVec(999, dim))
	if r.Verdict == "poison" {
		t.Fatalf("normal vector flagged poison: %+v", r)
	}
}

func TestVecAuditTooCentralFlagged(t *testing.T) {
	a := NewVectorAudit()
	const dim = 16
	baseline := make([][]float64, 30)
	for i := range baseline {
		baseline[i] = makeNormalVec(int64(i+1), dim)
	}
	a.Baseline("docs", baseline)

	// Centroid itself is the most central vector possible. Inserting
	// it directly should fire the "too central" signal.
	st, _ := a.Status("docs")
	if st.BaselineSize != 30 {
		t.Fatalf("baseline size = %d", st.BaselineSize)
	}
	// Build a vector that is exactly the centroid.
	idx := a.indexes["docs"]
	idx.mu.RLock()
	central := make([]float64, len(idx.centroid))
	copy(central, idx.centroid)
	idx.mu.RUnlock()

	r, _ := a.Check("docs", central)
	if r.Verdict != "poison" && r.Verdict != "warning" {
		t.Fatalf("centroid-aligned vector not flagged: %+v", r)
	}
}

func TestVecAuditOutlierGetsWarning(t *testing.T) {
	a := NewVectorAudit()
	const dim = 16
	baseline := make([][]float64, 30)
	for i := range baseline {
		baseline[i] = makeNormalVec(int64(i+1), dim)
	}
	a.Baseline("docs", baseline)
	// Far-out vector (opposite direction of centroid)
	idx := a.indexes["docs"]
	idx.mu.RLock()
	far := make([]float64, len(idx.centroid))
	for i, x := range idx.centroid {
		far[i] = -x
	}
	idx.mu.RUnlock()
	r, _ := a.Check("docs", far)
	if r.Verdict == "stable" {
		t.Fatalf("opposite-of-centroid vector flagged stable: %+v", r)
	}
}

func TestVecAuditDimensionMismatch(t *testing.T) {
	a := NewVectorAudit()
	baseline := make([][]float64, 8)
	for i := range baseline {
		baseline[i] = makeNormalVec(int64(i+1), 16)
	}
	a.Baseline("docs", baseline)
	r, _ := a.Check("docs", []float64{1, 2, 3})
	if r.Verdict != "warning" {
		t.Fatalf("dim mismatch should warn: %+v", r)
	}
}

func TestVecAuditQueryAffinityFires(t *testing.T) {
	a := NewVectorAudit()
	const dim = 16
	baseline := make([][]float64, 30)
	for i := range baseline {
		baseline[i] = makeNormalVec(int64(i+1), dim)
	}
	a.Baseline("docs", baseline)
	// Build a "shared direction" — every query is the shared vector plus
	// a small noise term. A poison vector aligned to the shared direction
	// should fire the affinity signal.
	shared := makeNormalVec(42, dim)
	for i := 0; i < 12; i++ {
		noise := makeNormalVec(int64(2000+i), dim)
		q := make([]float64, dim)
		for j := range q {
			q[j] = 0.9*shared[j] + 0.1*noise[j]
		}
		l2NormaliseInPlace(q)
		a.AddQuery("docs", q)
	}
	// Vector exactly along the shared direction
	r, _ := a.Check("docs", append([]float64(nil), shared...))
	if r.TopQueryAffinity < 0.80 {
		t.Fatalf("query affinity too low: %f (verdict=%s)", r.TopQueryAffinity, r.Verdict)
	}
	if r.Verdict == "stable" {
		t.Fatalf("vector aligned to query distribution should fire: %+v", r)
	}
}

func TestVecAuditAddQueryRolls(t *testing.T) {
	a := NewVectorAudit()
	a.SetCap(5)
	for i := 0; i < 10; i++ {
		a.AddQuery("docs", makeNormalVec(int64(i+1), 16))
	}
	st, _ := a.Status("docs")
	if st.QueryBufferSize != 5 {
		t.Fatalf("query buffer = %d", st.QueryBufferSize)
	}
}

func TestVecAuditRejectsBadBaseline(t *testing.T) {
	a := NewVectorAudit()
	if err := a.Baseline("", [][]float64{makeNormalVec(1, 4)}); err == nil {
		t.Fatal("empty index_id should fail")
	}
	if err := a.Baseline("a", [][]float64{}); err == nil {
		t.Fatal("empty baseline should fail")
	}
	if err := a.Baseline("a", [][]float64{makeNormalVec(1, 4)}); err == nil {
		t.Fatal("< 5 vectors should fail")
	}
	// dim mismatch in baseline
	if err := a.Baseline("a", [][]float64{
		makeNormalVec(1, 4), makeNormalVec(2, 4), makeNormalVec(3, 4),
		makeNormalVec(4, 4), makeNormalVec(5, 8),
	}); err == nil {
		t.Fatal("dim mismatch in baseline should fail")
	}
}

func TestVecAuditCheckRejectsBadInput(t *testing.T) {
	a := NewVectorAudit()
	if _, err := a.Check("", []float64{1}); err == nil {
		t.Fatal("empty index should fail")
	}
	if _, err := a.Check("a", []float64{}); err == nil {
		t.Fatal("empty vector should fail")
	}
}

func TestVecAuditListAndReset(t *testing.T) {
	a := NewVectorAudit()
	base := make([][]float64, 6)
	for i := range base {
		base[i] = makeNormalVec(int64(i+1), 8)
	}
	a.Baseline("a", base)
	a.Baseline("b", base)
	l := a.List()
	if len(l) != 2 || l[0] != "a" {
		t.Fatalf("list = %v", l)
	}
	if a.Reset("a") != 1 {
		t.Fatal("reset a should drop 1")
	}
	if a.Reset("ALL") != 1 {
		t.Fatal("ALL reset should drop the remaining 1")
	}
}

func TestVecAuditStatsAdvance(t *testing.T) {
	a := NewVectorAudit()
	base := make([][]float64, 6)
	for i := range base {
		base[i] = makeNormalVec(int64(i+1), 8)
	}
	a.Baseline("a", base)
	a.AddQuery("a", makeNormalVec(99, 8))
	a.Check("a", makeNormalVec(100, 8))
	st := a.Stats()
	if st.Indexes != 1 || st.TotalChecks != 1 || st.TotalQueries != 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func BenchmarkVecAuditCheck(b *testing.B) {
	a := NewVectorAudit()
	const dim = 128
	base := make([][]float64, 50)
	for i := range base {
		base[i] = makeNormalVec(int64(i+1), dim)
	}
	a.Baseline("docs", base)
	for i := 0; i < 50; i++ {
		a.AddQuery("docs", makeNormalVec(int64(2000+i), dim))
	}
	v := makeNormalVec(9999, dim)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Check("docs", v)
	}
}
