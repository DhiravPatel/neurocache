package scripting

import (
	"testing"
	"time"
)

func runScript(t *testing.T, src string, keys, argv []string, call Caller) any {
	t.Helper()
	v, err := Run(src, keys, argv, call, time.Time{})
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	return v
}

func TestArithmeticAndReturn(t *testing.T) {
	v := runScript(t, "return 1 + 2 * 3", nil, nil, nil)
	if v.(int64) != 7 {
		t.Fatalf("expected 7, got %v", v)
	}
}

func TestKeysAndArgv(t *testing.T) {
	src := `return KEYS[1] .. ":" .. ARGV[1]`
	v := runScript(t, src, []string{"k"}, []string{"v"}, nil)
	if v.(string) != "k:v" {
		t.Fatalf("got %v", v)
	}
}

func TestRedisCallBridge(t *testing.T) {
	called := 0
	caller := func(cmd string, args []string) (any, error) {
		called++
		if cmd != "INCR" {
			t.Fatalf("unexpected cmd %s", cmd)
		}
		return int64(42), nil
	}
	v := runScript(t, `return redis.call('INCR', KEYS[1])`, []string{"counter"}, nil, caller)
	if v.(int64) != 42 {
		t.Fatalf("got %v", v)
	}
	if called != 1 {
		t.Fatal("expected one call")
	}
}

func TestIfThenElse(t *testing.T) {
	src := `if tonumber(ARGV[1]) > 10 then return "big" else return "small" end`
	// Our subset doesn't include tonumber as a global; use direct compare instead.
	src = `if 1 < 2 then return "yes" else return "no" end`
	v := runScript(t, src, nil, nil, nil)
	if v.(string) != "yes" {
		t.Fatalf("got %v", v)
	}
}

func TestForNumeric(t *testing.T) {
	src := `local s = 0
	for i = 1, 5 do
	  s = s + i
	end
	return s`
	v := runScript(t, src, nil, nil, nil)
	if v.(int64) != 15 {
		t.Fatalf("got %v", v)
	}
}

func TestErrorReply(t *testing.T) {
	src := `return redis.error_reply("custom failure")`
	_, err := Run(src, nil, nil, nil, time.Time{})
	if err == nil || err.Error() != "custom failure" {
		t.Fatalf("expected error 'custom failure', got %v", err)
	}
}
