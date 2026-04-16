package resp

import (
	"bufio"
	"errors"
	"io"
	"strconv"
	"strings"
)

// readArray reads a single RESP array of bulk strings. It also tolerates an
// inline command (space-separated text) for redis-cli interactive use.
func readArray(br *bufio.Reader) ([]string, error) {
	b, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	if b != '*' {
		// inline command
		_ = br.UnreadByte()
		line, err := readLine(br)
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return nil, nil
		}
		return strings.Fields(line), nil
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
		if _, err := readLine(br); err != nil { // trailing CRLF
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

// encoders
func writeSimple(w *bufio.Writer, s string) { _, _ = w.WriteString("+" + s + "\r\n") }
func writeError(w *bufio.Writer, s string)  { _, _ = w.WriteString("-ERR " + s + "\r\n") }
func writeInt(w *bufio.Writer, n int64)     { _, _ = w.WriteString(":" + strconv.FormatInt(n, 10) + "\r\n") }
func writeNil(w *bufio.Writer)              { _, _ = w.WriteString("$-1\r\n") }

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
