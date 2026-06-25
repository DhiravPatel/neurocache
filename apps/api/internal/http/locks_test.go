package http

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLockLifecycleHTTP(t *testing.T) {
	h := testHandlers(t)

	acquire := func(name, owner string, ttl int) map[string]any {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/locks/"+name+"/acquire",
			strings.NewReader(`{"owner":"`+owner+`","ttl_ms":`+itoa(ttl)+`}`))
		req.SetPathValue("name", name)
		h.acquireLock(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}

	// First acquire wins and returns a fencing token.
	a := acquire("job", "worker-a", 5000)
	if a["acquired"] != true {
		t.Fatalf("first acquire should succeed: %v", a)
	}
	tok1 := a["token"].(float64)
	if tok1 <= 0 {
		t.Fatalf("expected positive fencing token, got %v", tok1)
	}

	// A different owner is refused while the lock is held.
	b := acquire("job", "worker-b", 5000)
	if b["acquired"] != false || b["token"].(float64) != 0 {
		t.Fatalf("contended acquire should fail: %v", b)
	}

	// CHECK reflects the holder.
	rec := httptest.NewRecorder()
	creq := httptest.NewRequest("GET", "/api/locks/job", nil)
	creq.SetPathValue("name", "job")
	h.checkLock(rec, creq)
	var chk map[string]any
	json.Unmarshal(rec.Body.Bytes(), &chk)
	if chk["held"] != true || chk["owner"] != "worker-a" {
		t.Fatalf("check: %v", chk)
	}

	// LIST includes the held lock.
	rec = httptest.NewRecorder()
	h.listLocks(rec, httptest.NewRequest("GET", "/api/locks", nil))
	if !strings.Contains(rec.Body.String(), `"job"`) || !strings.Contains(rec.Body.String(), "worker-a") {
		t.Fatalf("list: %s", rec.Body.String())
	}

	// Re-acquire by the same owner is reentrant and bumps the fencing token.
	a2 := acquire("job", "worker-a", 5000)
	if a2["acquired"] != true || a2["token"].(float64) <= tok1 {
		t.Fatalf("reentrant acquire should bump token (%v → %v)", tok1, a2["token"])
	}

	// Release: wrong owner is a no-op, correct owner succeeds.
	release := func(owner string) bool {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/locks/job/release",
			strings.NewReader(`{"owner":"`+owner+`"}`))
		req.SetPathValue("name", "job")
		h.releaseLock(rec, req)
		var out map[string]bool
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out["released"]
	}
	if release("worker-b") {
		t.Fatal("release by non-owner must fail")
	}
	if !release("worker-a") {
		t.Fatal("release by owner must succeed")
	}

	// After release the lock is free.
	rec = httptest.NewRecorder()
	creq = httptest.NewRequest("GET", "/api/locks/job", nil)
	creq.SetPathValue("name", "job")
	h.checkLock(rec, creq)
	if !strings.Contains(rec.Body.String(), `"held":false`) {
		t.Fatalf("lock should be free after release: %s", rec.Body.String())
	}

	// ttl_ms <= 0 is rejected (no silent instant-expiry lock).
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/locks/x/acquire", strings.NewReader(`{"owner":"a","ttl_ms":0}`))
	req.SetPathValue("name", "x")
	h.acquireLock(rec, req)
	if rec.Code != 400 {
		t.Fatalf("ttl_ms=0 should be 400, got %d", rec.Code)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
