package llmstack

import (
	"testing"
	"time"
)

func TestAgentBBPostAndRead(t *testing.T) {
	b := NewAgentBlackboard()
	if _, err := b.Post("run1", "agent-a", "EU VAT is 21% for SaaS", []string{"pricing", "eu"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Post("run1", "agent-b", "users prefer monthly billing", []string{"billing"}); err != nil {
		t.Fatal(err)
	}
	rows, ok := b.Read("run1", "european tax rates", 5, 0)
	if !ok {
		t.Fatal("read missed run")
	}
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	// Top result must be the VAT post (highest cosine to query)
	if rows[0].AgentID != "agent-a" {
		t.Fatalf("top = %v, want agent-a", rows[0])
	}
}

func TestAgentBBReadEmptyRun(t *testing.T) {
	b := NewAgentBlackboard()
	if _, ok := b.Read("nope", "x", 5, 0); ok {
		t.Fatal("read on missing run should fail")
	}
}

func TestAgentBBListReverseChronological(t *testing.T) {
	b := NewAgentBlackboard()
	b.Post("run1", "a", "first", nil)
	b.Post("run1", "a", "second", nil)
	b.Post("run1", "a", "third", nil)
	rows, _ := b.List("run1", 10, "")
	if rows[0].Text != "third" || rows[2].Text != "first" {
		t.Fatalf("not reverse chrono: %+v", rows)
	}
}

func TestAgentBBListTagFilter(t *testing.T) {
	b := NewAgentBlackboard()
	b.Post("run1", "a", "x", []string{"red"})
	b.Post("run1", "a", "y", []string{"blue"})
	b.Post("run1", "a", "z", []string{"red"})
	rows, _ := b.List("run1", 10, "red")
	if len(rows) != 2 {
		t.Fatalf("tag filter = %d", len(rows))
	}
}

func TestAgentBBClaimAtomic(t *testing.T) {
	b := NewAgentBlackboard()
	r1, _ := b.Claim("run1", "task-pay", "agent-3", 0)
	r2, _ := b.Claim("run1", "task-pay", "agent-7", 0)
	if !r1.Claimed {
		t.Fatal("first claim should win")
	}
	if r2.Claimed {
		t.Fatal("second claim should lose")
	}
	if r2.Owner != "agent-3" {
		t.Fatalf("owner = %s, want agent-3", r2.Owner)
	}
}

func TestAgentBBClaimTTLExpiry(t *testing.T) {
	b := NewAgentBlackboard()
	b.Claim("run1", "t", "agent-1", 5*time.Millisecond)
	time.Sleep(15 * time.Millisecond)
	r, _ := b.Claim("run1", "t", "agent-2", 0)
	if !r.Claimed {
		t.Fatal("expired claim should be reclaimable")
	}
	if r.Owner != "agent-2" {
		t.Fatalf("new owner = %s", r.Owner)
	}
}

func TestAgentBBReleaseOnlyByOwner(t *testing.T) {
	b := NewAgentBlackboard()
	b.Claim("run1", "t", "owner", 0)
	if n, _ := b.Release("run1", "t", "intruder"); n != 0 {
		t.Fatal("non-owner release should fail")
	}
	if n, _ := b.Release("run1", "t", "owner"); n != 1 {
		t.Fatal("owner release should succeed")
	}
}

func TestAgentBBClaimsLists(t *testing.T) {
	b := NewAgentBlackboard()
	b.Claim("run1", "a", "x", time.Hour)
	b.Claim("run1", "b", "y", 0)
	rows, _ := b.Claims("run1")
	if len(rows) != 2 {
		t.Fatalf("claims = %d", len(rows))
	}
}

func TestAgentBBDropRun(t *testing.T) {
	b := NewAgentBlackboard()
	b.Post("a", "x", "y", nil)
	b.Post("b", "x", "y", nil)
	if b.Drop("a") != 1 {
		t.Fatal("drop a should remove 1")
	}
	if b.Drop("ALL") != 1 {
		t.Fatal("ALL drop should remove remaining 1")
	}
}

func TestAgentBBStats(t *testing.T) {
	b := NewAgentBlackboard()
	b.Post("r", "a", "hello", nil)
	b.Read("r", "h", 5, 0)
	b.Claim("r", "t1", "a", 0)
	b.Claim("r", "t1", "b", 0) // conflict
	s := b.Stats()
	if s.TotalPosts != 1 || s.TotalReads != 1 || s.TotalClaims != 2 {
		t.Fatalf("stats = %+v", s)
	}
	if s.ClaimConflicts != 1 {
		t.Fatalf("conflicts = %d", s.ClaimConflicts)
	}
}

func TestAgentBBRejectsBadInput(t *testing.T) {
	b := NewAgentBlackboard()
	if _, err := b.Post("", "a", "x", nil); err == nil {
		t.Fatal("empty run_id should fail")
	}
	if _, err := b.Post("r", "", "x", nil); err == nil {
		t.Fatal("empty agent_id should fail")
	}
	if _, err := b.Post("r", "a", "", nil); err == nil {
		t.Fatal("empty text should fail")
	}
	if _, err := b.Claim("r", "t", "a", -1); err == nil {
		t.Fatal("negative ttl should fail")
	}
}

func TestAgentBusRoutesBySemantics(t *testing.T) {
	b := NewAgentBus()
	b.Register("sql-bot", "writes and reviews SQL migrations")
	b.Register("ui-bot", "designs react components and css")
	r, err := b.Send("need a migration for the new orders table", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.RoutedTo != "sql-bot" {
		t.Fatalf("routed_to = %s, want sql-bot", r.RoutedTo)
	}
	if r.Match <= 0 {
		t.Fatalf("match = %f", r.Match)
	}
}

func TestAgentBusUnroutedWhenNoMatch(t *testing.T) {
	b := NewAgentBus()
	b.Register("agent", "purely about cooking recipes")
	r, _ := b.Send("write me a python script", 0.95, "")
	if r.RoutedTo != "" {
		t.Fatalf("should be unrouted with high min_sim, got %s", r.RoutedTo)
	}
}

func TestAgentBusRecvAndAck(t *testing.T) {
	b := NewAgentBus()
	b.Register("a", "things")
	b.Send("hello", 0, "x")
	rows, _ := b.Recv("a", 0)
	if len(rows) != 1 {
		t.Fatalf("recv = %d", len(rows))
	}
	if n, _ := b.Ack("a", rows[0].MsgID); n != 1 {
		t.Fatal("ack should succeed")
	}
	rows2, _ := b.Recv("a", 0)
	if len(rows2) != 0 {
		t.Fatalf("after ack = %d", len(rows2))
	}
}

func TestAgentBusReregisterOverwrites(t *testing.T) {
	b := NewAgentBus()
	b.Register("a", "old capability")
	b.Register("a", "new capability")
	a := b.Agents()
	if len(a) != 1 || a[0].Capability != "new capability" {
		t.Fatalf("agents = %+v", a)
	}
}

func TestAgentBusUnregister(t *testing.T) {
	b := NewAgentBus()
	b.Register("a", "x")
	if b.Unregister("a") != 1 {
		t.Fatal("unregister should drop one")
	}
	if b.Unregister("a") != 0 {
		t.Fatal("repeat unregister should be 0")
	}
}

func TestAgentBusStats(t *testing.T) {
	b := NewAgentBus()
	b.Register("a", "things")
	b.Send("hello", 0, "")
	b.Send("nothing here matches", 0.99, "") // unrouted
	s := b.Stats()
	if s.Agents != 1 {
		t.Fatalf("agents = %d", s.Agents)
	}
	if s.TotalSent != 2 || s.Unrouted != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestAgentBusRejectsBadInput(t *testing.T) {
	b := NewAgentBus()
	if err := b.Register("", "x"); err == nil {
		t.Fatal("empty agent should fail")
	}
	if err := b.Register("a", ""); err == nil {
		t.Fatal("empty capability should fail")
	}
	if _, err := b.Send("", 0, ""); err == nil {
		t.Fatal("empty message should fail")
	}
}

func BenchmarkAgentBBPost(b *testing.B) {
	bb := NewAgentBlackboard()
	for i := 0; i < b.N; i++ {
		bb.Post("run", "a", "the quick brown fox jumps over the lazy dog", nil)
	}
}

func BenchmarkAgentBusSend(b *testing.B) {
	bus := NewAgentBus()
	for i := 0; i < 20; i++ {
		bus.Register("agent-"+itoaBench(i), "handles "+itoaBench(i))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.Send("handles 5 something", 0, "")
	}
}
