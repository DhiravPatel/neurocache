package llmstack

import (
	"testing"
)

func TestMemConflictAddAssignsID(t *testing.T) {
	m := NewMemoryConflicts()
	id, err := m.Add("user:dhirav", "prefers async communication", "")
	if err != nil || id == "" {
		t.Fatalf("add returned id=%q err=%v", id, err)
	}
}

func TestMemConflictCheckEmptyKeyNoConflict(t *testing.T) {
	m := NewMemoryConflicts()
	r, _ := m.Check("missing", "anything", false)
	if r.Conflict {
		t.Fatalf("empty key shouldn't conflict: %+v", r)
	}
}

func TestMemConflictDetectsPolarityFlip(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("user:d", "prefers async communication for everything important", "")
	r, _ := m.Check("user:d", "wants synchronous daily standup meetings", false)
	if !r.Conflict {
		t.Fatalf("async vs sync should conflict: %+v", r)
	}
	if r.ResolutionHint == "" {
		t.Fatalf("hint empty: %+v", r)
	}
}

func TestMemConflictDetectsNegationDifferential(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("user:d", "user approves the migration plan completely", "")
	r, _ := m.Check("user:d", "user does not approve the migration plan", false)
	if !r.Conflict {
		t.Fatalf("approve vs not-approve should conflict: %+v", r)
	}
}

func TestMemConflictDietContradiction(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("user:d", "user is vegetarian", "")
	r, _ := m.Check("user:d", "user ordered steak meat dinner", false)
	if !r.Conflict {
		t.Fatalf("vegetarian vs steak should conflict: %+v", r)
	}
}

func TestMemConflictUnrelatedNoConflict(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("user:d", "lives in San Francisco", "")
	r, _ := m.Check("user:d", "loves chocolate cake recipes", false)
	if r.Conflict {
		t.Fatalf("unrelated text flagged: %+v", r)
	}
}

func TestMemConflictDuplicateIsNotAConflict(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("user:d", "prefers async communication daily", "")
	r, _ := m.Check("user:d", "prefers async communication daily", false)
	if r.Conflict {
		t.Fatalf("identical fact should not contradict: %+v", r)
	}
}

func TestMemConflictStrictModeRaisesBar(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("user:d", "prefers morning slot for meetings", "")
	// Without polarity flip OR negation, strict mode should reject
	r, _ := m.Check("user:d", "prefers afternoon slot for meetings", true)
	if r.Conflict {
		t.Fatalf("strict mode should require stronger signal: %+v", r)
	}
}

func TestMemConflictAddConflictAndList(t *testing.T) {
	m := NewMemoryConflicts()
	older, _ := m.Add("user:d", "prefers async", "")
	newer, _ := m.Add("user:d", "wants sync standup", "")
	id, err := m.AddConflict("user:d", newer, older, 0.81, "supersede")
	if err != nil || id == "" {
		t.Fatalf("addconflict: id=%q err=%v", id, err)
	}
	rows, ok := m.List("user:d")
	if !ok || len(rows) != 1 {
		t.Fatalf("list rows = %d", len(rows))
	}
	if rows[0].NewerID != newer || rows[0].OlderID != older {
		t.Fatalf("rows = %+v", rows[0])
	}
}

func TestMemConflictResolveDropsOlder(t *testing.T) {
	m := NewMemoryConflicts()
	older, _ := m.Add("user:d", "prefers async", "")
	newer, _ := m.Add("user:d", "wants sync standup", "")
	cid, _ := m.AddConflict("user:d", newer, older, 0.8, "supersede")
	if err := m.Resolve("user:d", cid, "newer"); err != nil {
		t.Fatal(err)
	}
	// older fact should be gone; newer survives
	rows, _ := m.List("user:d")
	if len(rows) != 0 {
		t.Fatalf("conflict not removed: %+v", rows)
	}
}

func TestMemConflictResolveBothDropsNothing(t *testing.T) {
	m := NewMemoryConflicts()
	older, _ := m.Add("user:d", "prefers async", "")
	newer, _ := m.Add("user:d", "wants sync standup", "")
	cid, _ := m.AddConflict("user:d", newer, older, 0.8, "review")
	if err := m.Resolve("user:d", cid, "both"); err != nil {
		t.Fatal(err)
	}
	st := m.Stats()
	if st.TotalFacts != 2 {
		t.Fatalf("'both' should keep facts: total=%d", st.TotalFacts)
	}
}

func TestMemConflictResolveRejectsBadKeep(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("user:d", "x", "")
	if err := m.Resolve("user:d", "c1", "wrong"); err == nil {
		t.Fatal("bad keep should fail")
	}
}

func TestMemConflictResolveUnknownConflict(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("user:d", "x", "")
	if err := m.Resolve("user:d", "ghost", "newer"); err == nil {
		t.Fatal("unknown conflict id should fail")
	}
}

func TestMemConflictPurge(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("a", "x", "")
	m.Add("b", "x", "")
	if !m.Purge("a") {
		t.Fatal("purge should report success")
	}
	if _, ok := m.List("a"); ok {
		t.Fatal("key still present after purge")
	}
}

func TestMemConflictKeysSorted(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("zeta", "x", "")
	m.Add("alpha", "x", "")
	m.Add("mid", "x", "")
	ks := m.Keys()
	if ks[0] != "alpha" || ks[2] != "zeta" {
		t.Fatalf("keys = %v", ks)
	}
}

func TestMemConflictRejectsBadInput(t *testing.T) {
	m := NewMemoryConflicts()
	if _, err := m.Add("", "x", ""); err == nil {
		t.Fatal("empty key should fail")
	}
	if _, err := m.Add("k", "", ""); err == nil {
		t.Fatal("empty text should fail")
	}
	if _, err := m.Check("", "x", false); err == nil {
		t.Fatal("empty key for check should fail")
	}
	if _, err := m.AddConflict("ghost", "n", "o", 0.5, "h"); err == nil {
		t.Fatal("unknown key for addconflict should fail")
	}
}

func TestMemConflictStatsAdvance(t *testing.T) {
	m := NewMemoryConflicts()
	m.Add("u", "prefers async", "")
	m.Check("u", "wants sync", false)
	st := m.Stats()
	if st.Keys != 1 || st.TotalFacts != 1 {
		t.Fatalf("stats = %+v", st)
	}
	if st.TotalAdds != 1 || st.TotalChecks != 1 || st.TotalConflicts < 1 {
		t.Fatalf("counters = %+v", st)
	}
}

func BenchmarkMemConflictCheck(b *testing.B) {
	m := NewMemoryConflicts()
	for i := 0; i < 20; i++ {
		m.Add("user", "fact number "+itoaBench(i), "")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Check("user", "candidate fact under inspection", false)
	}
}

func BenchmarkMemConflictAdd(b *testing.B) {
	m := NewMemoryConflicts()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Add("user", "some preference fact", "")
	}
}
