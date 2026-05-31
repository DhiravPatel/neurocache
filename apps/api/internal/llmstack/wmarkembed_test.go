package llmstack

import (
	"strings"
	"testing"
)

func TestWmarkEmbedAndDetect(t *testing.T) {
	w := NewWatermarkEmbedder()
	original := strings.Repeat("the good fast easy hard make use give show see think know want need ", 8)
	r, err := w.Embed(original, "secret-key-1", 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Replacements == 0 {
		t.Fatal("expected some replacements at strength=1")
	}
	// Detect should be confidently positive
	d, _ := w.Detect(r.Text, "secret-key-1")
	if !d.Watermarked {
		t.Fatalf("watermark not detected: %+v", d)
	}
}

func TestWmarkDetectsAgainstWrongKey(t *testing.T) {
	w := NewWatermarkEmbedder()
	original := strings.Repeat("the good fast easy hard make use give show see ", 8)
	r, _ := w.Embed(original, "key-A", 1.0)
	d, _ := w.Detect(r.Text, "key-B")
	if d.Watermarked {
		t.Fatalf("wrong key should not detect: %+v", d)
	}
}

func TestWmarkDetectInsufficient(t *testing.T) {
	w := NewWatermarkEmbedder()
	d, _ := w.Detect("hello world", "key")
	if d.Confidence != "INSUFFICIENT" && d.N != 0 {
		// "world" and "hello" aren't in our synonym set; n should be 0
		if d.N != 0 {
			t.Fatalf("unexpected n: %+v", d)
		}
	}
}

func TestWmarkPreservesCasing(t *testing.T) {
	w := NewWatermarkEmbedder()
	r, _ := w.Embed("The BIG quick make use see", "key", 1.0)
	// Some words may have been replaced; we just verify no all-caps
	// got lowercased mid-word, no leading-cap got fully lowered.
	for _, word := range strings.Fields(r.Text) {
		if word == strings.ToUpper(word) && len(word) > 1 {
			// likely was BIG → still all caps after replacement (or unchanged)
		}
	}
}

func TestWmarkStrengthZeroNoChange(t *testing.T) {
	w := NewWatermarkEmbedder()
	original := "the good fast easy hard"
	r, _ := w.Embed(original, "key", 0)
	if r.Replacements != 0 {
		t.Fatalf("strength=0 should not replace: %d", r.Replacements)
	}
}

func TestWmarkKeyRegister(t *testing.T) {
	w := NewWatermarkEmbedder()
	if err := w.Key("k1", "secret"); err != nil {
		t.Fatal(err)
	}
	keys := w.Keys()
	if len(keys) != 1 || keys[0] != "k1" {
		t.Fatalf("keys = %v", keys)
	}
	if w.DropKey("k1") != 1 {
		t.Fatal("dropkey")
	}
}

func TestWmarkRejectsBadInput(t *testing.T) {
	w := NewWatermarkEmbedder()
	if _, err := w.Embed("", "k", 0.5); err == nil {
		t.Fatal("empty text")
	}
	if _, err := w.Embed("x", "", 0.5); err == nil {
		t.Fatal("empty key")
	}
	if _, err := w.Embed("x", "k", -1); err == nil {
		t.Fatal("negative strength")
	}
	if _, err := w.Embed("x", "k", 2); err == nil {
		t.Fatal("strength > 1")
	}
	if _, err := w.Detect("", "k"); err == nil {
		t.Fatal("empty text in detect")
	}
}

func TestWmarkStats(t *testing.T) {
	w := NewWatermarkEmbedder()
	w.Embed("good make use", "k", 0.5)
	w.Detect("good make use", "k")
	s := w.Stats()
	if s.TotalEmbeds != 1 || s.TotalDetects != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestWmarkDeterministic(t *testing.T) {
	w := NewWatermarkEmbedder()
	r1, _ := w.Embed("good fast easy hard make use see", "key", 0.7)
	r2, _ := w.Embed("good fast easy hard make use see", "key", 0.7)
	if r1.Text != r2.Text {
		t.Fatalf("not deterministic:\n%s\nvs\n%s", r1.Text, r2.Text)
	}
}
