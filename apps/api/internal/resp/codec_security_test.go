package resp

import (
	"bufio"
	"strconv"
	"strings"
	"testing"
)

// TestReadArrayRejectsOversized locks in the DoS caps: a giant bulk-string
// length, a giant array count, and an overflowing length digit-run must
// all be refused at parse time (errProtocol) rather than allocating.
func TestReadArrayRejectsOversized(t *testing.T) {
	cases := []struct {
		name, frame string
	}{
		{"huge bulk len", "*1\r\n$2000000000\r\n"},
		{"over bulk cap", "*1\r\n$536870913\r\n"}, // maxBulkLen+1
		{"huge array count", "*2000000000\r\n"},
		{"over multibulk cap", "*1048577\r\n"}, // maxMultiBulk+1
		{"overflow digits", "*1\r\n$9999999999999999999999999999999999\r\n"},
	}
	for _, c := range cases {
		br := bufio.NewReader(strings.NewReader(c.frame))
		if _, err := readArray(br); err == nil {
			t.Errorf("%s: expected a protocol error, got nil", c.name)
		}
	}
}

// TestReadArrayAcceptsNormal makes sure the caps didn't break ordinary
// commands, including a legitimately largish (1 MiB) value.
func TestReadArrayAcceptsNormal(t *testing.T) {
	big := strings.Repeat("x", 1024*1024)
	frame := "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$" +
		strconv.Itoa(len(big)) + "\r\n" + big + "\r\n"
	br := bufio.NewReader(strings.NewReader(frame))
	parts, err := readArray(br)
	if err != nil {
		t.Fatalf("normal SET frame rejected: %v", err)
	}
	if len(parts) != 3 || parts[0] != "SET" || parts[2] != big {
		t.Fatalf("parse mismatch: %d parts", len(parts))
	}
}

// TestRedactSecrets proves passwords never reach the SLOWLOG / MONITOR
// sinks, while ordinary commands pass through untouched (and unallocated).
func TestRedactSecrets(t *testing.T) {
	// AUTH password
	got := redactSecrets("AUTH", []string{"AUTH", "hunter2"})
	if got[1] != "(redacted)" {
		t.Errorf("AUTH password not redacted: %v", got)
	}
	// AUTH user password
	got = redactSecrets("AUTH", []string{"AUTH", "alice", "hunter2"})
	if got[1] != "(redacted)" || got[2] != "(redacted)" {
		t.Errorf("AUTH user/pass not redacted: %v", got)
	}
	// HELLO ... AUTH user pass
	got = redactSecrets("HELLO", []string{"HELLO", "3", "AUTH", "alice", "hunter2"})
	if got[3] != "(redacted)" || got[4] != "(redacted)" {
		t.Errorf("HELLO AUTH not redacted: %v", got)
	}
	// Non-sensitive command: returned unchanged, same backing array (no alloc).
	in := []string{"SET", "k", "v"}
	out := redactSecrets("SET", in)
	if &in[0] != &out[0] {
		t.Error("non-sensitive command should not be copied")
	}
}
