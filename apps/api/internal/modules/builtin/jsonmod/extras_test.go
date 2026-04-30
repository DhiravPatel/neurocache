package jsonmod

import (
	"encoding/json"
	"reflect"
	"testing"
)

// mergePatch is the RFC 7396 worker — exercise it directly so we cover
// the matrix without dragging the module ABI into the test harness.

func TestMergePatchAddsAndOverwrites(t *testing.T) {
	target := mustJSON(`{"a":1,"b":{"x":1,"y":2}}`)
	patch := mustJSON(`{"a":99,"b":{"y":99,"z":3}}`)
	got := mergePatch(target, patch)
	want := mustJSON(`{"a":99,"b":{"x":1,"y":99,"z":3}}`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge mismatch\n got: %v\nwant: %v", got, want)
	}
}

func TestMergePatchDeletesViaNull(t *testing.T) {
	target := mustJSON(`{"a":1,"b":2,"c":{"x":1,"y":2}}`)
	patch := mustJSON(`{"a":null,"c":{"x":null}}`)
	got := mergePatch(target, patch).(map[string]any)
	if _, present := got["a"]; present {
		t.Fatal("a should have been deleted")
	}
	c := got["c"].(map[string]any)
	if _, present := c["x"]; present {
		t.Fatal("c.x should have been deleted")
	}
	if _, present := c["y"]; !present {
		t.Fatal("c.y should remain")
	}
}

func TestMergePatchScalarReplacesObject(t *testing.T) {
	target := mustJSON(`{"a":{"deep":1}}`)
	patch := mustJSON(`{"a":"plain"}`)
	got := mergePatch(target, patch).(map[string]any)
	if got["a"] != "plain" {
		t.Fatalf("expected scalar replacement, got %v", got["a"])
	}
}

func TestMergePatchNonObjectPatchReplaces(t *testing.T) {
	target := mustJSON(`{"a":1}`)
	patch := mustJSON(`[1,2,3]`)
	got := mergePatch(target, patch)
	want := mustJSON(`[1,2,3]`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("array patch should replace target, got %v", got)
	}
}

// ── ARRINDEX deep equality ────────────────────────────────────────

func TestSearchInArrayScalars(t *testing.T) {
	arr := []any{"a", "b", "c", "b"}
	if got := searchInArray(arr, "b", 0, 0); got != 1 {
		t.Fatalf("first b at idx 1, got %d", got)
	}
	if got := searchInArray(arr, "b", 2, 0); got != 3 {
		t.Fatalf("from idx 2 first b at 3, got %d", got)
	}
	if got := searchInArray(arr, "z", 0, 0); got != -1 {
		t.Fatalf("missing should return -1, got %d", got)
	}
}

func TestSearchInArrayDeepObject(t *testing.T) {
	arr := []any{
		mustJSON(`{"k":1}`),
		mustJSON(`{"k":2,"v":[1,2]}`),
		mustJSON(`{"k":3}`),
	}
	needle := mustJSON(`{"v":[1,2],"k":2}`) // same content, different key order
	if got := searchInArray(arr, needle, 0, 0); got != 1 {
		t.Fatalf("deep equality should find idx 1, got %d", got)
	}
}

func TestSearchInArrayNumericFlexible(t *testing.T) {
	arr := []any{
		json.Number("10"), float64(20), json.Number("30.0"),
	}
	if got := searchInArray(arr, float64(10), 0, 0); got != 0 {
		t.Fatalf("float-search-int should match, got %d", got)
	}
	if got := searchInArray(arr, json.Number("30"), 0, 0); got != 2 {
		t.Fatalf("int-search-float should match (30 == 30.0), got %d", got)
	}
}

func TestSearchInArrayNegativeStartWindow(t *testing.T) {
	arr := []any{"a", "b", "c", "d"}
	// negative start counts from end → -2 == idx 2
	if got := searchInArray(arr, "d", -2, 0); got != 3 {
		t.Fatalf("neg start: got %d, want 3", got)
	}
}

func mustJSON(raw string) any {
	d, err := New([]byte(raw))
	if err != nil {
		panic(err)
	}
	return d.Root
}
