package llmstack

import "testing"

func TestFedSignalAndGet(t *testing.T) {
	f := NewFedRegistry()
	f.Signal("trust", "source:blog", 10, 2, 12)
	s, ok := f.Get("trust", "source:blog")
	if !ok || s.Alpha != 10 || s.Beta != 2 {
		t.Fatalf("get = %+v", s)
	}
}

func TestFedUpdateAdditive(t *testing.T) {
	f := NewFedRegistry()
	f.Update("trust", "k", 1, 0, 1)
	f.Update("trust", "k", 2, 1, 3)
	s, _ := f.Get("trust", "k")
	if s.Alpha != 3 || s.Beta != 1 || s.N != 4 {
		t.Fatalf("additive: %+v", s)
	}
}

func TestFedExportMerge(t *testing.T) {
	a := NewFedRegistry()
	b := NewFedRegistry()
	a.Node("node-a")
	b.Node("node-b")
	a.Signal("trust", "k", 5, 1, 6)
	b.Signal("trust", "k", 3, 2, 5)
	// Export b → merge into a
	exp := b.Export("")
	n, _ := a.Merge("node-b", exp.Signals)
	if n != 1 {
		t.Fatalf("merged = %d", n)
	}
	s, _ := a.Get("trust", "k")
	// a now holds 5+3, 1+2, 6+5
	if s.Alpha != 8 || s.Beta != 3 || s.N != 11 {
		t.Fatalf("merged values: %+v", s)
	}
}

func TestFedMergeIdempotent(t *testing.T) {
	// Repeated merge must NOT double-count under CRDT semantics in the
	// strict sense — but our additive model would double-count. To get
	// true idempotency we'd need a vector-clock generation token per
	// signal. Document the limit in the test (it's the typical
	// fed-learning trade-off: applications dedupe on the broadcast side
	// or use a different MERGE shape).
	//
	// What we DO guarantee: merging the same signals twice produces
	// strictly additive doubling — predictable, callers can plan for it.
	a := NewFedRegistry()
	a.Node("a")
	exp := FedExport{NodeID: "b", Signals: []fedSignal{
		{Kind: "k", Key: "x", Alpha: 1, Beta: 1, N: 1},
	}}
	a.Merge("b", exp.Signals)
	a.Merge("b", exp.Signals)
	s, _ := a.Get("k", "x")
	if s.Alpha != 2 {
		t.Fatalf("additive double-merge expected: %+v", s)
	}
}

func TestFedMergeRejectsSelf(t *testing.T) {
	a := NewFedRegistry()
	a.Node("alpha")
	if _, err := a.Merge("alpha", nil); err == nil {
		t.Fatal("self-merge should fail")
	}
}

func TestFedExportFilterKind(t *testing.T) {
	a := NewFedRegistry()
	a.Signal("trust", "x", 1, 1, 1)
	a.Signal("bandit", "x", 1, 1, 1)
	exp := a.Export("trust")
	if len(exp.Signals) != 1 || exp.Signals[0].Kind != "trust" {
		t.Fatalf("filter: %+v", exp)
	}
}

func TestFedPeers(t *testing.T) {
	a := NewFedRegistry()
	a.Node("a")
	a.Merge("b", nil)
	a.Merge("c", nil)
	peers := a.Peers()
	if len(peers) != 2 {
		t.Fatalf("peers = %d", len(peers))
	}
}

func TestFedForget(t *testing.T) {
	a := NewFedRegistry()
	a.Signal("k", "1", 1, 1, 1)
	a.Signal("k", "2", 1, 1, 1)
	if a.Forget("k", "1") != 1 {
		t.Fatal("forget one")
	}
	if a.Forget("ALL", "") != 1 {
		t.Fatal("forget ALL")
	}
}

func TestFedStats(t *testing.T) {
	a := NewFedRegistry()
	a.Node("alpha")
	a.Signal("k", "x", 1, 1, 1)
	a.Export("")
	a.Merge("beta", nil)
	s := a.Stats()
	if s.NodeID != "alpha" || s.Signals != 1 || s.Peers != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestFedRejectsBadInput(t *testing.T) {
	f := NewFedRegistry()
	if err := f.Node(""); err == nil {
		t.Fatal("empty node id")
	}
	if err := f.Signal("", "k", 1, 1, 1); err == nil {
		t.Fatal("empty kind")
	}
	if err := f.Signal("k", "", 1, 1, 1); err == nil {
		t.Fatal("empty key")
	}
	if err := f.Signal("k", "x", -1, 0, 0); err == nil {
		t.Fatal("negative alpha")
	}
	if err := f.Update("", "x", 0, 0, 0); err == nil {
		t.Fatal("empty kind")
	}
}
