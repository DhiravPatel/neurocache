package llmstack

import (
	"testing"
)

func TestBlastRecordAndReport(t *testing.T) {
	b := NewBlastRadius()
	b.Set("prompt", "v4")
	b.Record("prompt", "v5", "acme", "u1")
	b.Record("prompt", "v5", "acme", "u2")
	b.Record("prompt", "v5", "globex", "u3")
	r, ok := b.Report("prompt", "v5")
	if !ok {
		t.Fatal("report missing")
	}
	if r.ExposedUsers != 3 || r.ExposedTenants != 2 {
		t.Fatalf("report = %+v", r)
	}
	if r.PerTenant["acme"] != 2 {
		t.Fatalf("per_tenant = %v", r.PerTenant)
	}
}

func TestBlastDedupsUsers(t *testing.T) {
	b := NewBlastRadius()
	b.Record("p", "v5", "t", "u1")
	b.Record("p", "v5", "t", "u1") // dup
	r, _ := b.Report("p", "v5")
	if r.ExposedUsers != 1 {
		t.Fatalf("user dedup failed: %d", r.ExposedUsers)
	}
}

func TestBlastRevertSwingsCurrent(t *testing.T) {
	b := NewBlastRadius()
	b.Set("p", "v4")
	b.Record("p", "v5", "t", "u")
	r, err := b.Revert("p", "v5", "v4", "bad output")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Reverted || r.RevertReason != "bad output" {
		t.Fatalf("revert: %+v", r)
	}
	st, _ := b.Status("p")
	if st.CurrentVersion != "v4" {
		t.Fatalf("current_version = %s", st.CurrentVersion)
	}
}

func TestBlastRevertRejectsUnknown(t *testing.T) {
	b := NewBlastRadius()
	if _, err := b.Revert("p", "v99", "v4", ""); err == nil {
		t.Fatal("revert of unknown version should fail")
	}
}

func TestBlastRevertRejectsSelf(t *testing.T) {
	b := NewBlastRadius()
	b.Record("p", "v5", "t", "u")
	if _, err := b.Revert("p", "v5", "v5", ""); err == nil {
		t.Fatal("revert to self should fail")
	}
}

func TestBlastReportAfterRevertStillWorks(t *testing.T) {
	b := NewBlastRadius()
	b.Record("p", "v5", "t", "u")
	b.Revert("p", "v5", "v4", "")
	r, ok := b.Report("p", "v5")
	if !ok || !r.Reverted {
		t.Fatalf("post-revert report: %+v", r)
	}
	if r.ExposedUsers != 1 {
		t.Fatalf("data not preserved: %+v", r)
	}
}

func TestBlastStatusListsVersions(t *testing.T) {
	b := NewBlastRadius()
	b.Set("p", "v4")
	b.Record("p", "v5", "t", "u")
	b.Record("p", "v6", "t", "u")
	st, _ := b.Status("p")
	if len(st.Versions) != 2 {
		t.Fatalf("versions = %v", st.Versions)
	}
}

func TestBlastForget(t *testing.T) {
	b := NewBlastRadius()
	b.Set("p1", "v")
	b.Set("p2", "v")
	if b.Forget("p1") != 1 {
		t.Fatal("forget p1")
	}
	if b.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestBlastStats(t *testing.T) {
	b := NewBlastRadius()
	b.Record("p", "v5", "t", "u")
	b.Revert("p", "v5", "v4", "")
	s := b.Stats()
	if s.TotalRecords != 1 || s.TotalReverts != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestBlastRejectsBadInput(t *testing.T) {
	b := NewBlastRadius()
	if err := b.Set("", "v"); err == nil {
		t.Fatal("empty feature should fail")
	}
	if err := b.Record("p", "", "t", "u"); err == nil {
		t.Fatal("empty version should fail")
	}
	if err := b.Record("p", "v", "", "u"); err == nil {
		t.Fatal("empty tenant should fail")
	}
	if err := b.Record("p", "v", "t", ""); err == nil {
		t.Fatal("empty user should fail")
	}
}
