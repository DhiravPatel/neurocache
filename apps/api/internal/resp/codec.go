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
var errProtocol = errors.New("ERR Protocol error")

const (
	// maxBulkLen caps a single RESP bulk string ($N), mirroring Redis's
	// proto-max-bulk-len (512 MiB). Without it an unauthenticated client
	// could send "$2000000000\r\n" and the parser would make([]byte, 2e9)
	// before a single payload byte arrives — an instant out-of-memory DoS
	// off one tiny packet. The cap also bounds readRESPInt so the digit
	// accumulator can never integer-overflow.
	maxBulkLen = 512 * 1024 * 1024
	// maxMultiBulk caps a RESP array's element count (*N), mirroring
	// Redis's 1M multibulk limit, so "*2000000000\r\n" can't allocate a
	// two-billion-entry slice.
	maxMultiBulk = 1024 * 1024
)

// readRESPInt reads a CRLF-terminated decimal integer directly from the
// reader, byte by byte, without allocating an intermediate string (unlike
// readLine + strconv.Atoi). The leading type byte ('*' / '$') must already
// be consumed. A leading '-' yields a negative value (used for the `$-1`
// null bulk and `*-1` null array sentinels). This runs once for the array
// header, once per bulk-length header, and is the bulk of the per-command
// parse-alloc reduction (each of those was previously a throwaway string).
func readRESPInt(br *bufio.Reader) (int, error) {
	n := 0
	neg := false
	any := false
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		switch {
		case b == '\r':
			b2, err := br.ReadByte()
			if err != nil {
				return 0, err
			}
			if b2 != '\n' {
				return 0, errProtocol
			}
			if neg {
				return -1, nil
			}
			if !any {
				return 0, errProtocol
			}
			return n, nil
		case b == '-' && !any && !neg:
			neg = true
		case b >= '0' && b <= '9':
			any = true
			n = n*10 + int(b-'0')
			// Bound the accumulator at the largest legal length. This
			// both rejects oversized declarations early and makes the
			// `n*10` step impossible to overflow (n stays < 2^30).
			if n > maxBulkLen {
				return 0, errProtocol
			}
		default:
			return 0, errProtocol
		}
	}
}

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
	n, err := readRESPInt(br)
	if err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, nil // empty or null array — nothing to dispatch
	}
	if n > maxMultiBulk {
		return nil, errProtocol
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
		size, err := readRESPInt(br)
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
		// Discard the trailing CRLF in place — readLine here would have
		// allocated a throwaway string per argument.
		if _, err := br.Discard(2); err != nil {
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

// redactSecrets returns a copy of parts with credential arguments
// masked, for the SLOWLOG / MONITOR sinks that echo raw command args. It
// returns the original slice unchanged (no allocation) when there is
// nothing sensitive — the overwhelmingly common case. cmd must already
// be upper-cased.
func redactSecrets(cmd string, parts []string) []string {
	switch cmd {
	case "AUTH":
		// AUTH password  |  AUTH username password — mask every arg.
		out := append([]string(nil), parts...)
		for i := 1; i < len(out); i++ {
			out[i] = "(redacted)"
		}
		return out
	case "HELLO":
		// HELLO [proto] [AUTH user pass] [SETNAME name] — mask the two
		// tokens after an AUTH word.
		for i := 1; i+1 < len(parts); i++ {
			if strings.EqualFold(parts[i], "AUTH") {
				out := append([]string(nil), parts...)
				if i+1 < len(out) {
					out[i+1] = "(redacted)"
				}
				if i+2 < len(out) {
					out[i+2] = "(redacted)"
				}
				return out
			}
		}
	}
	return parts
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
	// Emit the whole "$<len>\r\n" header in ONE bufio.Write instead of the
	// three calls (WriteByte + Write + WriteString) it took before. Each
	// bufio call is a bounds-check + copy; collection replies write one
	// bulk per element (LRANGE/SMEMBERS/HGETALL/ZRANGE), so trimming
	// 5 calls → 3 per element measurably cuts the serialization slice.
	var hdr [24]byte
	hdr[0] = '$'
	n := len(strconv.AppendInt(hdr[1:1], int64(len(s)), 10))
	hdr[1+n] = '\r'
	hdr[2+n] = '\n'
	_, _ = w.Write(hdr[:3+n])
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
