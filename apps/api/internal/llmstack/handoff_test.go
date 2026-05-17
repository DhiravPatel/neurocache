package llmstack

import (
	"testing"
	"time"
)

func TestHandoffSpawnAndReturn(t *testing.T) {
	h := NewHandoffs()
	r, err := h.Spawn("parent", "research X", 0, 0, []string{"summary"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Return(r.ID, map[string]string{"summary": "ok"}); err != nil {
		t.Fatal(err)
	}
	st, ok := h.Status(r.ID)
	if !ok || st.Status != "returned" {
		t.Fatalf("status = %+v", st)
	}
	if st.Returned["summary"] != "ok" {
		t.Fatalf("returned = %v", st.Returned)
	}
}

func TestHandoffReturnValidatesRequiredKeys(t *testing.T) {
	h := NewHandoffs()
	r, _ := h.Spawn("p", "t", 0, 0, []string{"summary", "evidence"}, nil)
	if err := h.Return(r.ID, map[string]string{"summary": "x"}); err == nil {
		t.Fatal("missing required key should fail")
	}
}

func TestHandoffReturnIsOneShot(t *testing.T) {
	h := NewHandoffs()
	r, _ := h.Spawn("p", "t", 0, 0, nil, nil)
	h.Return(r.ID, map[string]string{"x": "1"})
	if err := h.Return(r.ID, map[string]string{"x": "2"}); err == nil {
		t.Fatal("double return should fail")
	}
}

func TestHandoffBudgetCancels(t *testing.T) {
	h := NewHandoffs()
	r, _ := h.Spawn("p", "t", 100, 0, nil, nil)
	h.ReportUsage(r.ID, 60)
	h.ReportUsage(r.ID, 60) // 120 > 100
	st, _ := h.Status(r.ID)
	if st.Status != "cancelled" || st.CancelReason != "budget exhausted" {
		t.Fatalf("budget enforcement: %+v", st)
	}
}

func TestHandoffDeadlineCancels(t *testing.T) {
	h := NewHandoffs()
	r, _ := h.Spawn("p", "t", 0, 10*time.Millisecond, nil, nil)
	time.Sleep(20 * time.Millisecond)
	st, _ := h.Status(r.ID)
	if st.Status != "cancelled" || st.CancelReason != "deadline exceeded" {
		t.Fatalf("deadline enforcement: %+v", st)
	}
}

func TestHandoffJoinBlocks(t *testing.T) {
	h := NewHandoffs()
	r, _ := h.Spawn("p", "t", 0, 0, nil, nil)
	go func() {
		time.Sleep(20 * time.Millisecond)
		h.Return(r.ID, map[string]string{"x": "1"})
	}()
	st, _ := h.Join(r.ID, 200*time.Millisecond)
	if st.Status != "returned" {
		t.Fatalf("join did not wait: %+v", st)
	}
}

func TestHandoffJoinTimeout(t *testing.T) {
	h := NewHandoffs()
	r, _ := h.Spawn("p", "t", 0, 0, nil, nil)
	st, _ := h.Join(r.ID, 30*time.Millisecond)
	if st.Status != "pending" {
		t.Fatalf("join should return pending on timeout: %+v", st)
	}
}

func TestHandoffCancel(t *testing.T) {
	h := NewHandoffs()
	r, _ := h.Spawn("p", "t", 0, 0, nil, nil)
	n, _ := h.Cancel(r.ID, "user aborted")
	if n != 1 {
		t.Fatal("cancel should drop 1")
	}
	st, _ := h.Status(r.ID)
	if st.Status != "cancelled" || st.CancelReason != "user aborted" {
		t.Fatalf("cancel: %+v", st)
	}
	// Re-cancelling a non-pending handoff is a no-op
	n2, _ := h.Cancel(r.ID, "again")
	if n2 != 0 {
		t.Fatal("re-cancel should be 0")
	}
}

func TestHandoffListByParent(t *testing.T) {
	h := NewHandoffs()
	h.Spawn("p1", "t", 0, 0, nil, nil)
	h.Spawn("p1", "t", 0, 0, nil, nil)
	h.Spawn("p2", "t", 0, 0, nil, nil)
	if len(h.List("p1")) != 2 {
		t.Fatal("parent filter wrong")
	}
	if len(h.List("")) != 3 {
		t.Fatal("no filter wrong")
	}
}

func TestHandoffForget(t *testing.T) {
	h := NewHandoffs()
	r, _ := h.Spawn("p", "t", 0, 0, nil, nil)
	if h.Forget(r.ID) != 1 {
		t.Fatal("forget")
	}
	if h.Forget("ALL") != 0 {
		t.Fatal("empty forget all")
	}
}

func TestHandoffStats(t *testing.T) {
	h := NewHandoffs()
	r, _ := h.Spawn("p", "t", 0, 0, nil, nil)
	h.Return(r.ID, map[string]string{"x": "1"})
	r2, _ := h.Spawn("p", "t", 100, 0, nil, nil)
	h.ReportUsage(r2.ID, 200)
	s := h.Stats()
	if s.TotalSpawns != 2 || s.TotalReturns != 1 || s.TotalCancels != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestHandoffRejectsBadInput(t *testing.T) {
	h := NewHandoffs()
	if _, err := h.Spawn("", "t", 0, 0, nil, nil); err == nil {
		t.Fatal("empty parent should fail")
	}
	if _, err := h.Spawn("p", "", 0, 0, nil, nil); err == nil {
		t.Fatal("empty task should fail")
	}
	if _, err := h.Spawn("p", "t", -1, 0, nil, nil); err == nil {
		t.Fatal("negative budget should fail")
	}
	if err := h.Return("ghost", map[string]string{"x": "1"}); err == nil {
		t.Fatal("unknown id should fail")
	}
}

func TestHandoffJoinUnknown(t *testing.T) {
	h := NewHandoffs()
	if _, ok := h.Join("ghost", 5*time.Millisecond); ok {
		t.Fatal("unknown join should fail")
	}
}
