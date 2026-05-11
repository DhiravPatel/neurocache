package llmstack

import (
	"strings"
	"testing"
)

func TestRedactScrubBuiltins(t *testing.T) {
	r := NewRedactor()
	in := "Email me at jane.doe@example.com or call 555-123-4567. SSN 123-45-6789."
	res := r.Scrub(in)
	if strings.Contains(res.Text, "jane.doe@example.com") {
		t.Fatalf("email leaked: %q", res.Text)
	}
	if strings.Contains(res.Text, "555-123-4567") {
		t.Fatalf("phone leaked: %q", res.Text)
	}
	if strings.Contains(res.Text, "123-45-6789") {
		t.Fatalf("ssn leaked: %q", res.Text)
	}
	if res.Replacements["email"] != 1 {
		t.Fatalf("email replacements = %d, want 1", res.Replacements["email"])
	}
	if res.Replacements["phone-us"] != 1 {
		t.Fatalf("phone replacements = %d, want 1", res.Replacements["phone-us"])
	}
	if res.Replacements["ssn"] != 1 {
		t.Fatalf("ssn replacements = %d, want 1", res.Replacements["ssn"])
	}
}

func TestRedactRestoreRoundTrip(t *testing.T) {
	r := NewRedactor()
	in := "Order from cust@shop.io paid 4111-1111-1111-1111."
	scrub := r.Scrub(in)
	if !strings.Contains(scrub.Text, "<EMAIL_1>") {
		t.Fatalf("expected <EMAIL_1> in %q", scrub.Text)
	}
	if !strings.Contains(scrub.Text, "<CARD_1>") {
		t.Fatalf("expected <CARD_1> in %q", scrub.Text)
	}
	// Imagine the LLM responded with the placeholders intact.
	llmResp := "Refund " + scrub.Text + " to original card."
	restored, ok := r.Restore(scrub.RestoreToken, llmResp)
	if !ok {
		t.Fatal("restore returned false")
	}
	if !strings.Contains(restored, "cust@shop.io") {
		t.Fatalf("email not restored: %q", restored)
	}
	if !strings.Contains(restored, "4111-1111-1111-1111") {
		t.Fatalf("card not restored: %q", restored)
	}
}

func TestRedactRestoreUnknownToken(t *testing.T) {
	r := NewRedactor()
	out, ok := r.Restore("nope", "hello")
	if ok {
		t.Fatal("expected false for unknown token")
	}
	if out != "hello" {
		t.Fatalf("text changed unexpectedly: %q", out)
	}
}

func TestRedactForgetToken(t *testing.T) {
	r := NewRedactor()
	res := r.Scrub("ping me at me@x.io")
	if !r.ForgetToken(res.RestoreToken) {
		t.Fatal("ForgetToken returned false on existing token")
	}
	if r.ForgetToken(res.RestoreToken) {
		t.Fatal("ForgetToken returned true on already-forgotten token")
	}
	if _, ok := r.Restore(res.RestoreToken, ""); ok {
		t.Fatal("Restore succeeded after forget")
	}
}

func TestRedactCustomPattern(t *testing.T) {
	r := NewRedactor()
	if err := r.Add("employee", `EMP-\d{6}`, "<EMP>"); err != nil {
		t.Fatal(err)
	}
	res := r.Scrub("Bug filed by EMP-123456 at 9am.")
	if strings.Contains(res.Text, "EMP-123456") {
		t.Fatalf("custom pattern not applied: %q", res.Text)
	}
	if res.Replacements["employee"] != 1 {
		t.Fatalf("employee count = %d", res.Replacements["employee"])
	}
}

func TestRedactRemovePattern(t *testing.T) {
	r := NewRedactor()
	if !r.Remove("ipv4") {
		t.Fatal("expected ipv4 to be removable")
	}
	res := r.Scrub("server 10.0.0.1 is down")
	if !strings.Contains(res.Text, "10.0.0.1") {
		t.Fatalf("ipv4 should not be redacted after remove: %q", res.Text)
	}
}

func TestRedactPatternListContainsBuiltins(t *testing.T) {
	r := NewRedactor()
	rows := r.Patterns()
	want := map[string]bool{
		"email": true, "phone-us": true, "ssn": true,
		"credit-card": true, "ipv4": true, "api-key": true,
	}
	got := map[string]bool{}
	for _, p := range rows {
		got[p.Name] = true
		if !p.Builtin {
			t.Fatalf("pattern %s should be marked builtin", p.Name)
		}
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("missing builtin pattern %s", k)
		}
	}
}

func TestRedactStatsAdvance(t *testing.T) {
	r := NewRedactor()
	r.Scrub("hi")                     // no hits, but +1 scrub
	r.Scrub("a@b.co and c@d.io")      // 1 scrub, 1 hits-call (multi-replace)
	res := r.Scrub("e@f.io")
	if _, ok := r.Restore(res.RestoreToken, "<EMAIL_1>"); !ok {
		t.Fatal("expected restore success")
	}
	s := r.Stats()
	if s.TotalScrubs != 3 {
		t.Fatalf("scrubs = %d", s.TotalScrubs)
	}
	if s.TotalHits != 2 {
		t.Fatalf("hits = %d", s.TotalHits)
	}
	if s.TotalRestores != 1 {
		t.Fatalf("restores = %d", s.TotalRestores)
	}
}

func TestRedactPlaceholderNumbering(t *testing.T) {
	r := NewRedactor()
	res := r.Scrub("a@x.io b@x.io c@x.io")
	if !strings.Contains(res.Text, "<EMAIL_1>") ||
		!strings.Contains(res.Text, "<EMAIL_2>") ||
		!strings.Contains(res.Text, "<EMAIL_3>") {
		t.Fatalf("expected EMAIL_1..3 in %q", res.Text)
	}
	restored, _ := r.Restore(res.RestoreToken, res.Text)
	if !strings.Contains(restored, "a@x.io") ||
		!strings.Contains(restored, "b@x.io") ||
		!strings.Contains(restored, "c@x.io") {
		t.Fatalf("not all originals restored: %q", restored)
	}
}
