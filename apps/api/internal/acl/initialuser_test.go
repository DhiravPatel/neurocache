package acl

import "testing"

// TestInitialUserRequiresAuth locks in the requirepass-enforcement fix: a
// fresh connection must start UNAUTHENTICATED (nil) once the default user
// has a password, so the dispatch gate demands AUTH. Before the fix every
// connection started as the privileged default user and the password was
// never enforced.
func TestInitialUserRequiresAuth(t *testing.T) {
	m := NewManager(nil)

	// No password configured -> open default user (dev default).
	if u := m.InitialUser(); u == nil {
		t.Fatal("with no password, a connection should start as the default user")
	}

	// requirepass set -> a fresh connection is unauthenticated.
	m.SetRequirePass("topsecret")
	if u := m.InitialUser(); u != nil {
		t.Fatalf("with a password set, a connection must start unauthenticated (got user %q)", u.Name)
	}

	// And AUTH still resolves the user with the right password.
	if _, err := m.Authenticate("", "topsecret"); err != nil {
		t.Fatalf("AUTH with correct password should succeed: %v", err)
	}
	if _, err := m.Authenticate("", "wrong"); err == nil {
		t.Fatal("AUTH with wrong password must fail")
	}
}
