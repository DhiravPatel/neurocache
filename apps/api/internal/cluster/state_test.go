package cluster

import "testing"

func TestSlotAssignmentTracksOwnership(t *testing.T) {
	st := NewState()
	a := NewNode("", "10.0.0.1", "6379", "16379", RoleMaster)
	b := NewNode("", "10.0.0.2", "6379", "16379", RoleMaster)
	st.Enable(a)
	st.AddNode(b)

	if _, err := st.AssignSlot(0, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AssignSlot(1, b.ID); err != nil {
		t.Fatal(err)
	}
	if owner := st.SlotOwner(0); owner == nil || owner.ID != a.ID {
		t.Fatal("slot 0 should belong to a")
	}
	if !a.HasSlot(0) || !b.HasSlot(1) {
		t.Fatal("node bitmaps should reflect ownership")
	}
	// reassign — the previous owner loses the bit
	if _, err := st.AssignSlot(0, b.ID); err != nil {
		t.Fatal(err)
	}
	if a.HasSlot(0) {
		t.Fatal("slot 0 should have been moved off a")
	}
	if !b.HasSlot(0) {
		t.Fatal("slot 0 should now belong to b")
	}
}

func TestRouteVerdicts(t *testing.T) {
	st := NewState()
	me := NewNode("", "127.0.0.1", "6379", "16379", RoleMaster)
	other := NewNode("", "10.0.0.2", "6379", "16379", RoleMaster)
	st.Enable(me)
	st.AddNode(other)
	// give every slot to "other" first
	for s := 0; s < SlotCount; s++ {
		_, _ = st.AssignSlot(s, other.ID)
	}
	// MOVED for any key (other owns everything)
	v := st.Route([]string{"x"}, false)
	if v.Redirect == 0 {
		t.Fatal("expected MOVED — we don't own any slot")
	}
	// now take over the key's slot
	slot := KeySlot("x")
	_, _ = st.AssignSlot(slot, me.ID)
	v = st.Route([]string{"x"}, false)
	if v.Redirect != 0 {
		t.Fatalf("expected OK, got %v", v)
	}
	// CROSSSLOT for keys in different slots
	other2 := KeySlot("y")
	if slot == other2 {
		t.Skip("hash collision; skipping cross-slot probe")
	}
	v = st.Route([]string{"x", "y"}, false)
	if v.Redirect == 0 {
		t.Fatal("expected CROSSSLOT")
	}
}

func TestForgetCannotDropSelf(t *testing.T) {
	st := NewState()
	me := NewNode("", "127.0.0.1", "6379", "16379", RoleMaster)
	st.Enable(me)
	if st.ForgetNode(me.ID) {
		t.Fatal("ForgetNode must refuse to drop ourselves")
	}
}
