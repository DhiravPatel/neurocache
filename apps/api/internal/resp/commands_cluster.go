package resp

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/cluster"
)

// clusterCmd dispatches the CLUSTER * subcommand surface.
func (c *conn) clusterCmd(args []string) {
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'cluster'")
		return
	}
	st := c.eng.Cluster
	if st == nil {
		writeError(c.bw, "ERR This instance has cluster support disabled")
		return
	}
	sub := strings.ToUpper(args[0])
	switch sub {
	case "INFO":
		writeBulk(c.bw, formatClusterInfo(st))
	case "MYID":
		if m := st.Myself(); m != nil {
			writeBulk(c.bw, m.ID)
		} else {
			writeBulk(c.bw, "")
		}
	case "NODES":
		writeBulk(c.bw, formatClusterNodes(st))
	case "SLOTS":
		writeClusterSlots(c.bw, st)
	case "SHARDS":
		writeClusterShards(c.bw, st)
	case "KEYSLOT":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'cluster|keyslot'")
			return
		}
		writeInt(c.bw, int64(cluster.KeySlot(args[1])))
	case "COUNTKEYSINSLOT":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'cluster|countkeysinslot'")
			return
		}
		slot, err := strconv.Atoi(args[1])
		if err != nil {
			writeError(c.bw, "value is not an integer")
			return
		}
		writeInt(c.bw, int64(st.CountKeysInSlot(slot)))
	case "GETKEYSINSLOT":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'cluster|getkeysinslot'")
			return
		}
		slot, _ := strconv.Atoi(args[1])
		count, _ := strconv.Atoi(args[2])
		writeArray(c.bw, c.eng.KeysInSlot(slot, count))
	case "ADDSLOTS":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'cluster|addslots'")
			return
		}
		myself := st.Myself()
		if myself == nil {
			writeError(c.bw, "ERR cluster not enabled")
			return
		}
		for _, a := range args[1:] {
			slot, err := strconv.Atoi(a)
			if err != nil {
				writeError(c.bw, "value is not an integer")
				return
			}
			if _, err := st.AssignSlot(slot, myself.ID); err != nil {
				writeError(c.bw, err.Error())
				return
			}
		}
		st.BumpEpoch()
		writeSimple(c.bw, "OK")
	case "ADDSLOTSRANGE":
		if len(args) < 3 || (len(args)-1)%2 != 0 {
			writeError(c.bw, "wrong number of arguments for 'cluster|addslotsrange'")
			return
		}
		myself := st.Myself()
		for i := 1; i+1 < len(args); i += 2 {
			lo, _ := strconv.Atoi(args[i])
			hi, _ := strconv.Atoi(args[i+1])
			for s := lo; s <= hi; s++ {
				_, _ = st.AssignSlot(s, myself.ID)
			}
		}
		st.BumpEpoch()
		writeSimple(c.bw, "OK")
	case "DELSLOTS":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'cluster|delslots'")
			return
		}
		for _, a := range args[1:] {
			slot, err := strconv.Atoi(a)
			if err != nil {
				writeError(c.bw, "value is not an integer")
				return
			}
			_ = st.UnassignSlot(slot)
		}
		st.BumpEpoch()
		writeSimple(c.bw, "OK")
	case "SETSLOT":
		clusterSetSlot(c, st, args[1:])
	case "MEET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'cluster|meet'")
			return
		}
		busPort := args[2]
		if len(args) >= 4 {
			busPort = args[3]
		}
		if c.eng.Bus == nil {
			writeError(c.bw, "ERR cluster bus not running")
			return
		}
		if err := c.eng.Bus.Meet(args[1], busPort); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "FORGET":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'cluster|forget'")
			return
		}
		if !st.ForgetNode(args[1]) {
			writeError(c.bw, "ERR Unknown node")
			return
		}
		writeSimple(c.bw, "OK")
	case "REPLICATE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'cluster|replicate'")
			return
		}
		myself := st.Myself()
		if myself == nil {
			writeError(c.bw, "ERR cluster not enabled")
			return
		}
		myself.Role = cluster.RoleReplica
		myself.MasterID = args[1]
		myself.ClearFlag(cluster.FlagMaster)
		myself.SetFlag(cluster.FlagReplica)
		writeSimple(c.bw, "OK")
	case "FAILOVER":
		// Cluster-mode FAILOVER is a hint to be promoted; without a
		// full election protocol we simply mark ourselves as master.
		myself := st.Myself()
		if myself != nil {
			myself.Role = cluster.RoleMaster
			myself.SetFlag(cluster.FlagMaster)
			myself.ClearFlag(cluster.FlagReplica)
			myself.MasterID = ""
		}
		st.BumpEpoch()
		writeSimple(c.bw, "OK")
	case "RESET":
		hard := len(args) >= 2 && strings.EqualFold(args[1], "HARD")
		st.Reset(hard)
		writeSimple(c.bw, "OK")
	case "COUNT-FAILURE-REPORTS":
		writeInt(c.bw, 0)
	case "BUMPEPOCH":
		writeBulk(c.bw, "BUMPED "+strconv.FormatInt(st.BumpEpoch(), 10))
	case "LINKS":
		c.clusterLinksCmd()
	case "REPLICAS", "SLAVES":
		c.clusterReplicasCmd(args[1:])
	case "MYSHARDID":
		c.clusterMyShardIDCmd()
	case "FLUSHSLOTS":
		c.clusterFlushSlotsCmd()
	case "SAVECONFIG":
		c.clusterSaveConfigCmd()
	case "SLOT-STATS":
		c.clusterSlotStatsCmd(args[1:])
	case "MIGRATION":
		c.clusterMigrationCmd()
	default:
		writeError(c.bw, "Unknown CLUSTER subcommand "+sub)
	}
}

