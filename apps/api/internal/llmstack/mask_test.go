package llmstack

import (
	"strings"
	"testing"
)

func TestMaskBuiltinFormats(t *testing.T) {
	m := NewMaskTemplates()
	// Pre-loaded starcoder format
	out, ok := m.Build("starcoder", "def fibonacci(n):", "    return result", "")
	if !ok {
		t.Fatal("starcoder format should be pre-loaded")
	}
	if !strings.Contains(out, "<fim_prefix>") || !strings.Contains(out, "<fim_middle>") {
		t.Fatalf("starcoder template not applied: %s", out)
	}
}

func TestMaskRegisterAndBuild(t *testing.T) {
	m := NewMaskTemplates()
	m.Register("custom", "BEFORE: {PREFIX}\nMASK\nAFTER: {SUFFIX}")
	out, _ := m.Build("custom", "x = 1", "return x", "")
	if !strings.Contains(out, "BEFORE: x = 1") || !strings.Contains(out, "AFTER: return x") {
		t.Fatalf("template not applied: %s", out)
	}
}

func TestMaskMaskValSubstitution(t *testing.T) {
	m := NewMaskTemplates()
	m.Register("with_mask", "{PREFIX}{MASK}{SUFFIX}")
	out, _ := m.Build("with_mask", "Hello ", " World", "<FILL>")
	if out != "Hello <FILL> World" {
		t.Fatalf("got = %q", out)
	}
}

func TestMaskRejectsBadTemplate(t *testing.T) {
	m := NewMaskTemplates()
	if err := m.Register("bad", "no placeholders"); err == nil {
		t.Fatal("template without {PREFIX} should fail")
	}
	if err := m.Register("bad", "{PREFIX} only"); err == nil {
		t.Fatal("template without {SUFFIX} should fail")
	}
}

func TestMaskRejectsBadID(t *testing.T) {
	m := NewMaskTemplates()
	if err := m.Register("", "{PREFIX}{SUFFIX}"); err == nil {
		t.Fatal("empty format_id should fail")
	}
}

func TestMaskUnregister(t *testing.T) {
	m := NewMaskTemplates()
	m.Register("custom", "{PREFIX}|{SUFFIX}")
	if !m.Unregister("custom") {
		t.Fatal("unregister should return true")
	}
	if _, ok := m.Build("custom", "a", "b", ""); ok {
		t.Fatal("build should fail after unregister")
	}
}

func TestMaskUnknownFormat(t *testing.T) {
	m := NewMaskTemplates()
	if _, ok := m.Build("nope", "a", "b", ""); ok {
		t.Fatal("unknown format should fail")
	}
}

func TestMaskReplaceExisting(t *testing.T) {
	m := NewMaskTemplates()
	m.Register("custom", "v1: {PREFIX}|{SUFFIX}")
	m.Register("custom", "v2: {PREFIX}-{SUFFIX}")
	out, _ := m.Build("custom", "a", "b", "")
	if !strings.HasPrefix(out, "v2:") {
		t.Fatalf("replace failed: %s", out)
	}
}

func TestMaskListIncludesBuiltins(t *testing.T) {
	m := NewMaskTemplates()
	rows := m.List()
	names := map[string]bool{}
	for _, r := range rows {
		names[r.FormatID] = true
	}
	for _, want := range []string{"starcoder", "codellama", "deepseek", "mask_token"} {
		if !names[want] {
			t.Errorf("builtin %s missing from LIST", want)
		}
	}
}

func TestMaskStatsAdvance(t *testing.T) {
	m := NewMaskTemplates()
	m.Register("x", "{PREFIX}|{SUFFIX}")
	m.Build("x", "a", "b", "")
	m.Build("x", "c", "d", "")
	s := m.Stats()
	if s.TotalBuilds != 2 || s.TotalRegisters != 1 {
		t.Fatalf("stats = %+v", s)
	}
}
