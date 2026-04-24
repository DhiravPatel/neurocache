package replication

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Encode turns (cmd, args) into a RESP array frame. Used by the master
// to append propagated writes to the backlog + replicate them to
// connected replicas, and by the replica to frame commands during the
// handshake (PING, REPLCONF, PSYNC).
func Encode(cmd string, args []string) []byte {
	b := make([]byte, 0, 16+len(cmd)+8*len(args))
	b = append(b, '*')
	b = strconv.AppendInt(b, int64(1+len(args)), 10)
	b = append(b, '\r', '\n')
	b = append(b, '$')
	b = strconv.AppendInt(b, int64(len(cmd)), 10)
	b = append(b, '\r', '\n')
	b = append(b, cmd...)
	b = append(b, '\r', '\n')
	for _, a := range args {
		b = append(b, '$')
		b = strconv.AppendInt(b, int64(len(a)), 10)
		b = append(b, '\r', '\n')
		b = append(b, a...)
		b = append(b, '\r', '\n')
	}
	return b
}

// ReadArray parses one RESP array from br. Used on the replica side to
// consume master → replica streaming commands.
func ReadArray(br *bufio.Reader) ([]string, error) {
	tag, err := br.ReadByte()
	if err != nil {
		return nil, err
	}
	if tag != '*' {
		// tolerate inline commands — some test utilities send them.
		_ = br.UnreadByte()
		line, err := readLine(br)
		if err != nil {
			return nil, err
		}
		return strings.Fields(line), nil
	}
	line, err := readLine(br)
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(line)
	if err != nil {
		return nil, fmt.Errorf("invalid array len: %s", line)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		t, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		if t != '$' {
			return nil, errors.New("expected bulk header")
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

// ReadReply parses one top-level RESP reply into a (kind, payload)
// pair. Used by the replica during handshake to observe +FULLRESYNC /
// +CONTINUE style replies as well as bulk-string RDB payloads.
func ReadReply(br *bufio.Reader) (kind byte, payload string, err error) {
	tag, err := br.ReadByte()
	if err != nil {
		return 0, "", err
	}
	switch tag {
	case '+', '-', ':':
		line, err := readLine(br)
		return tag, line, err
	case '$':
		ll, err := readLine(br)
		if err != nil {
			return 0, "", err
		}
		n, err := strconv.Atoi(ll)
		if err != nil {
			return 0, "", err
		}
		if n < 0 {
			return tag, "", nil
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(br, buf); err != nil {
			return 0, "", err
		}
		if _, err := readLine(br); err != nil {
			return 0, "", err
		}
		return tag, string(buf), nil
	case '*':
		// return the remaining line so callers know an array followed
		line, err := readLine(br)
		return tag, line, err
	}
	return 0, "", fmt.Errorf("unknown RESP tag: %q", tag)
}

func readLine(br *bufio.Reader) (string, error) {
	s, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(s, "\r\n"), nil
}