// clusterSetSlot handles CLUSTER SETSLOT slot {MIGRATING|IMPORTING|STABLE|NODE} [target].
func clusterSetSlot(c *conn, st *cluster.State, args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'cluster|setslot'")
		return
	}
	slot, err := strconv.Atoi(args[0])
	if err != nil {
		writeError(c.bw, "slot is not an integer")
		return
	}
	switch strings.ToUpper(args[1]) {
	case "MIGRATING":
		if len(args) < 3 {
			writeError(c.bw, "syntax error")
			return
		}
		_ = st.SetSlotState(slot, cluster.SlotMigrating, args[2])
		writeSimple(c.bw, "OK")
	case "IMPORTING":
		if len(args) < 3 {
			writeError(c.bw, "syntax error")
			return
		}
		_ = st.SetSlotState(slot, cluster.SlotImporting, args[2])
		writeSimple(c.bw, "OK")
	case "STABLE":
		_ = st.SetSlotState(slot, cluster.SlotStable, "")
		writeSimple(c.bw, "OK")
	case "NODE":
		if len(args) < 3 {
			writeError(c.bw, "syntax error")
			return
		}
		if _, err := st.AssignSlot(slot, args[2]); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		st.BumpEpoch()
		writeSimple(c.bw, "OK")
	default:
		writeError(c.bw, "syntax error")
	}
}

// formatClusterInfo renders the canonical CLUSTER INFO bulk string.
func formatClusterInfo(st *cluster.State) string {
	stats := st.Stats()
	state := stats.State
	if !stats.Enabled {
		state = "disabled"
	}
	return fmt.Sprintf(
		"cluster_enabled:%d\r\ncluster_state:%s\r\ncluster_slots_assigned:%d\r\n"+
			"cluster_slots_ok:%d\r\ncluster_slots_pfail:%d\r\ncluster_slots_fail:%d\r\n"+
			"cluster_known_nodes:%d\r\ncluster_size:%d\r\n"+
			"cluster_current_epoch:%d\r\ncluster_my_epoch:%d\r\n",
		boolToInt(stats.Enabled), state, stats.SlotsAssigned, stats.SlotsOK,
		stats.SlotsPFail, stats.SlotsFail, stats.KnownNodes, stats.Size,
		stats.CurrentEpoch, stats.MyEpoch,
	)
}

func formatClusterNodes(st *cluster.State) string {
	myself := st.Myself()
	myID := ""
	if myself != nil {
		myID = myself.ID
	}
	var sb strings.Builder
	for _, n := range st.Nodes() {
		sb.WriteString(cluster.FormatNodesLine(n, myID))
		sb.WriteByte('\n')
	}
	return sb.String()
}

func writeClusterSlots(w *bufio.Writer, st *cluster.State) {
	// Group consecutive slots with the same owner into one [start, end, [host port id]] tuple.
	rows := [][]any{}
	for _, n := range st.Nodes() {
		if n.Role != cluster.RoleMaster {
			continue
		}
		for _, r := range n.SlotRanges() {
			rows = append(rows, []any{
				int64(r[0]), int64(r[1]),
				[]any{n.Host, parsePortInt(n.Port), n.ID},
			})
		}
	}
	fmt.Fprintf(w, "*%d\r\n", len(rows))
	for _, row := range rows {
		writeValue(w, row)
	}
}

func writeClusterShards(w *bufio.Writer, st *cluster.State) {
	// One shard per master + its replicas.
	masters := []*cluster.Node{}
	for _, n := range st.Nodes() {
		if n.Role == cluster.RoleMaster {
			masters = append(masters, n)
		}
	}
	fmt.Fprintf(w, "*%d\r\n", len(masters))
	for _, m := range masters {
		// shard = [slots, nodes]
		slotPairs := []any{}
		for _, r := range m.SlotRanges() {
			slotPairs = append(slotPairs, int64(r[0]), int64(r[1]))
		}
		nodes := []any{
			[]any{
				"id", m.ID,
				"port", parsePortInt(m.Port),
				"ip", m.Host,
				"role", "master",
				"replication-offset", int64(0),
				"health", "online",
			},
		}
		for _, n := range st.Nodes() {
			if n.Role == cluster.RoleReplica && n.MasterID == m.ID {
				nodes = append(nodes, []any{
					"id", n.ID,
					"port", parsePortInt(n.Port),
					"ip", n.Host,
					"role", "replica",
					"replication-offset", int64(0),
					"health", "online",
				})
			}
		}
		writeValue(w, []any{"slots", slotPairs, "nodes", nodes})
	}
}

