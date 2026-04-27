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

func TestPathFilterRejected(t *testing.T) {
	if _, err := parsePath("$..price[?(@.qty > 0)]"); err == nil {
		t.Fatal("filter expression should error")
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
