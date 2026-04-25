package resp

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/replication"
)

// replicaofCmd handles `REPLICAOF host port` and `REPLICAOF NO ONE`
// (also registered as SLAVEOF for backwards compatibility).
func (c *conn) replicaofCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'replicaof'")
		return
	}
	if strings.EqualFold(args[0], "NO") && strings.EqualFold(args[1], "ONE") {
		c.eng.PromoteToMaster()
		writeSimple(c.bw, "OK")
		return
	}
	c.eng.FollowMaster(args[0], args[1])
	writeSimple(c.bw, "OK")
}

// roleCmd returns the standard ROLE reply:
//
//	master: ["master", <offset>, [[host, port, off], ...]]
//	replica: ["slave", host, port, <link-state>, <offset>]
func (c *conn) roleCmd() {
	st := c.eng.Replication
	if st.Role() == replication.RoleReplica {
		host, port := st.Master()
		fmt.Fprintf(c.bw, "*5\r\n")
		writeBulk(c.bw, "slave")
		writeBulk(c.bw, host)
		writeBulk(c.bw, port)
		writeBulk(c.bw, st.LinkState().String())
		writeInt(c.bw, st.MasterOffset())
		return
	}
	replicas := st.Replicas()
	fmt.Fprintf(c.bw, "*3\r\n")
	writeBulk(c.bw, "master")
	writeInt(c.bw, st.Offset())
	fmt.Fprintf(c.bw, "*%d\r\n", len(replicas))
	for _, r := range replicas {
		addr := r.Conn.RemoteAddr().String()
		host, _, _ := splitAddr(addr)
		fmt.Fprintf(c.bw, "*3\r\n")
		writeBulk(c.bw, host)
		writeBulk(c.bw, r.ListenPort)
		writeInt(c.bw, r.AckOffset.Load())
	}
}

// waitCmd implements WAIT numreplicas timeout-ms. Returns the number of
// replicas that had caught up to the master's current offset by the
// time the deadline expired.
func (c *conn) waitCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'wait'")
		return
	}
	need, err := strconv.Atoi(args[0])
	if err != nil {
		writeError(c.bw, "value is not an integer")
		return
	}
	timeoutMs, err := strconv.Atoi(args[1])
	if err != nil {
		writeError(c.bw, "timeout is not an integer")
		return
	}
	st := c.eng.Replication
	target := st.Offset()
	deadline := time.Time{}
	if timeoutMs > 0 {
		deadline = time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	}
	// Send a REPLCONF GETACK * to every replica so their ACKs are fresh.
	ping := replication.Encode("REPLCONF", []string{"GETACK", "*"})
	for _, r := range st.Replicas() {
		_ = r.Send(ping)
	}
	for {
		n := st.ConnectedReplicasAtOffset(target)
		if n >= need {
			writeInt(c.bw, int64(n))
			return
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			writeInt(c.bw, int64(n))
			return
		}
		// Short sleep — WAIT is deliberately coarse.
		time.Sleep(25 * time.Millisecond)
	}
}

// failoverCmd implements FAILOVER [TO host port] [TIMEOUT ms] [FORCE].
// For single-node deployments we just flip roles; real Redis sentinel
// semantics live at the orchestrator layer.
func (c *conn) failoverCmd(args []string) {
	var toHost, toPort string
	force := false
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "TO":
			if i+2 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			toHost, toPort = args[i+1], args[i+2]
			i += 2
		case "TIMEOUT":
			i++ // ignored — best-effort
		case "ABORT":
			c.eng.PromoteToMaster()
			writeSimple(c.bw, "OK")
			return
		case "FORCE":
			force = true
		}
	}
	_ = force
	if toHost != "" {
		// On the master: instruct a replica to take over. We don't
		// orchestrate the swap — the caller issues REPLICAOF on the
		// chosen replica first. This command just records intent and
		// flips this node into replica mode following the promoted one.
		c.eng.FollowMaster(toHost, toPort)
	} else {
		// Plain FAILOVER on the replica promotes itself.
		c.eng.PromoteToMaster()
	}
	writeSimple(c.bw, "OK")
}

// psyncCmd handles PSYNC <replid> <offset> from a connecting replica.
// Sends back +FULLRESYNC <replid> <offset>, then streams the current
// RDB snapshot as a bulk frame, then releases the connection into the
// streaming state so the master's fan-out loop can write to it.
func (c *conn) psyncCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'psync'")
		return
	}
	st := c.eng.Replication
	replid := args[0]
	offset, _ := strconv.ParseInt(args[1], 10, 64)

	partial := replid != "?" &&
		(replid == st.ReplID() || replid == st.PrevReplID()) &&
		c.eng.Backlog.Contains(offset)

	if partial {
		if _, err := c.bw.Write(replication.ContinueReply(st.ReplID())); err != nil {
			return
		}
		_ = c.bw.Flush()
		// Stream whatever's in the backlog from offset onwards.
		remaining := st.Offset() - offset
		if remaining > 0 {
			buf, ok := c.eng.Backlog.Slice(offset, remaining)
			if ok {
				_, _ = c.bw.Write(buf)
				_ = c.bw.Flush()
			}
		}
	} else {
		snapshot := c.eng.RDBBlob()
		if _, err := c.bw.Write(replication.FullResyncReply(st.ReplID(), st.Offset())); err != nil {
			return
		}
		if _, err := c.bw.Write(replication.BulkHeader(len(snapshot))); err != nil {
			return
		}
		if _, err := c.bw.Write(snapshot); err != nil {
			return
		}
		if _, err := c.bw.Write([]byte("\r\n")); err != nil {
			return
		}
		_ = c.bw.Flush()
	}

	// Promote this conn into a dedicated replica link. From here, the
	// master's fan-out loop owns writes; we must stop the per-command
	// RESP dispatcher from touching the socket.
	link := replication.NewReplicaLink(c.nc, c.br, c.bw)
	// Carry any metadata we gathered from earlier REPLCONF frames.
	link.ListenPort = c.replListenPort
	link.Capa = c.replCapa
	st.AddReplica(link)
	c.adoptedByMaster = link

	// Read heartbeats from the replica to feed WAIT + liveness.
	go c.eng.ConsumeReplicaHeartbeats(link)
}

// replconfCmd answers the handshake-time REPLCONF frames and, during
// streaming, records ACK offsets for WAIT.
func (c *conn) replconfCmd(args []string) {
	for i := 0; i+1 < len(args); i += 2 {
		switch strings.ToUpper(args[i]) {
		case "LISTENING-PORT":
			c.replListenPort = args[i+1]
		case "CAPA":
			c.replCapa = append(c.replCapa, args[i+1])
		case "ACK":
			if c.adoptedByMaster != nil {
				if off, err := strconv.ParseInt(args[i+1], 10, 64); err == nil {
					c.adoptedByMaster.AckOffset.Store(off)
					c.adoptedByMaster.LastAck.Store(time.Now().UnixNano())
				}
			}
		case "GETACK":
			// master asked replica to ACK — we're the master side, so
			// just record and move on. Replica side responds via the
			// ackLoop in the replication.Client.
		}
	}
	writeSimple(c.bw, "OK")
}

// splitAddr cracks a net.Addr string like "10.0.0.1:6379" into host/port.
func splitAddr(s string) (host, port string, ok bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

// silence unused import warnings if a later refactor drops uses.
var _ = bufio.NewReader
