package llmstack

import (
	"strings"
	"testing"
)

func TestContractDiffAddedOperationIsSafe(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{"operations":{"foo":{"args":{}}}}`)
	c.Register("t", "v2", `{"operations":{"foo":{"args":{}},"bar":{"args":{}}}}`)
	d, err := c.Diff("t", "v1", "v2")
	if err != nil {
		t.Fatal(err)
	}
	if d.Verdict != "NON-BREAKING" {
		t.Fatalf("verdict = %s", d.Verdict)
	}
}

func TestContractDiffRemovedOperationIsBreaking(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{"operations":{"foo":{"args":{}}}}`)
	c.Register("t", "v2", `{"operations":{}}`)
	d, _ := c.Diff("t", "v1", "v2")
	if d.Verdict != "BREAKING" {
		t.Fatalf("verdict = %s", d.Verdict)
	}
}

func TestContractDiffNewRequiredFieldBreaking(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{"operations":{"foo":{"args":{}}}}`)
	c.Register("t", "v2", `{"operations":{"foo":{"args":{"x":{"type":"string","required":true}}}}}`)
	d, _ := c.Diff("t", "v1", "v2")
	if d.Verdict != "BREAKING" {
		t.Fatalf("verdict = %s", d.Verdict)
	}
}

func TestContractDiffNewRequiredFieldWithDefaultIsRisky(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{"operations":{"foo":{"args":{}}}}`)
	c.Register("t", "v2", `{"operations":{"foo":{"args":{"x":{"type":"string","required":true,"default":"a"}}}}}`)
	d, _ := c.Diff("t", "v1", "v2")
	if d.Verdict != "RISKY" {
		t.Fatalf("verdict = %s", d.Verdict)
	}
}

func TestContractDiffTypeChangeBreaking(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{"operations":{"foo":{"args":{"x":{"type":"string"}}}}}`)
	c.Register("t", "v2", `{"operations":{"foo":{"args":{"x":{"type":"number"}}}}}`)
	d, _ := c.Diff("t", "v1", "v2")
	if d.Verdict != "BREAKING" {
		t.Fatalf("verdict = %s", d.Verdict)
	}
}

func TestContractDiffEnumRemovedBreaking(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{"operations":{"foo":{"args":{"x":{"type":"string","enum":["a","b","c"]}}}}}`)
	c.Register("t", "v2", `{"operations":{"foo":{"args":{"x":{"type":"string","enum":["a","b"]}}}}}`)
	d, _ := c.Diff("t", "v1", "v2")
	if d.Verdict != "BREAKING" {
		t.Fatalf("enum-removed verdict = %s", d.Verdict)
	}
}

func TestContractDiffEnumAddedRisky(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{"operations":{"foo":{"args":{"x":{"type":"string","enum":["a","b"]}}}}}`)
	c.Register("t", "v2", `{"operations":{"foo":{"args":{"x":{"type":"string","enum":["a","b","c"]}}}}}`)
	d, _ := c.Diff("t", "v1", "v2")
	if d.Verdict != "RISKY" {
		t.Fatalf("enum-added verdict = %s", d.Verdict)
	}
}

func TestContractDiffDefaultChangeRisky(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{"operations":{"foo":{"args":{"x":{"type":"string","default":"a"}}}}}`)
	c.Register("t", "v2", `{"operations":{"foo":{"args":{"x":{"type":"string","default":"b"}}}}}`)
	d, _ := c.Diff("t", "v1", "v2")
	if d.Verdict != "RISKY" {
		t.Fatalf("default-change verdict = %s", d.Verdict)
	}
}

func TestContractDiffMaxLenShrunkRisky(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{"operations":{"foo":{"args":{"x":{"type":"string","max_len":100}}}}}`)
	c.Register("t", "v2", `{"operations":{"foo":{"args":{"x":{"type":"string","max_len":50}}}}}`)
	d, _ := c.Diff("t", "v1", "v2")
	if d.Verdict != "RISKY" {
		t.Fatalf("constraint-tighten verdict = %s", d.Verdict)
	}
}

func TestContractHintMatchesVerdict(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{"operations":{"foo":{"args":{}}}}`)
	c.Register("t", "v2", `{"operations":{"bar":{"args":{}}}}`) // both removed foo and added bar
	d, _ := c.Diff("t", "v1", "v2")
	if d.Hint == "" || !strings.Contains(d.Hint, "major") {
		t.Fatalf("hint = %s", d.Hint)
	}
}

func TestContractInvalidJSONRejected(t *testing.T) {
	c := NewContractEvolve()
	if err := c.Register("t", "v1", "{not json"); err == nil {
		t.Fatal("invalid JSON should fail")
	}
}

func TestContractDiffUnknown(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{}`)
	if _, err := c.Diff("t", "v1", "v99"); err == nil {
		t.Fatal("unknown version should fail")
	}
	if _, err := c.Diff("ghost", "v1", "v2"); err == nil {
		t.Fatal("unknown tool should fail")
	}
}

func TestContractVersions(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v3", `{}`)
	c.Register("t", "v1", `{}`)
	c.Register("t", "v2", `{}`)
	v := c.Versions("t")
	if len(v) != 3 || v[0] != "v1" {
		t.Fatalf("versions = %v", v)
	}
}

func TestContractForget(t *testing.T) {
	c := NewContractEvolve()
	c.Register("a", "v1", `{}`)
	c.Register("b", "v1", `{}`)
	if c.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if c.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestContractEvolveStats(t *testing.T) {
	c := NewContractEvolve()
	c.Register("t", "v1", `{}`)
	c.Register("t", "v2", `{}`)
	c.Diff("t", "v1", "v2")
	s := c.Stats()
	if s.TotalRegisters != 2 || s.TotalDiffs != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestContractRejectsBadInput(t *testing.T) {
	c := NewContractEvolve()
	if err := c.Register("", "v", `{}`); err == nil {
		t.Fatal("empty tool should fail")
	}
	if err := c.Register("t", "", `{}`); err == nil {
		t.Fatal("empty version should fail")
	}
	if err := c.Register("t", "v", ""); err == nil {
		t.Fatal("empty schema should fail")
	}
}
