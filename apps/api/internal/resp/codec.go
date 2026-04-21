package resp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// ─── reader ─────────────────────────────────────────────────────────────

// readArray reads a single RESP array of bulk strings. It also tolerates
// an inline command (space-separated text) for redis-cli interactive use.
func readArray(br *bufio.Reader) ([]string, error) {
	b, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	if b != '*' {
		_ = br.UnreadByte()
		line, err := readLine(br)
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return nil, nil
		}
		return splitInline(line), nil
	}
	line, err := readLine(br)
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(line)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		t, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		if t != '$' {
			return nil, errors.New("expected $ bulk")
		}
		ll, err := readLine(br)
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(ll)
		if err != nil {
			return nil, err
		}
		if size < 0 {
			out = append(out, "")
			continue
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(br, buf); err != nil {
			return nil, err
		}
		if _, err := readLine(br); err != nil {
			return nil, err
		}
		out = append(out, string(buf))
	}
	return out, nil
}

func readLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// splitInline tokenizes an inline command line, honoring simple double
// quotes so 'SET "hello world" 1' parses into three tokens.
func splitInline(line string) []string {
	out := []string{}
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// ─── writers ────────────────────────────────────────────────────────────

func writeSimple(w *bufio.Writer, s string) { _, _ = w.WriteString("+" + s + "\r\n") }
func writeError(w *bufio.Writer, s string)  { _, _ = w.WriteString("-ERR " + s + "\r\n") }
func writeTypedError(w *bufio.Writer, kind, msg string) {
	_, _ = w.WriteString("-" + kind + " " + msg + "\r\n")
}
func writeInt(w *bufio.Writer, n int64) { _, _ = w.WriteString(":" + strconv.FormatInt(n, 10) + "\r\n") }
func writeNil(w *bufio.Writer)          { _, _ = w.WriteString("$-1\r\n") }
func writeNilArray(w *bufio.Writer)     { _, _ = w.WriteString("*-1\r\n") }

func writeBulk(w *bufio.Writer, s string) {
	_, _ = w.WriteString("$" + strconv.Itoa(len(s)) + "\r\n")
	_, _ = w.WriteString(s + "\r\n")
}

func writeArray(w *bufio.Writer, items []string) {
	_, _ = w.WriteString("*" + strconv.Itoa(len(items)) + "\r\n")
	for _, it := range items {
		writeBulk(w, it)
	}
}

func writeFloat(w *bufio.Writer, f float64) {
	if math.IsInf(f, 1) {
		writeBulk(w, "inf")
		return
	}
	if math.IsInf(f, -1) {
		writeBulk(w, "-inf")
		return
	}
	writeBulk(w, strconv.FormatFloat(f, 'f', -1, 64))
}

// writeValue encodes an arbitrary Go value as RESP. Supported:
//
//	nil               -> nil bulk
//	string            -> bulk string
//	int / int64 / int32 -> integer
//	float64           -> bulk string (Redis convention for scores)
//	bool              -> integer 0/1
//	error             -> -ERR <msg>
//	[]any             -> nested array
//	[]string          -> flat bulk array
//
// Anything else falls back to fmt.Sprint().
func writeValue(w *bufio.Writer, v any) {
	switch x := v.(type) {
	case nil:
		writeNil(w)
	case string:
		writeBulk(w, x)
	case int:
		writeInt(w, int64(x))
	case int32:
		writeInt(w, int64(x))
	case int64:
		writeInt(w, x)
	case uint64:
		writeInt(w, int64(x))
	case float64:
		writeFloat(w, x)
	case bool:
		if x {
			writeInt(w, 1)
		} else {
			writeInt(w, 0)
		}
	case error:
		writeError(w, x.Error())
	case []string:
		writeArray(w, x)
	case []any:
		_, _ = w.WriteString("*" + strconv.Itoa(len(x)) + "\r\n")
		for _, it := range x {
			writeValue(w, it)
		}
	case [][]any:
		_, _ = w.WriteString("*" + strconv.Itoa(len(x)) + "\r\n")
		for _, it := range x {
			writeValue(w, it)
		}
	default:
		writeBulk(w, fmt.Sprint(x))
	}
}
