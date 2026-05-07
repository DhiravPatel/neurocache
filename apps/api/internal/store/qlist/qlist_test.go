package qlist

import (
	"strconv"
	"testing"
)

func collect(q *QList) []string {
	var out []string
	q.ForEach(func(v string) bool { out = append(out, v); return true })
	return out
}

func collectReverse(q *QList) []string {
	var out []string
	q.ForEachReverse(func(v string) bool { out = append(out, v); return true })
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len got=%v (n=%d) want=%v (n=%d)", got, len(got), want, len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("at %d got=%q want=%q (full got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
}

func TestPushPopBothEnds(t *testing.T) {
	q := New()
	q.PushBack("a")
	q.PushBack("b")
	q.PushFront("z")
	eq(t, collect(q), []string{"z", "a", "b"})
	if q.Len() != 3 {
		t.Fatalf("Len=%d want=3", q.Len())
	}
	v, ok := q.PopFront()
	if !ok || v != "z" {
		t.Fatalf("PopFront got=%q ok=%v", v, ok)
	}
	v, ok = q.PopBack()
	if !ok || v != "b" {
		t.Fatalf("PopBack got=%q ok=%v", v, ok)
	}
	eq(t, collect(q), []string{"a"})
}

func TestEmptyPops(t *testing.T) {
	q := New()
	if _, ok := q.PopFront(); ok {
		t.Fatal("PopFront on empty should be false")
	}
	if _, ok := q.PopBack(); ok {
		t.Fatal("PopBack on empty should be false")
	}
	if _, ok := q.Front(); ok {
		t.Fatal("Front on empty should be false")
	}
	if _, ok := q.Back(); ok {
		t.Fatal("Back on empty should be false")
	}
}

func TestCrossNodeBoundary(t *testing.T) {
	// Push enough to cross the 128-element nodeCap boundary in both
	// directions. Verifies the multi-node linking is correct.
	q := New()
	const n = 500
	for i := 0; i < n; i++ {
		q.PushBack(strconv.Itoa(i))
	}
	if q.Len() != n {
		t.Fatalf("Len=%d want=%d", q.Len(), n)
	}
	got := collect(q)
	for i, v := range got {
		if v != strconv.Itoa(i) {
			t.Fatalf("at %d got=%q want=%q", i, v, strconv.Itoa(i))
		}
	}
	// Pop them all from the front in order.
	for i := 0; i < n; i++ {
		v, ok := q.PopFront()
		if !ok || v != strconv.Itoa(i) {
			t.Fatalf("PopFront %d got=%q ok=%v", i, v, ok)
		}
	}
	if q.Len() != 0 {
		t.Fatalf("Len after drain=%d want=0", q.Len())
	}
}

func TestPushFrontCrossNode(t *testing.T) {
	q := New()
	const n = 500
	for i := 0; i < n; i++ {
		q.PushFront(strconv.Itoa(i))
	}
	// PushFront creates list [n-1, n-2, ..., 0]
	if q.Len() != n {
		t.Fatalf("Len=%d want=%d", q.Len(), n)
	}
	for i := 0; i < n; i++ {
		v, ok := q.PopFront()
		expect := strconv.Itoa(n - 1 - i)
		if !ok || v != expect {
			t.Fatalf("PopFront %d got=%q want=%q", i, v, expect)
		}
	}
}

func TestIndexAndSet(t *testing.T) {
	q := New()
	for i := 0; i < 300; i++ {
		q.PushBack(strconv.Itoa(i))
	}
	v, ok := q.Index(0)
	if !ok || v != "0" {
		t.Fatalf("Index 0 got=%q ok=%v", v, ok)
	}
	v, ok = q.Index(150)
	if !ok || v != "150" {
		t.Fatalf("Index 150 got=%q ok=%v", v, ok)
	}
	v, ok = q.Index(299)
	if !ok || v != "299" {
		t.Fatalf("Index 299 got=%q ok=%v", v, ok)
	}
	if _, ok := q.Index(300); ok {
		t.Fatal("Index 300 should be out-of-range")
	}
	old, ok := q.Set(150, "X")
	if !ok || old != "150" {
		t.Fatalf("Set 150 old=%q ok=%v", old, ok)
	}
	v, _ = q.Index(150)
	if v != "X" {
		t.Fatalf("after Set, Index 150 got=%q want=X", v)
	}
}

func TestInsert(t *testing.T) {
	q := New()
	q.PushBack("a")
	q.PushBack("c")
	if !q.Insert(1, "b") {
		t.Fatal("Insert(1,b) failed")
	}
	eq(t, collect(q), []string{"a", "b", "c"})
	if !q.Insert(0, "Z") {
		t.Fatal("Insert(0) failed")
	}
	eq(t, collect(q), []string{"Z", "a", "b", "c"})
	if !q.Insert(4, "tail") {
		t.Fatal("Insert at end failed")
	}
	eq(t, collect(q), []string{"Z", "a", "b", "c", "tail"})
}

func TestInsertSplitsFullNode(t *testing.T) {
	// Fill exactly one node and then insert in the middle to exercise
	// splitAfter().
	q := New()
	for i := 0; i < nodeCap; i++ {
		q.PushBack(strconv.Itoa(i))
	}
	if q.Len() != nodeCap {
		t.Fatalf("Len=%d want=%d", q.Len(), nodeCap)
	}
	if !q.Insert(60, "X") {
		t.Fatal("Insert into full node failed")
	}
	if q.Len() != nodeCap+1 {
		t.Fatalf("Len after Insert=%d want=%d", q.Len(), nodeCap+1)
	}
	v, _ := q.Index(60)
	if v != "X" {
		t.Fatalf("at 60 got=%q want=X", v)
	}
	v, _ = q.Index(61)
	if v != "60" {
		t.Fatalf("at 61 got=%q want=60", v)
	}
	v, _ = q.Index(0)
	if v != "0" {
		t.Fatalf("at 0 got=%q want=0", v)
	}
	v, _ = q.Index(nodeCap)
	if v != strconv.Itoa(nodeCap-1) {
		t.Fatalf("at last got=%q", v)
	}
}

func TestRemoveAt(t *testing.T) {
	q := New()
	for _, v := range []string{"a", "b", "c", "d", "e"} {
		q.PushBack(v)
	}
	v, ok := q.RemoveAt(2)
	if !ok || v != "c" {
		t.Fatalf("RemoveAt(2) got=%q ok=%v", v, ok)
	}
	eq(t, collect(q), []string{"a", "b", "d", "e"})
}

func TestRemoveByValueForward(t *testing.T) {
	q := New()
	for _, v := range []string{"a", "x", "b", "x", "c", "x"} {
		q.PushBack(v)
	}
	n := q.RemoveByValue("x", 2) // remove 2 from head
	if n != 2 {
		t.Fatalf("removed=%d want=2", n)
	}
	eq(t, collect(q), []string{"a", "b", "c", "x"})
}

func TestRemoveByValueBackward(t *testing.T) {
	q := New()
	for _, v := range []string{"a", "x", "b", "x", "c", "x"} {
		q.PushBack(v)
	}
	n := q.RemoveByValue("x", -2) // remove 2 from tail
	if n != 2 {
		t.Fatalf("removed=%d want=2", n)
	}
	eq(t, collect(q), []string{"a", "x", "b", "c"})
}

func TestRemoveByValueAll(t *testing.T) {
	q := New()
	for _, v := range []string{"a", "x", "b", "x", "c"} {
		q.PushBack(v)
	}
	n := q.RemoveByValue("x", 0)
	if n != 2 {
		t.Fatalf("removed=%d want=2", n)
	}
	eq(t, collect(q), []string{"a", "b", "c"})
}

func TestFindAndInsert(t *testing.T) {
	q := New()
	for _, v := range []string{"a", "b", "c"} {
		q.PushBack(v)
	}
	if got := q.FindAndInsert("b", true, "B-before"); got != 4 {
		t.Fatalf("FindAndInsert before got=%d want=4", got)
	}
	eq(t, collect(q), []string{"a", "B-before", "b", "c"})
	if got := q.FindAndInsert("b", false, "B-after"); got != 5 {
		t.Fatalf("FindAndInsert after got=%d want=5", got)
	}
	eq(t, collect(q), []string{"a", "B-before", "b", "B-after", "c"})
	if got := q.FindAndInsert("not-here", true, "x"); got != -1 {
		t.Fatalf("FindAndInsert missing got=%d want=-1", got)
	}
}

func TestTrim(t *testing.T) {
	q := New()
	for i := 0; i < 10; i++ {
		q.PushBack(strconv.Itoa(i))
	}
	q.Trim(2, 6)
	eq(t, collect(q), []string{"2", "3", "4", "5", "6"})

	q2 := New()
	for i := 0; i < 5; i++ {
		q2.PushBack(strconv.Itoa(i))
	}
	q2.Trim(10, 20) // empty range
	if q2.Len() != 0 {
		t.Fatalf("Trim out-of-range Len=%d want=0", q2.Len())
	}
}

func TestRange(t *testing.T) {
	q := New()
	for i := 0; i < 10; i++ {
		q.PushBack(strconv.Itoa(i))
	}
	eq(t, q.Range(0, 0), []string{"0"})
	eq(t, q.Range(0, 9), []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"})
	eq(t, q.Range(2, 5), []string{"2", "3", "4", "5"})
	eq(t, q.Range(8, 100), []string{"8", "9"})
}

func TestForEachReverse(t *testing.T) {
	q := New()
	for _, v := range []string{"a", "b", "c"} {
		q.PushBack(v)
	}
	eq(t, collectReverse(q), []string{"c", "b", "a"})
}

func TestPopReleasesEmptyNode(t *testing.T) {
	// After draining a multi-node list, the head/tail pointers must
	// be nil and Len must be 0.
	q := New()
	for i := 0; i < 300; i++ {
		q.PushBack(strconv.Itoa(i))
	}
	for i := 0; i < 300; i++ {
		q.PopFront()
	}
	if q.Len() != 0 {
		t.Fatalf("Len after drain=%d", q.Len())
	}
	if q.head != nil || q.tail != nil {
		t.Fatal("head/tail should be nil after full drain")
	}
	// Re-use should still work.
	q.PushBack("z")
	v, ok := q.PopFront()
	if !ok || v != "z" {
		t.Fatalf("re-use after drain failed got=%q", v)
	}
}

func TestInterleavedPushFrontPushBack(t *testing.T) {
	q := New()
	q.PushBack("c")
	q.PushFront("b")
	q.PushBack("d")
	q.PushFront("a")
	eq(t, collect(q), []string{"a", "b", "c", "d"})
}

// ─── benchmarks ────────────────────────────────────────────────

func BenchmarkPushBackGrowing(b *testing.B) {
	q := New()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.PushBack("x")
	}
}

func BenchmarkPushFrontGrowing(b *testing.B) {
	q := New()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.PushFront("x")
	}
}

func BenchmarkPushPopFIFO(b *testing.B) {
	q := New()
	for i := 0; i < 64; i++ {
		q.PushBack("x")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.PushBack("x")
		q.PopFront()
	}
}