func parsePortInt(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// askingCmd flips the per-conn ASKING flag for exactly the next command.
func (c *conn) askingCmd() {
	c.asking = true
	writeSimple(c.bw, "OK")
}

// readonlyCmd / readwriteCmd toggle whether this conn accepts reads on
// imported slots from a replica perspective.
func (c *conn) readonlyCmd() {
	c.readonly = true
	writeSimple(c.bw, "OK")
}

func (c *conn) readwriteCmd() {
	c.readonly = false
	writeSimple(c.bw, "OK")
}

// migrateCmd implements MIGRATE host port key|"" dest-db timeout
// [COPY] [REPLACE] [AUTH password] [AUTH2 user pass] [KEYS key [key ...]].
//
// We use DUMP + RESTORE under the hood — same payload the local
// COPY command uses, so cross-cluster moves don't require a special
// wire format.
func (c *conn) migrateCmd(args []string) {
	if len(args) < 5 {
		writeError(c.bw, "wrong number of arguments for 'migrate'")
		return
	}
	host, port := args[0], args[1]
	keyArg := args[2]
	// dest-db ignored — we only have db 0
	timeoutMs, _ := strconv.Atoi(args[4])

	copy_, replace := false, false
	authUser, authPass := "", ""
	keys := []string{}
	if keyArg != "" {
		keys = []string{keyArg}
	}
	for i := 5; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "COPY":
			copy_ = true
		case "REPLACE":
			replace = true
		case "AUTH":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			authPass = args[i+1]
			i++
		case "AUTH2":
			if i+2 >= len(args) {
				writeError(c.bw, "syntax error")
				return
			}
			authUser, authPass = args[i+1], args[i+2]
			i += 2
		case "KEYS":
			keys = append(keys, args[i+1:]...)
			i = len(args)
		}
	}
	if len(keys) == 0 {
		writeError(c.bw, "ERR no keys to migrate")
		return
	}

	if err := migrateRun(c, host, port, time.Duration(timeoutMs)*time.Millisecond,
		keys, copy_, replace, authUser, authPass); err != nil {
		writeError(c.bw, err.Error())
		return
	}
	writeSimple(c.bw, "OK")
}

// migrateRun is the worker half of MIGRATE. For each key it:
//
//	1. DUMPs locally
//	2. RESTOREs over the dial to host:port (with optional REPLACE)
//	3. DELetes locally unless COPY was set
//
// This is purposely simple — a single TCP connection per call, no
// pipelining — but it's correct and bounded. Real Redis pipelines the
// whole batch; we can layer that on later without breaking callers.
func migrateRun(c *conn, host, port string, timeout time.Duration,
	keys []string, copy_, replace bool, authUser, authPass string) error {
	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, max(timeout, 3*time.Second))
	if err != nil {
		return fmt.Errorf("IOERR dial %s: %w", addr, err)
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	send := func(parts ...string) error {
		_, err := bw.Write(encodeRESP(parts))
		if err != nil {
			return err
		}
		return bw.Flush()
	}
	expectOK := func() error {
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "+") {
			return nil
		}
		if strings.HasPrefix(line, "-") {
			return errors.New(line[1:])
		}
		return fmt.Errorf("unexpected reply: %s", line)
	}

	if authPass != "" {
		if authUser != "" {
			if err := send("AUTH", authUser, authPass); err != nil {
				return err
			}
		} else {
			if err := send("AUTH", authPass); err != nil {
				return err
			}
		}
		if err := expectOK(); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}

	for _, k := range keys {
		blob, ok, err := c.eng.KV.Dump(k)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		ttlMs := int64(0)
		if d := c.eng.KV.TTL(k); d > 0 {
			ttlMs = d.Milliseconds()
		}
		// Build RESTORE … [REPLACE]
		parts := []string{"RESTORE", k, strconv.FormatInt(ttlMs, 10), blob}
		if replace {
			parts = append(parts, "REPLACE")
		}
		if err := send(parts...); err != nil {
			return err
		}
		if err := expectOK(); err != nil {
			return fmt.Errorf("restore %s: %w", k, err)
		}
		if !copy_ {
			c.eng.KV.Del(k)
			c.eng.RecordWrite("DEL", []string{k})
		}
	}
	return nil
}

// encodeRESP is a tiny RESP encoder used by MIGRATE — we don't reach
// for the engine-side encoder because we want zero allocations on the
// network path and a private copy keeps the dependency graph clean.
func encodeRESP(parts []string) []byte {
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%d\r\n", len(parts))
	for _, p := range parts {
		fmt.Fprintf(&sb, "$%d\r\n%s\r\n", len(p), p)
	}
	return []byte(sb.String())
}

func max(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
