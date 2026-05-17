package llmstack

import "testing"

func TestProofCommitProduceVerify(t *testing.T) {
	p := NewProofRegistry()
	h, err := p.Commit("c1", "gpt-4o", "summarise this", `{"temp":0.7}`)
	if err != nil {
		t.Fatal(err)
	}
	if h == "" {
		t.Fatal("commit hash empty")
	}
	r, _ := p.Produce("c1", "r1", "the summary")
	v, _ := p.Verify("r1", "gpt-4o", "summarise this", `{"temp":0.7}`, "the summary")
	if !v.Valid {
		t.Fatalf("verify: %+v", v)
	}
	if r.CommitHash == "" {
		t.Fatal("receipt commit hash empty")
	}
}

func TestProofVerifyRejectsTamperedOutput(t *testing.T) {
	p := NewProofRegistry()
	p.Commit("c", "m", "p", `{}`)
	p.Produce("c", "r", "original output")
	v, _ := p.Verify("r", "m", "p", `{}`, "TAMPERED")
	if v.Valid {
		t.Fatalf("tampered output should fail: %+v", v)
	}
}

func TestProofVerifyRejectsTamperedCommitment(t *testing.T) {
	p := NewProofRegistry()
	p.Commit("c", "m", "p", `{}`)
	p.Produce("c", "r", "out")
	// Caller pretends a different prompt was committed
	v, _ := p.Verify("r", "m", "different-prompt", `{}`, "out")
	if v.Valid {
		t.Fatalf("tampered commitment should fail: %+v", v)
	}
}

func TestProofCommitDeterministic(t *testing.T) {
	a := NewProofRegistry()
	b := NewProofRegistry()
	ha, _ := a.Commit("x", "m", "p", `{"a":1,"b":2}`)
	hb, _ := b.Commit("x", "m", "p", `{"b":2,"a":1}`)
	if ha != hb {
		t.Fatalf("key order should not matter: %s vs %s", ha, hb)
	}
}

func TestProofCommitDuplicate(t *testing.T) {
	p := NewProofRegistry()
	p.Commit("c", "m", "p", `{}`)
	if _, err := p.Commit("c", "m", "p", `{}`); err == nil {
		t.Fatal("duplicate commit should fail")
	}
}

func TestProofProduceUnknownCommit(t *testing.T) {
	p := NewProofRegistry()
	if _, err := p.Produce("ghost", "r", "x"); err == nil {
		t.Fatal("unknown commit should fail")
	}
}

func TestProofProduceDuplicateReceipt(t *testing.T) {
	p := NewProofRegistry()
	p.Commit("c", "m", "p", `{}`)
	p.Produce("c", "r", "x")
	if _, err := p.Produce("c", "r", "y"); err == nil {
		t.Fatal("duplicate receipt should fail")
	}
}

func TestProofListGetForget(t *testing.T) {
	p := NewProofRegistry()
	p.Commit("c", "m", "p", `{}`)
	p.Produce("c", "r", "x")
	if rows := p.List(10); len(rows) != 1 {
		t.Fatalf("list: %d", len(rows))
	}
	if _, ok := p.Get("r"); !ok {
		t.Fatal("get")
	}
	if p.Forget("r") != 1 {
		t.Fatal("forget")
	}
	if p.Forget("ALL") != 0 {
		t.Fatal("empty forget all")
	}
}

func TestProofStats(t *testing.T) {
	p := NewProofRegistry()
	p.Commit("c", "m", "p", `{}`)
	p.Produce("c", "r", "x")
	p.Verify("r", "m", "p", `{}`, "x")
	s := p.Stats()
	if s.TotalCommits != 1 || s.TotalProduces != 1 || s.TotalVerifies != 1 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestProofRejectsBadInput(t *testing.T) {
	p := NewProofRegistry()
	if _, err := p.Commit("", "m", "p", "{}"); err == nil {
		t.Fatal("empty id")
	}
	if _, err := p.Commit("c", "", "p", "{}"); err == nil {
		t.Fatal("empty model")
	}
	if _, err := p.Commit("c", "m", "", "{}"); err == nil {
		t.Fatal("empty prompt")
	}
	if _, err := p.Commit("c", "m", "p", "{bad"); err == nil {
		t.Fatal("bad params JSON")
	}
}
