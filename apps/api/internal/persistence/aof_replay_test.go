package persistence

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestReplaySkipsUnapplicableCommands is the regression guard for the
// data-loss bug: a command that fails to apply (e.g. a runtime-only AI
// command the replayer doesn't implement) must be skipped, NOT abort the
// replay and discard the keyspace writes recorded after it.
func TestReplaySkipsUnapplicableCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "append.aof")
	aof, err := OpenAOF(path, FsyncAlways)
	if err != nil {
		t.Fatal(err)
	}
	// SET a, then an un-applicable command, then SET b. The second SET is the
	// one a buggy abort-on-error replay would lose.
	_ = aof.Append("SET", []string{"a", "1"})
	_ = aof.Append("QUOTA.ADMIT", []string{"p"})
	_ = aof.Append("SET", []string{"b", "2"})
	if err := aof.Close(); err != nil {
		t.Fatal(err)
	}

	var applied, skipped []string
	n, err := Replay(path,
		func(cmd string, args []string) error {
			if cmd == "QUOTA.ADMIT" {
				return errors.New("unknown command: QUOTA.ADMIT")
			}
			applied = append(applied, cmd+" "+args[0])
			return nil
		},
		func(cmd string, err error) { skipped = append(skipped, cmd) },
	)
	if err != nil {
		t.Fatalf("replay returned error: %v", err)
	}
	if n != 2 {
		t.Fatalf("applied count = %d, want 2", n)
	}
	if len(skipped) != 1 || skipped[0] != "QUOTA.ADMIT" {
		t.Fatalf("skipped = %v, want [QUOTA.ADMIT]", skipped)
	}
	// The critical assertion: the SET that FOLLOWED the bad command survived.
	if len(applied) != 2 || applied[0] != "SET a" || applied[1] != "SET b" {
		t.Fatalf("applied = %v, want [SET a, SET b] (no data loss after the skip)", applied)
	}
}
