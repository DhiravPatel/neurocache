package modules

import (
	"errors"
	"testing"
)

// fakeEngine is a minimal EngineHandle for unit-testing the registry.
type fakeEngine struct {
	store map[string]any
}

func newFakeEngine() *fakeEngine { return &fakeEngine{store: map[string]any{}} }

func (f *fakeEngine) SetCustomValue(key string, _ TypeID, value any, _ int64) error {
	f.store[key] = value
	return nil
}
func (f *fakeEngine) GetCustomValue(key string, _ TypeID) (any, bool, error) {
	v, ok := f.store[key]
	return v, ok, nil
}
func (f *fakeEngine) DelCustomValue(key string) bool {
	if _, ok := f.store[key]; ok {
		delete(f.store, key)
		return true
	}
	return false
}
func (f *fakeEngine) Publish(string, string) int { return 0 }

func TestRegisterAvailableAndLoad(t *testing.T) {
	mod := Module{
		Name: "_test_a", Version: "0.1",
		Init: func(ctx *RegisterCtx) error {
			return ctx.RegisterCmd(Cmd{
				Name: "TEST.PING", Arity: 1,
				Run: func(c *Ctx, _ []string) error { c.Reply.SimpleString("OK"); return nil },
			})
		},
	}
	RegisterAvailable(mod)
	r := NewRegistry(newFakeEngine())
	if err := r.Load("_test_a"); err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := r.FindCmd("TEST.PING"); !ok {
		t.Fatal("TEST.PING should be registered")
	}
	infos := r.List()
	if len(infos) != 1 || infos[0].Name != "_test_a" {
		t.Fatalf("List wrong: %+v", infos)
	}
}

func TestDoubleLoadRejected(t *testing.T) {
	RegisterAvailable(Module{Name: "_test_b", Init: func(ctx *RegisterCtx) error { return nil }})
	r := NewRegistry(newFakeEngine())
	if err := r.Load("_test_b"); err != nil {
		t.Fatal(err)
	}
	if err := r.Load("_test_b"); err == nil {
		t.Fatal("second Load should error")
	}
}

func TestUnloadCleansCommands(t *testing.T) {
	RegisterAvailable(Module{
		Name: "_test_c",
		Init: func(ctx *RegisterCtx) error {
			return ctx.RegisterCmd(Cmd{Name: "C.PING", Run: func(c *Ctx, _ []string) error { return nil }})
		},
	})
	r := NewRegistry(newFakeEngine())
	if err := r.Load("_test_c"); err != nil {
		t.Fatal(err)
	}
	if err := r.Unload("_test_c"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.FindCmd("C.PING"); ok {
		t.Fatal("C.PING should be gone after Unload")
	}
}

func TestInitErrorAborts(t *testing.T) {
	RegisterAvailable(Module{
		Name: "_test_bad",
		Init: func(ctx *RegisterCtx) error { return errors.New("bad config") },
	})
	r := NewRegistry(newFakeEngine())
	if err := r.Load("_test_bad"); err == nil {
		t.Fatal("expected init failure")
	}
	if len(r.LoadedNames()) != 0 {
		t.Fatal("failed module should not appear loaded")
	}
}

func TestKeyPositionExtraction(t *testing.T) {
	cases := []struct {
		pos  KeyPosition
		args []string
		want []string
	}{
		{KeyAt(1), []string{"k", "v"}, []string{"k"}},
		{KeyRange(1, -1, 1), []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{KeyRange(1, -1, 2), []string{"k1", "v1", "k2", "v2"}, []string{"k1", "k2"}},
		{KeyNone, []string{"x"}, nil},
	}
	for _, tc := range cases {
		got := tc.pos.Keys(tc.args)
		if len(got) != len(tc.want) {
			t.Fatalf("Keys(%+v, %v) = %v, want %v", tc.pos, tc.args, got, tc.want)
		}
		for i, k := range got {
			if k != tc.want[i] {
				t.Fatalf("Keys[%d] = %q, want %q", i, k, tc.want[i])
			}
		}
	}
}
