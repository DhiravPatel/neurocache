// neurocache-cli — tiny interactive REPL that talks to the RESP server.
package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

func main() {
	addr := "localhost:6379"
	if v := os.Getenv("NEUROCACHE_ADDR"); v != "" {
		addr = v
	}
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "-h" {
		fmt.Println("usage: neurocache-cli [CMD ...]")
		fmt.Println("       NEUROCACHE_ADDR=host:port neurocache-cli")
		return
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)

	run := func(parts []string) {
		writeCmd(conn, parts)
		reply, err := readReply(r)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			return
		}
		fmt.Println(reply)
	}

	if len(args) > 0 {
		run(args)
		return
	}

	fmt.Printf("connected to %s — type 'help' or 'quit'\n", addr)
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("neurocache> ")
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "quit" || line == "exit" {
			return
		}
		if line == "help" {
			fmt.Println("commands: PING, SET, GET, DEL, EXISTS, INCR, KEYS, TTL, EXPIRE,")
			fmt.Println("          SEMANTIC_SET, SEMANTIC_GET, CACHE_LLM, CACHE_LLM_GET,")
			fmt.Println("          MEMORY_ADD, MEMORY_QUERY, INFO, FLUSHDB")
			continue
		}
		run(parseLine(line))
	}
}

func parseLine(s string) []string {
	// simple whitespace split with "double quoted" support
	var out []string
	var cur strings.Builder
	inQ := false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
		case r == ' ' && !inQ:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func writeCmd(w net.Conn, parts []string) {
	var b strings.Builder
	b.WriteString("*")
	b.WriteString(strconv.Itoa(len(parts)))
	b.WriteString("\r\n")
	for _, p := range parts {
		b.WriteString("$")
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteString("\r\n")
		b.WriteString(p)
		b.WriteString("\r\n")
	}
	_, _ = w.Write([]byte(b.String()))
}

func readReply(r *bufio.Reader) (string, error) {
	b, err := r.ReadByte()
	if err != nil {
		return "", err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	switch b {
	case '+':
		return line, nil
	case '-':
		return "(error) " + line, nil
	case ':':
		return "(integer) " + line, nil
	case '$':
		n, _ := strconv.Atoi(line)
		if n < 0 {
			return "(nil)", nil
		}
		buf := make([]byte, n)
		if _, err := r.Read(buf); err != nil {
			return "", err
		}
		_, _ = r.ReadString('\n')
		return string(buf), nil
	case '*':
		n, _ := strconv.Atoi(line)
		var out []string
		for i := 0; i < n; i++ {
			item, err := readReply(r)
			if err != nil {
				return "", err
			}
			out = append(out, fmt.Sprintf("%d) %s", i+1, item))
		}
		return strings.Join(out, "\n"), nil
	}
	return line, nil
}
