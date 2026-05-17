package llmstack

import "testing"

func TestDRSnapshotContributeSeal(t *testing.T) {
	d := NewDRRegistry()
	if err := d.Snapshot("b1", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.Contribute("b1", "trust", `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	d.Seal("b1")
	if err := d.Contribute("b1", "x", "y"); err == nil {
		t.Fatal("post-seal contribute should fail")
	}
}

func TestDRRestoreAndAssertMatches(t *testing.T) {
	d := NewDRRegistry()
	d.Snapshot("src", nil)
	d.Contribute("src", "trust", `{"a":1}`)
	d.Contribute("src", "market", `{"x":2}`)
	d.Seal("src")
	if err := d.RestoreInto("src", "shadow"); err != nil {
		t.Fatal(err)
	}
	r, err := d.Assert("src", "shadow")
	if err != nil {
		t.Fatal(err)
	}
	if !r.AllMatch || len(r.Diverged) != 0 {
		t.Fatalf("assert: %+v", r)
	}
}

func TestDRRestoreSourceMustBeSealed(t *testing.T) {
	d := NewDRRegistry()
	d.Snapshot("src", nil)
	if err := d.RestoreInto("src", "shadow"); err == nil {
		t.Fatal("unsealed restore should fail")
	}
}

func TestDRAssertDetectsTampering(t *testing.T) {
	d := NewDRRegistry()
	d.Snapshot("src", nil)
	d.Contribute("src", "trust", `{"a":1}`)
	d.Seal("src")
	// Build a shadow with a different payload (simulate corrupt restore)
	d.Snapshot("shadow", nil)
	d.Contribute("shadow", "trust", `{"a":99}`) // tampered
	d.Seal("shadow")
	r, _ := d.Assert("src", "shadow")
	if r.AllMatch || len(r.Diverged) != 1 || r.Diverged[0] != "trust" {
		t.Fatalf("expected divergence: %+v", r)
	}
}

func TestDRAssertDetectsMissingAndExtra(t *testing.T) {
	d := NewDRRegistry()
	d.Snapshot("src", nil)
	d.Contribute("src", "a", "1")
	d.Contribute("src", "b", "2")
	d.Seal("src")
	d.Snapshot("shadow", nil)
	d.Contribute("shadow", "a", "1") // matches
	d.Contribute("shadow", "c", "3") // extra
	d.Seal("shadow")
	r, _ := d.Assert("src", "shadow")
	if len(r.MissingInShadow) != 1 || r.MissingInShadow[0] != "b" {
		t.Fatalf("missing: %+v", r.MissingInShadow)
	}
	if len(r.ExtraInShadow) != 1 || r.ExtraInShadow[0] != "c" {
		t.Fatalf("extra: %+v", r.ExtraInShadow)
	}
}

func TestDRPromote(t *testing.T) {
	d := NewDRRegistry()
	d.Snapshot("b", nil)
	d.Seal("b")
	if err := d.Promote("b"); err != nil {
		t.Fatal(err)
	}
	v, _ := d.Get("b")
	if !v.Promoted {
		t.Fatal("promoted should be true")
	}
}

func TestDRPayloadReturnsBlob(t *testing.T) {
	d := NewDRRegistry()
	d.Snapshot("b", nil)
	d.Contribute("b", "trust", "the-blob")
	p, ok := d.Payload("b", "trust")
	if !ok || p != "the-blob" {
		t.Fatalf("payload: %s", p)
	}
}

func TestDRListGetForget(t *testing.T) {
	d := NewDRRegistry()
	d.Snapshot("a", nil)
	d.Snapshot("b", nil)
	if len(d.List(10)) != 2 {
		t.Fatal("list")
	}
	if _, ok := d.Get("a"); !ok {
		t.Fatal("get")
	}
	if d.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if d.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestDRDuplicateSnapshot(t *testing.T) {
	d := NewDRRegistry()
	d.Snapshot("a", nil)
	if err := d.Snapshot("a", nil); err == nil {
		t.Fatal("dup should fail")
	}
}

func TestDRDuplicateShadow(t *testing.T) {
	d := NewDRRegistry()
	d.Snapshot("a", nil)
	d.Seal("a")
	d.RestoreInto("a", "shadow")
	if err := d.RestoreInto("a", "shadow"); err == nil {
		t.Fatal("dup shadow should fail")
	}
}

func TestDRStats(t *testing.T) {
	d := NewDRRegistry()
	d.Snapshot("a", nil)
	d.Seal("a")
	d.RestoreInto("a", "b")
	d.Assert("a", "b")
	d.Promote("b")
	s := d.Stats()
	if s.TotalSnapshots != 1 || s.TotalRestores != 1 || s.TotalAsserts != 1 || s.TotalPromotes != 1 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestDRRejectsBadInput(t *testing.T) {
	d := NewDRRegistry()
	if err := d.Snapshot("", nil); err == nil {
		t.Fatal("empty id")
	}
	if err := d.Contribute("", "x", "y"); err == nil {
		t.Fatal("empty bundle")
	}
	if err := d.Contribute("nope", "x", "y"); err == nil {
		t.Fatal("unknown bundle")
	}
	if err := d.RestoreInto("ghost", "shadow"); err == nil {
		t.Fatal("unknown source")
	}
}
