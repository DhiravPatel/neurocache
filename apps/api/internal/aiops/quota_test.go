package aiops

import "testing"

func TestQuotaDefineValidation(t *testing.T) {
	q := NewQuotaManager()

	if _, err := q.Define("", []string{"cost"}, "all"); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := q.Define("p", nil, "all"); err == nil {
		t.Fatal("empty gate list accepted")
	}
	if _, err := q.Define("p", []string{"bogus"}, "all"); err == nil {
		t.Fatal("unknown gate accepted")
	}
	if _, err := q.Define("p", []string{"cost"}, "weird"); err == nil {
		t.Fatal("invalid mode accepted")
	}

	// Valid: mode defaults to all, duplicate gates collapse.
	pol, err := q.Define("p", []string{"cost", "cost", "risk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if pol.Mode != QuotaModeAll {
		t.Fatalf("default mode = %q, want all", pol.Mode)
	}
	if len(pol.Gates) != 2 {
		t.Fatalf("gates = %v, want deduped to 2", pol.Gates)
	}
}

func TestQuotaRegistryLifecycle(t *testing.T) {
	q := NewQuotaManager()
	if _, err := q.Define("a", []string{"cost"}, "all"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Define("b", []string{"rate"}, "any"); err != nil {
		t.Fatal(err)
	}

	if got := q.List(); len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("List = %+v, want [a,b] sorted", got)
	}
	if _, ok := q.Get("a"); !ok {
		t.Fatal("Get(a) missing")
	}
	if !q.Delete("a") {
		t.Fatal("Delete(a) returned false")
	}
	if q.Delete("a") {
		t.Fatal("second Delete(a) returned true")
	}
	if _, ok := q.Get("a"); ok {
		t.Fatal("a still present after delete")
	}
}

func TestQuotaStatsCounters(t *testing.T) {
	q := NewQuotaManager()
	_, _ = q.Define("p", []string{"cost"}, "all")

	q.RecordDecision(QuotaDecision{Admitted: true}, false)
	q.RecordDecision(QuotaDecision{Admitted: false}, false)
	q.RecordDecision(QuotaDecision{}, true) // simulate

	s := q.Stats()
	if s.Policies != 1 || s.Admits != 1 || s.Denials != 1 || s.Simulations != 1 {
		t.Fatalf("stats = %+v, want policies=1 admits=1 denials=1 simulations=1", s)
	}
}
