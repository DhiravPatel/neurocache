package acl

import "testing"

func TestDefaultUserAllowsEverything(t *testing.T) {
	m := NewManager(nil)
	u := m.DefaultUser()
	if u == nil {
		t.Fatal("default user missing")
	}
	if err := m.Allowed(u, "GET", []string{"k"}, nil); err != nil {
		t.Fatalf("default user denied GET: %v", err)
	}
	if err := m.Allowed(u, "FLUSHALL", nil, nil); err != nil {
		t.Fatalf("default user denied FLUSHALL: %v", err)
	}
}

func TestSetUserPermissionsAndDeny(t *testing.T) {
	m := NewManager(nil)
	if err := m.SetUser("alice", []string{"on", ">secret", "+@read", "-FLUSHALL", "~cache:*"}); err != nil {
		t.Fatalf("setuser: %v", err)
	}
	u, err := m.Authenticate("alice", "secret")
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	if err := m.Allowed(u, "GET", []string{"cache:1"}, nil); err != nil {
		t.Fatalf("expected allow: %v", err)
	}
	if err := m.Allowed(u, "GET", []string{"other:1"}, nil); err == nil {
		t.Fatal("expected key-pattern denial")
	}
	if err := m.Allowed(u, "FLUSHALL", nil, nil); err == nil {
		t.Fatal("expected explicit -FLUSHALL denial")
	}
}

func TestWrongPassword(t *testing.T) {
	m := NewManager(nil)
	_ = m.SetUser("bob", []string{"on", ">good"})
	if _, err := m.Authenticate("bob", "bad"); err == nil {
		t.Fatal("expected wrongpass")
	}
	if got := m.Log(0); len(got) == 0 {
		t.Fatal("expected audit entry on auth-fail")
	}
}

func TestRequirePassDowngrade(t *testing.T) {
	m := NewManager(nil)
	m.SetRequirePass("topsecret")
	if _, err := m.Authenticate("", "topsecret"); err != nil {
		t.Fatalf("legacy AUTH should accept: %v", err)
	}
	if _, err := m.Authenticate("", "wrong"); err == nil {
		t.Fatal("legacy AUTH should reject wrong password")
	}
}
