package aiops

import (
	"testing"
	"time"
)

func TestCircuitClosedToOpenToHalfOpenToClosed(t *testing.T) {
	c := NewCircuits()
	c.Configure("svc", CircuitConfig{
		Threshold:   0.5,
		WindowSize:  4,
		MinSamples:  4,
		Cooldown:    20 * time.Millisecond,
		HalfOpenMax: 2,
	})

	// 4 records — 3 fail. Ratio 0.75 ≥ 0.5 → trip.
	c.Record("svc", true)
	c.Record("svc", false)
	c.Record("svc", false)
	c.Record("svc", false)

	snap, ok := c.State("svc")
	if !ok || snap.State != CircuitOpen {
		t.Fatalf("expected OPEN after 3/4 failures, got %v", snap.State)
	}
	allowed, _ := c.Check("svc")
	if allowed {
		t.Fatalf("CHECK should deny while OPEN")
	}

	time.Sleep(25 * time.Millisecond)

	// First CHECK after cooldown promotes to HALFOPEN and reserves a probe.
	allowed, state := c.Check("svc")
	if !allowed || state != CircuitHalfOpen {
		t.Fatalf("expected HALFOPEN probe allowed, got allowed=%v state=%v", allowed, state)
	}
	c.Record("svc", true)

	// Second probe → success → HalfOpenMax=2 reached → CLOSED.
	allowed, _ = c.Check("svc")
	if !allowed {
		t.Fatalf("second probe should be allowed")
	}
	c.Record("svc", true)

	snap, _ = c.State("svc")
	if snap.State != CircuitClosed {
		t.Fatalf("expected CLOSED after two successful probes, got %v", snap.State)
	}
}

func TestCircuitHalfOpenProbeFailureReopens(t *testing.T) {
	c := NewCircuits()
	c.Configure("svc", CircuitConfig{
		Threshold: 0.5, WindowSize: 4, MinSamples: 4,
		Cooldown: 10 * time.Millisecond, HalfOpenMax: 2,
	})
	c.Record("svc", false)
	c.Record("svc", false)
	c.Record("svc", false)
	c.Record("svc", true)
	time.Sleep(15 * time.Millisecond)
	allowed, _ := c.Check("svc")
	if !allowed {
		t.Fatal("expected probe slot")
	}
	c.Record("svc", false) // any HALFOPEN failure re-opens
	snap, _ := c.State("svc")
	if snap.State != CircuitOpen {
		t.Fatalf("expected re-OPEN after HALFOPEN probe failure, got %v", snap.State)
	}
}

func TestSagaHappyPathThenFailReturnsCompsLIFO(t *testing.T) {
	s := NewSagas()
	if err := s.Start("order-1", map[string]string{"customer": "alice"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Step("order-1", "reserve_inventory", `{"sku":"A"}`, "RELEASE_INVENTORY A")
	_ = s.Step("order-1", "charge_card", `{"amount":10}`, "REFUND 10")
	_ = s.Step("order-1", "ship_pkg", `{"id":"pkg1"}`, "") // no comp

	comps, err := s.Fail("order-1", "carrier_offline")
	if err != nil {
		t.Fatal(err)
	}
	if len(comps) != 2 {
		t.Fatalf("expected 2 comps, got %d", len(comps))
	}
	// LIFO: most recent step with comp first.
	if comps[0] != "REFUND 10" || comps[1] != "RELEASE_INVENTORY A" {
		t.Fatalf("unexpected comp order: %v", comps)
	}
	snap, _ := s.Status("order-1")
	if snap.State != SagaFailed {
		t.Fatalf("expected SagaFailed, got %v", snap.State)
	}
}

func TestSagaCompleteIsTerminal(t *testing.T) {
	s := NewSagas()
	_ = s.Start("a", nil)
	if err := s.Complete("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Step("a", "extra", "", ""); err == nil {
		t.Fatal("step on completed saga should fail")
	}
}

func TestCRDTGCounterMergeIsCommutative(t *testing.T) {
	r := NewCRDTRegistry()
	if _, err := r.GIncr("clicks", "node-a", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := r.GIncr("clicks-b", "node-a", 1); err != nil {
		t.Fatal(err)
	}
	// node-b's view: a:1 (older), b:5
	_, _ = r.GIncr("clicks-b", "node-b", 5)
	// Merge clicks-b into clicks. node-a gets max(3,1)=3, node-b gets 5.
	if err := r.Merge("clicks", "clicks-b"); err != nil {
		t.Fatal(err)
	}
	v, _ := r.GValue("clicks")
	if v != 8 {
		t.Fatalf("expected 8 (3+5), got %d", v)
	}
}

func TestCRDTPNCounterIncrementsAndDecrements(t *testing.T) {
	r := NewCRDTRegistry()
	_, _ = r.PNIncr("balance", "node-a", 10)
	_, _ = r.PNIncr("balance", "node-b", -3)
	v, _ := r.PNValue("balance")
	if v != 7 {
		t.Fatalf("expected 7, got %d", v)
	}
}

func TestCRDTORSetObservedRemoveSemantics(t *testing.T) {
	r := NewCRDTRegistry()
	// node-a adds member m
	_, _ = r.SAdd("set1", "node-a", "m")
	// node-b adds member m concurrently — separate replica simulated as
	// a sibling key.
	_, _ = r.SAdd("set2", "node-b", "m")
	// node-a observes its own add and removes.
	_, _ = r.SRem("set1", "m")
	// Merge set2 into set1 — the unobserved tag survives.
	_ = r.Merge("set1", "set2")
	members, _ := r.SMembers("set1")
	if len(members) != 1 || members[0] != "m" {
		t.Fatalf("expected member m to survive observed-remove merge, got %v", members)
	}
}

func TestCRDTLWWLatestTimestampWins(t *testing.T) {
	r := NewCRDTRegistry()
	_ = r.LWWSet("flag", "node-a", "off", 100)
	_ = r.LWWSet("flag", "node-b", "on", 200)
	// Older write must not overwrite newer.
	_ = r.LWWSet("flag", "node-a", "off", 150)
	v, ts, actor, _ := r.LWWGet("flag")
	if v != "on" || ts != 200 || actor != "node-b" {
		t.Fatalf("expected (on,200,node-b), got (%s,%d,%s)", v, ts, actor)
	}
}

func TestCRDTKindMismatchRejected(t *testing.T) {
	r := NewCRDTRegistry()
	_, _ = r.GIncr("k", "a", 1)
	if _, err := r.PNIncr("k", "a", 1); err == nil {
		t.Fatal("expected kind-mismatch error")
	}
}
