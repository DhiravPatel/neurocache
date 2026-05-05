package clist

import "testing"

func collect(l *List) []string {
	out := []string{}
	for e := l.Front(); e != nil; e = e.Next() {
		out = append(out, e.Value)
	}
	return out
}

func TestPushFrontPushBack(t *testing.T) {
	l := New()
	l.PushBack("a")
	l.PushBack("b")
	l.PushFront("z")
	got := collect(l)
	want := []string{"z", "a", "b"}
	if len(got) != len(want) {
		t.Fatalf("len got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d got=%v want=%v", i, got[i], want[i])
		}
	}
	if l.Len() != 3 {
		t.Fatalf("Len=%d want=3", l.Len())
	}
}

func TestRemoveAndPoolReuse(t *testing.T) {
	l := New()
	a := l.PushBack("a")
	b := l.PushBack("b")
	if l.Remove(a) != "a" {
		t.Fatal("removed value mismatch")
	}
	if l.Len() != 1 {
		t.Fatalf("Len after Remove=%d want=1", l.Len())
	}
	if l.Front() != b {
		t.Fatal("Front after Remove(a) should be b")
	}
	// Pool reuse: pushing again may return the same *Element struct.
	// Either way the Value should be fresh (no stale "a" leak).
	c := l.PushBack("c")
	if c.Value != "c" {
		t.Fatalf("pushed value=%v want=c", c.Value)
	}
	if c.Next() != nil {
		t.Fatal("c is last; Next should be nil")
	}
	if c.Prev() != b {
		t.Fatal("c.Prev should be b")
	}
}

func TestInsertBeforeAfter(t *testing.T) {
	l := New()
	a := l.PushBack("a")
	c := l.PushBack("c")
	l.InsertBefore("b", c)
	l.InsertAfter("d", c)
	_ = a
	got := collect(l)
	want := []string{"a", "b", "c", "d"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("at %d got=%v want=%v", i, got[i], want[i])
		}
	}
}

func TestInitClearsLen(t *testing.T) {
	l := New()
	l.PushBack("a")
	l.PushBack("b")
	l.Init()
	if l.Len() != 0 {
		t.Fatalf("Len after Init=%d", l.Len())
	}
	if l.Front() != nil || l.Back() != nil {
		t.Fatal("Front/Back not nil after Init")
	}
}

func TestRemoveForeignElementIsNoop(t *testing.T) {
	l1 := New()
	l2 := New()
	a := l1.PushBack("a")
	// Removing l1's element from l2 must not corrupt either list.
	l2.Remove(a)
	if l1.Len() != 1 {
		t.Fatalf("l1.Len=%d want=1", l1.Len())
	}
	if l2.Len() != 0 {
		t.Fatalf("l2.Len=%d want=0", l2.Len())
	}
}

func TestNextPrevTraversal(t *testing.T) {
	l := New()
	for _, v := range []string{"a", "b", "c"} {
		l.PushBack(v)
	}
	// Forward
	got := []string{}
	for e := l.Front(); e != nil; e = e.Next() {
		got = append(got, e.Value)
	}
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("forward traversal=%v", got)
	}
	// Backward
	got = got[:0]
	for e := l.Back(); e != nil; e = e.Prev() {
		got = append(got, e.Value)
	}
	if len(got) != 3 || got[0] != "c" || got[2] != "a" {
		t.Fatalf("backward traversal=%v", got)
	}
}

func BenchmarkPushBack(b *testing.B) {
	l := New()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.PushBack("x")
		if l.Len() > 1024 {
			// Drain so we measure steady-state pool reuse, not unbounded growth.
			for l.Len() > 0 {
				l.Remove(l.Front())
			}
		}
	}
}

func BenchmarkPushFrontPopBack(b *testing.B) {
	l := New()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.PushFront("x")
		if l.Len() > 32 {
			l.Remove(l.Back())
		}
	}
}

// BenchmarkPushBackGrowing matches the redis-benchmark RPUSH shape:
// monotonic growth against one list, no removes. The arena path
// should report ~1/32 alloc per op (one [32]Element batch per 32
// pushes). Compare against a hypothetical container/list run which
// would show ~40 B/op and 1 alloc/op.
func BenchmarkPushBackGrowing(b *testing.B) {
	l := New()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		l.PushBack("x")
	}
}
