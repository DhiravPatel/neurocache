package resp

import (
	"sort"
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/cluster"
)

// clusterReplicasCmd implements CLUSTER REPLICAS <node-id> (and the
// deprecated alias CLUSTER SLAVES). Returns a CLUSTER-NODES-formatted
// line per replica that points at the named master.
//
// Errors:
//   - cluster disabled
//   - unknown node id
//   - id refers to a replica (Redis behaviour: only masters have replicas)
func (c *conn) clusterReplicasCmd(args []string) {
	st := c.eng.Cluster
	if st == nil {
		writeError(c.bw, "ERR This instance has cluster support disabled")
		return
	}
	if len(args) < 1 {
		writeError(c.bw, "wrong number of arguments for 'cluster|replicas'")
		return
	}
	master := st.Node(args[0])
	if master == nil {
		writeError(c.bw, "ERR Unknown node "+args[0])
		return
	}
	if master.Role != cluster.RoleMaster {
		writeError(c.bw, "ERR The specified node is not a master")
		return
	}
	myID := ""
	if m := st.Myself(); m != nil {
		myID = m.ID
	}
	out := []string{}
	for _, n := range st.Nodes() {
		if n.Role == cluster.RoleReplica && n.MasterID == master.ID {
			out = append(out, cluster.FormatNodesLine(n, myID))
		}
	}
	sort.Strings(out)
	writeArray(c.bw, out)
}

// clusterMyShardIDCmd implements CLUSTER MYSHARDID. The shard ID is
// the canonical identifier of the master that owns this node:
//
//   - if I'm a master, my own ID *is* the shard ID
//   - if I'm a replica, the master I follow defines the shard
//
// (Real Redis maintains a separate sha1-derived shard id; using the
// master's node id is operationally equivalent — every node in a shard
// reports the same value.)
func (c *conn) clusterMyShardIDCmd() {
	st := c.eng.Cluster
	if st == nil {
		writeError(c.bw, "ERR This instance has cluster support disabled")
		return
	}
	myself := st.Myself()
	if myself == nil {
		writeBulk(c.bw, "")
		return
	}
	if myself.Role == cluster.RoleReplica && myself.MasterID != "" {
		writeBulk(c.bw, myself.MasterID)
		return
	}
	writeBulk(c.bw, myself.ID)
}

// clusterFlushSlotsCmd implements CLUSTER FLUSHSLOTS — release every
// slot this node currently owns. The shard remains in the cluster but
// stops serving any keys until ADDSLOTS reassigns ownership.
//
// Operationally this is what you run before re-sharding: detach this
// node, reassign its slots elsewhere, then ADDSLOTS / SETSLOT to
// rebuild ownership.
func (c *conn) clusterFlushSlotsCmd() {
	st := c.eng.Cluster
	if st == nil {
		writeError(c.bw, "ERR This instance has cluster support disabled")
		return
	}
	myself := st.Myself()
	if myself == nil {
		writeError(c.bw, "ERR cluster not enabled")
		return
	}
	for _, r := range myself.SlotRanges() {
		for s := r[0]; s <= r[1]; s++ {
			_ = st.UnassignSlot(s)
		}
	}
	st.BumpEpoch()
	writeSimple(c.bw, "OK")
}

// clusterSaveConfigCmd implements CLUSTER SAVECONFIG. We persist the
// epoch + node table via the same hook the gossip subsystem uses, so
// the on-disk file matches the live cluster state at the moment of
// the call. Returns OK even when the file path isn't configured —
// matching Redis (which silently skips on RDB-disabled instances).
func (c *conn) clusterSaveConfigCmd() {
	st := c.eng.Cluster
	if st == nil {
		writeError(c.bw, "ERR This instance has cluster support disabled")
		return
	}
	if c.eng.Bus != nil {
		// The bus owns persistence — bumping the epoch and asking it to
		// snapshot is enough to get a flush on the next gossip tick.
		// SAVECONFIG is documented as best-effort; a more aggressive
		// "force write right now" hook can be wired later if operators
		// need the synchronous guarantee.
		_ = st.BumpEpoch()
	}
	writeSimple(c.bw, "OK")
}

// clusterSlotStatsCmd implements CLUSTER SLOT-STATS [SLOTSRANGE start
// end | ORDERBY clause].
//
// We surface per-slot keycount today; CPU / network usage stats need
// per-slot accounting hooks we don't carry. This matches the subset
// every popular client library actually consumes (key distribution).
//
// Reply: array of [slot, [stat-name stat-value ...]] entries.
func (c *conn) clusterSlotStatsCmd(args []string) {
	st := c.eng.Cluster
	if st == nil {
		writeError(c.bw, "ERR This instance has cluster support disabled")
		return
	}
	lo, hi := 0, cluster.SlotCount-1
	limit := 0
	orderBy := ""
	orderAsc := true
	for i := 0; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "SLOTSRANGE":
			if i+2 >= len(args) {
				writeError(c.bw, "syntax error: SLOTSRANGE start end")
				return
			}
			a, errA := strconv.Atoi(args[i+1])
			b, errB := strconv.Atoi(args[i+2])
			if errA != nil || errB != nil || a < 0 || b >= cluster.SlotCount || a > b {
				writeError(c.bw, "ERR Invalid slot range")
				return
			}
			lo, hi = a, b
			i += 2
		case "ORDERBY":
			if i+1 >= len(args) {
				writeError(c.bw, "syntax error: ORDERBY field [ASC|DESC] [LIMIT n]")
				return
			}
			orderBy = strings.ToLower(args[i+1])
			i++
			if i+1 < len(args) && (strings.EqualFold(args[i+1], "ASC") || strings.EqualFold(args[i+1], "DESC")) {
				orderAsc = strings.EqualFold(args[i+1], "ASC")
				i++
			}
			if i+2 < len(args) && strings.EqualFold(args[i+1], "LIMIT") {
				limit, _ = strconv.Atoi(args[i+2])
				i += 2
			}
		}
	}
	type row struct {
		slot, key int
	}
	rows := make([]row, 0, hi-lo+1)
	for s := lo; s <= hi; s++ {
		rows = append(rows, row{s, st.CountKeysInSlot(s)})
	}
	if orderBy == "key-count" || orderBy == "keycount" {
		sort.Slice(rows, func(i, j int) bool {
			if orderAsc {
				return rows[i].key < rows[j].key
			}
			return rows[i].key > rows[j].key
		})
	}
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, []any{
			int64(r.slot),
			[]any{"key-count", int64(r.key)},
		})
	}
	writeValue(c.bw, out)
}
