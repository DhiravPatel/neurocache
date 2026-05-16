package llmstack

import (
	"testing"
)

func TestBanditCreateAndPick(t *testing.T) {
	b := NewBanditRouter()
	if err := b.Create("c1", []string{"a", "b", "c"}, "thompson"); err != nil {
		t.Fatal(err)
	}
	r, ok := b.Pick("c1", 42)
	if !ok {
		t.Fatal("pick returned false")
	}
	if r.Arm != "a" && r.Arm != "b" && r.Arm != "c" {
		t.Fatalf("pick returned unknown arm: %s", r.Arm)
	}
}

func TestBanditRejectsBadConfig(t *testing.T) {
	b := NewBanditRouter()
	if err := b.Create("", []string{"a", "b"}, ""); err == nil {
		t.Fatal("empty bandit_id should fail")
	}
	if err := b.Create("c", []string{"a"}, ""); err == nil {
		t.Fatal("single-arm bandit should fail")
	}
	if err := b.Create("c", []string{"a", "b"}, "magic"); err == nil {
		t.Fatal("unknown strategy should fail")
	}
	if err := b.Create("c", []string{"a", ""}, ""); err == nil {
		t.Fatal("empty arm name should fail")
	}
}

func TestBanditConvergesOnWinningArm(t *testing.T) {
	// Train a 2-arm bandit: arm "good" wins ~90% of the time, "bad" wins ~10%.
	// After many records, Thompson sampling should pick "good" more often.
	b := NewBanditRouter()
	b.Create("c", []string{"good", "bad"}, "thompson")
	for i := 0; i < 200; i++ {
		// "good" arm: 0.9 success rate
		b.Record("c", "good", 1.0)
		if i%10 < 1 {
			b.Record("c", "good", 0.0)
		}
		// "bad" arm: 0.1 success rate
		b.Record("c", "bad", 0.0)
		if i%10 < 1 {
			b.Record("c", "bad", 1.0)
		}
	}
	// Now pick 1000 times — "good" should dominate
	goodCount := 0
	for i := 0; i < 1000; i++ {
		r, _ := b.Pick("c", int64(i+1))
		if r.Arm == "good" {
			goodCount++
		}
	}
	if goodCount < 700 {
		t.Fatalf("Thompson should converge on 'good' arm, got %d/1000 picks", goodCount)
	}
}

func TestBanditUCB(t *testing.T) {
	b := NewBanditRouter()
	b.Create("c", []string{"a", "b"}, "ucb")
	// Unpulled arms always picked first
	r, _ := b.Pick("c", 0)
	if r.Arm == "" {
		t.Fatal("UCB should pick something")
	}
}

func TestBanditRejectsBadScore(t *testing.T) {
	b := NewBanditRouter()
	b.Create("c", []string{"a", "b"}, "")
	if err := b.Record("c", "a", -0.1); err == nil {
		t.Fatal("score < 0 should fail")
	}
	if err := b.Record("c", "a", 1.5); err == nil {
		t.Fatal("score > 1 should fail")
	}
}

func TestBanditRecordUnknownArm(t *testing.T) {
	b := NewBanditRouter()
	b.Create("c", []string{"a", "b"}, "")
	if err := b.Record("c", "magic", 0.5); err == nil {
		t.Fatal("unknown arm should fail")
	}
}

func TestBanditRecordUnknownBandit(t *testing.T) {
	b := NewBanditRouter()
	if err := b.Record("nope", "a", 0.5); err == nil {
		t.Fatal("unknown bandit should fail")
	}
}

func TestBanditStats(t *testing.T) {
	b := NewBanditRouter()
	b.Create("c", []string{"a", "b"}, "thompson")
	b.Record("c", "a", 1.0)
	b.Record("c", "a", 1.0)
	b.Record("c", "b", 0.0)
	s, ok := b.Stats("c")
	if !ok {
		t.Fatal("stats returned false")
	}
	if len(s.Arms) != 2 {
		t.Fatalf("arms = %d", len(s.Arms))
	}
	// Find arm "a"
	var armA BanditArmStats
	for _, ar := range s.Arms {
		if ar.Arm == "a" {
			armA = ar
		}
	}
	if armA.Pulls != 2 {
		t.Fatalf("arm a pulls = %d", armA.Pulls)
	}
	if armA.PosteriorMean < 0.5 {
		t.Fatalf("arm a posterior mean too low: %f", armA.PosteriorMean)
	}
}

func TestBanditReset(t *testing.T) {
	b := NewBanditRouter()
	b.Create("c", []string{"a", "b"}, "thompson")
	b.Record("c", "a", 1.0)
	b.Reset("c")
	s, _ := b.Stats("c")
	if s.Arms[0].Pulls != 0 {
		t.Fatalf("pulls after reset = %d", s.Arms[0].Pulls)
	}
}

func TestBanditForget(t *testing.T) {
	b := NewBanditRouter()
	b.Create("c", []string{"a", "b"}, "thompson")
	if !b.Forget("c") {
		t.Fatal("forget should return true")
	}
	if b.Forget("c") {
		t.Fatal("forget on missing should return false")
	}
}

func TestBanditList(t *testing.T) {
	b := NewBanditRouter()
	b.Create("alpha", []string{"a", "b"}, "")
	b.Create("beta", []string{"a", "b"}, "")
	list := b.List()
	if len(list) != 2 {
		t.Fatalf("list = %v", list)
	}
}

func TestBanditGlobalStats(t *testing.T) {
	b := NewBanditRouter()
	b.Create("c", []string{"a", "b"}, "")
	b.Pick("c", 1)
	b.Record("c", "a", 1.0)
	s := b.GlobalStats()
	if s.Bandits != 1 || s.TotalPicks != 1 || s.TotalRecords != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
