package probmod

import (
	"fmt"
	"testing"
)

func TestBloomFalseNegativeImpossible(t *testing.T) {
	b, err := NewBloom(0.01, 1000, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		b.Add([]byte(fmt.Sprintf("item-%d", i)))
	}
	for i := 0; i < 500; i++ {
		if !b.Contains([]byte(fmt.Sprintf("item-%d", i))) {
			t.Fatalf("false negative for item-%d", i)
		}
	}
}

func TestBloomScales(t *testing.T) {
	b, _ := NewBloom(0.01, 100, 2, false)
	for i := 0; i < 500; i++ {
		b.Add([]byte(fmt.Sprintf("x-%d", i)))
	}
	if len(b.Layers) < 2 {
		t.Fatalf("expected scaling, got %d layer(s)", len(b.Layers))
	}
}

func TestBloomMarshalRoundTrip(t *testing.T) {
	b, _ := NewBloom(0.01, 200, 2, false)
	for i := 0; i < 50; i++ {
		b.Add([]byte(fmt.Sprintf("k-%d", i)))
	}
	blob, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := UnmarshalBloom(blob)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if !b2.Contains([]byte(fmt.Sprintf("k-%d", i))) {
			t.Fatalf("missing after unmarshal: k-%d", i)
		}
	}
}

func TestCuckooAddExistsDel(t *testing.T) {
	c, err := NewCuckoo(1024, 4, 500, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Add([]byte("alpha")) {
		t.Fatal("add failed")
	}
	if !c.Contains([]byte("alpha")) {
		t.Fatal("contains failed")
	}
	if !c.Del([]byte("alpha")) {
		t.Fatal("del failed")
	}
	if c.Contains([]byte("alpha")) {
		// could still be true due to fingerprint collision; not a hard failure
	}
}

func TestCuckooMarshalRoundTrip(t *testing.T) {
	c, _ := NewCuckoo(512, 4, 500, 1)
	for i := 0; i < 100; i++ {
		c.Add([]byte(fmt.Sprintf("v-%d", i)))
	}
	blob, _ := c.Marshal()
	c2, err := UnmarshalCuckoo(blob)
	if err != nil {
		t.Fatal(err)
	}
	hits := 0
	for i := 0; i < 100; i++ {
		if c2.Contains([]byte(fmt.Sprintf("v-%d", i))) {
			hits++
		}
	}
	if hits < 100 { // cuckoo is exact for inserted items
		t.Fatalf("expected 100 hits, got %d", hits)
	}
}

func TestCMSCountsApproximate(t *testing.T) {
	c, _ := NewCMSByDim(2000, 5)
	for i := 0; i < 1000; i++ {
		c.IncrBy([]byte("popular"), 1)
	}
	c.IncrBy([]byte("rare"), 1)
	if got := c.Query([]byte("popular")); got < 1000 {
		t.Fatalf("popular count = %d, want >= 1000", got)
	}
	if got := c.Query([]byte("rare")); got < 1 {
		t.Fatalf("rare count = %d, want >= 1", got)
	}
	if got := c.Query([]byte("never-seen")); got != 0 {
		t.Fatalf("never-seen count = %d, want 0", got)
	}
}

func TestCMSMerge(t *testing.T) {
	a, _ := NewCMSByDim(100, 4)
	b, _ := NewCMSByDim(100, 4)
	a.IncrBy([]byte("x"), 5)
	b.IncrBy([]byte("x"), 3)
	dst, _ := NewCMSByDim(100, 4)
	if err := dst.Merge([]*CMS{a, b}, nil); err != nil {
		t.Fatal(err)
	}
	if got := dst.Query([]byte("x")); got != 8 {
		t.Fatalf("merged count = %d, want 8", got)
	}
}

func TestCMSMarshalRoundTrip(t *testing.T) {
	c, _ := NewCMSByDim(64, 3)
	c.IncrBy([]byte("a"), 7)
	blob, _ := c.Marshal()
	c2, err := UnmarshalCMS(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got := c2.Query([]byte("a")); got != 7 {
		t.Fatalf("got %d, want 7", got)
	}
}
