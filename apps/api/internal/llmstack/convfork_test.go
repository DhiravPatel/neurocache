package llmstack

import (
	"testing"
)

func TestConvForkSeedAndAppend(t *testing.T) {
	m := NewConvForkManager()
	if err := m.Seed("root"); err != nil {
		t.Fatal(err)
	}
	if err := m.Append("root", "user", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := m.Append("root", "assistant", "hi there"); err != nil {
		t.Fatal(err)
	}
	turns, ok := m.Get("root")
	if !ok || len(turns) != 2 {
		t.Fatalf("turns = %d", len(turns))
	}
	if turns[1].Role != "assistant" || turns[1].Content != "hi there" {
		t.Fatalf("turn[1] = %+v", turns[1])
	}
}

func TestConvForkCreateCopiesTurns(t *testing.T) {
	m := NewConvForkManager()
	m.Seed("root")
	m.Append("root", "user", "what's the plan")
	m.Append("root", "assistant", "do X")
	m.Append("root", "user", "ok do X")
	// Fork at index 2 (skip the last "ok do X")
	if err := m.Create("root", "what-if-Y", 2); err != nil {
		t.Fatal(err)
	}
	turns, _ := m.Get("what-if-Y")
	if len(turns) != 2 {
		t.Fatalf("forked turns = %d, want 2", len(turns))
	}
	if turns[1].Content != "do X" {
		t.Fatalf("forked turn[1] = %s", turns[1].Content)
	}
}

func TestConvForkCreateDefaultsToAllTurns(t *testing.T) {
	m := NewConvForkManager()
	m.Seed("root")
	m.Append("root", "user", "a")
	m.Append("root", "user", "b")
	m.Create("root", "child", -1)
	turns, _ := m.Get("child")
	if len(turns) != 2 {
		t.Fatalf("child got %d turns, want 2", len(turns))
	}
}

func TestConvForkAppendIsIndependent(t *testing.T) {
	m := NewConvForkManager()
	m.Seed("root")
	m.Append("root", "user", "shared")
	m.Create("root", "branch", -1)
	m.Append("branch", "user", "branch-only")
	rootTurns, _ := m.Get("root")
	branchTurns, _ := m.Get("branch")
	if len(rootTurns) != 1 {
		t.Fatalf("root should still have 1 turn, has %d", len(rootTurns))
	}
	if len(branchTurns) != 2 {
		t.Fatalf("branch should have 2, has %d", len(branchTurns))
	}
}

func TestConvForkListChildren(t *testing.T) {
	m := NewConvForkManager()
	m.Seed("root")
	m.Create("root", "b", -1)
	m.Create("root", "a", -1)
	m.Create("root", "c", -1)
	kids, ok := m.List("root")
	if !ok || len(kids) != 3 {
		t.Fatalf("kids = %v", kids)
	}
	// Sorted
	if kids[0] != "a" || kids[1] != "b" || kids[2] != "c" {
		t.Fatalf("not sorted: %v", kids)
	}
}

func TestConvForkTreeDepthFirst(t *testing.T) {
	m := NewConvForkManager()
	m.Seed("root")
	m.Create("root", "child1", -1)
	m.Create("child1", "grandchild", -1)
	m.Create("root", "child2", -1)
	nodes, ok := m.Tree("root")
	if !ok || len(nodes) != 4 {
		t.Fatalf("tree nodes = %d", len(nodes))
	}
	// DFS: root → child1 → grandchild → child2
	if nodes[0].ID != "root" {
		t.Fatalf("nodes[0] = %s", nodes[0].ID)
	}
	if nodes[2].ID != "grandchild" {
		t.Fatalf("nodes[2] = %s", nodes[2].ID)
	}
	if nodes[3].ID != "child2" {
		t.Fatalf("nodes[3] = %s", nodes[3].ID)
	}
}

func TestConvForkDeleteCascades(t *testing.T) {
	m := NewConvForkManager()
	m.Seed("root")
	m.Create("root", "branch", -1)
	m.Create("branch", "deep", -1)
	dropped := m.Delete("branch")
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2 (branch + deep)", dropped)
	}
	// root should still exist
	if _, ok := m.Get("root"); !ok {
		t.Fatal("root was wiped")
	}
	// branch detached from root
	kids, _ := m.List("root")
	if len(kids) != 0 {
		t.Fatalf("root still has children: %v", kids)
	}
}

func TestConvForkRejectsDuplicateID(t *testing.T) {
	m := NewConvForkManager()
	m.Seed("a")
	if err := m.Seed("a"); err == nil {
		t.Fatal("duplicate seed should fail")
	}
	if err := m.Create("a", "a", -1); err == nil {
		t.Fatal("fork_id == parent_id should fail")
	}
	m.Create("a", "b", -1)
	if err := m.Create("a", "b", -1); err == nil {
		t.Fatal("duplicate fork_id should fail")
	}
}

func TestConvForkRejectsUnknownParent(t *testing.T) {
	m := NewConvForkManager()
	if err := m.Create("ghost", "kid", -1); err == nil {
		t.Fatal("fork from missing parent should fail")
	}
}

func TestConvForkAppendRejectsBadInput(t *testing.T) {
	m := NewConvForkManager()
	m.Seed("a")
	if err := m.Append("a", "", "content"); err == nil {
		t.Fatal("empty role should fail")
	}
	if err := m.Append("ghost", "user", "x"); err == nil {
		t.Fatal("unknown conv should fail")
	}
}

func TestConvForkStats(t *testing.T) {
	m := NewConvForkManager()
	m.Seed("r")
	m.Create("r", "c", -1)
	m.Append("r", "user", "x")
	s := m.Stats()
	if s.Branches != 2 || s.Roots != 1 {
		t.Fatalf("stats = %+v", s)
	}
	if s.TotalSeeds != 1 || s.TotalForks != 1 || s.TotalAppends != 1 {
		t.Fatalf("counters = %+v", s)
	}
}

func BenchmarkConvForkAppend(b *testing.B) {
	m := NewConvForkManager()
	m.Seed("root")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Append("root", "user", "msg")
	}
}

func BenchmarkConvForkCreate(b *testing.B) {
	m := NewConvForkManager()
	m.Seed("root")
	for i := 0; i < 20; i++ {
		m.Append("root", "user", "turn")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Create("root", "f"+itoa(i), -1)
	}
}

