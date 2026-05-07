package resp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unsafe"
)

// bytesToStringNoCopy reinterprets a byte slice as a string without
// copying the backing array. Caller MUST guarantee the slice is never
// mutated after this call — otherwise the resulting string violates
// Go's string-immutability contract. We use it on freshly-allocated
// read buffers in readArray (callers never mutate the returned string,
// and the buffer escapes into the returned []string).
func bytesToStringNoCopy(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// asciiUpper is a zero-alloc ASCII-only fast path for command names.
// Real-world traffic sends commands in lowercase ("set", "get") or
// uppercase ("SET", "GET"); we walk the string once, and only allocate
// when at least one lowercase byte is present. The previous form
// (`strings.ToUpper(parts[0])`) always allocated a fresh string —
// expensive on a metric called per command per dispatch + record path.
//
// Falls through to strings.ToUpper for non-ASCII inputs (rare in
// command names but possible in unit tests).
func asciiUpper(s string) string {
	hasLower := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			hasLower = true
			break
		}
		if c >= 0x80 {
			return strings.ToUpper(s)
		}
	}
	if !hasLower {
		// Already upper (or has no letters) — return as-is, no alloc.
		return s
	}
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x80 {
			return strings.ToUpper(s)
		}
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		b[i] = c
	}
	return bytesToStringNoCopy(b)
}

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
		// `string(buf)` would copy the bytes — for a 100 KiB SET that's
		// 100 KiB of duplicated allocation per arg. We just allocated
		// `buf` here, never reuse it, and never mutate it again, so
		// the cast is safe. unsafe.String reuses the backing array.
		out = append(out, bytesToStringNoCopy(buf))
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

// All writers stream directly into the bufio.Writer without
// intermediate string concatenation. The previous form ("$" + N + "\r\n"
// or s + "\r\n") allocated a fresh string per call — a real GC pressure
// problem for large values (100 KiB SET allocated 200+ KiB per call:
// once for the buf in readArray, once for s+"\r\n" here). Direct
// WriteString calls skip both allocations.

const crlf = "\r\n"

func writeSimple(w *bufio.Writer, s string) {
	_ = w.WriteByte('+')
	_, _ = w.WriteString(s)
	_, _ = w.WriteString(crlf)
}

func writeError(w *bufio.Writer, s string) {
	_, _ = w.WriteString("-ERR ")
	_, _ = w.WriteString(s)
	_, _ = w.WriteString(crlf)
}

func writeTypedError(w *bufio.Writer, kind, msg string) {
	_ = w.WriteByte('-')
	_, _ = w.WriteString(kind)
	_ = w.WriteByte(' ')
	_, _ = w.WriteString(msg)
	_, _ = w.WriteString(crlf)
}

func writeInt(w *bufio.Writer, n int64) {
	_ = w.WriteByte(':')
	// AppendInt writes into a stack-allocated buffer instead of
	// allocating a new string via FormatInt+concat.
	var buf [20]byte
	_, _ = w.Write(strconv.AppendInt(buf[:0], n, 10))
	_, _ = w.WriteString(crlf)
}

func writeNil(w *bufio.Writer)      { _, _ = w.WriteString("$-1\r\n") }
func writeNilArray(w *bufio.Writer) { _, _ = w.WriteString("*-1\r\n") }

func writeBulk(w *bufio.Writer, s string) {
	_ = w.WriteByte('$')
	var buf [20]byte
	_, _ = w.Write(strconv.AppendInt(buf[:0], int64(len(s)), 10))
	_, _ = w.WriteString(crlf)
	_, _ = w.WriteString(s) // streamed directly — no s+"\r\n" allocation
	_, _ = w.WriteString(crlf)
}

func writeArray(w *bufio.Writer, items []string) {
	_ = w.WriteByte('*')
	var buf [20]byte
	_, _ = w.Write(strconv.AppendInt(buf[:0], int64(len(items)), 10))
	_, _ = w.WriteString(crlf)
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
		// Stream the array header byte-by-byte instead of allocating
		// "*"+itoa+"\r\n" (3 allocs per call). Same shape as writeBulk.
		_ = w.WriteByte('*')
		var buf [20]byte
		_, _ = w.Write(strconv.AppendInt(buf[:0], int64(len(x)), 10))
		_, _ = w.WriteString(crlf)
		for _, it := range x {
			writeValue(w, it)
		}
	case [][]any:
		_ = w.WriteByte('*')
		var buf [20]byte
		_, _ = w.Write(strconv.AppendInt(buf[:0], int64(len(x)), 10))
		_, _ = w.WriteString(crlf)
		for _, it := range x {
			writeValue(w, it)
		}
	default:
		writeBulk(w, fmt.Sprint(x))
	}
}
