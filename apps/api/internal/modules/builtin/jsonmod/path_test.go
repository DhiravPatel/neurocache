package jsonmod

import "testing"

func TestPathParseAndGet(t *testing.T) {
	doc, err := New([]byte(`{"a":{"b":[1,2,3],"c":"x"},"d":true}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want int // expected number of matches
	}{
		{"$", 1},
		{"$.a", 1},
		{"$.a.b", 1},
		{"$.a.b[0]", 1},
		{"$.a.b[*]", 3},
		{"$.a.*", 2},
		{"$..b", 1},
	}
	for _, tc := range cases {
		p, err := parsePath(tc.path)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.path, err)
		}
		got := p.Get(doc.Root)
		if len(got) != tc.want {
			t.Errorf("Get(%q) = %d matches, want %d (%v)", tc.path, len(got), tc.want, got)
		}
	}
}

func TestPathFilterMatches(t *testing.T) {
	doc, _ := New([]byte(`{"items":[{"name":"a","qty":2},{"name":"b","qty":0},{"name":"c","qty":5}]}`))
	p, err := parsePath("$.items[?(@.qty > 0)]")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := p.Get(doc.Root)
	if len(got) != 2 {
		t.Fatalf("expected 2 items with qty>0, got %d (%v)", len(got), got)
	}
}

func TestPathFilterAndOr(t *testing.T) {
	doc, _ := New([]byte(`{"xs":[{"k":1},{"k":2},{"k":3}]}`))
	p, _ := parsePath(`$.xs[?(@.k == 1 || @.k == 3)]`)
	got := p.Get(doc.Root)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestPathRecursiveFindsAcrossDepth(t *testing.T) {
	doc, _ := New([]byte(`{"x":{"name":"a","sub":{"name":"b"}},"name":"c"}`))
	p, _ := parsePath("$..name")
	got := p.Get(doc.Root)
	if len(got) != 3 {
		t.Fatalf("expected 3 names, got %d (%v)", len(got), got)
	}
}
