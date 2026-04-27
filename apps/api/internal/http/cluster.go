package http

import (
	"errors"
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/cluster"
)

// httpCluster mirrors the RESP CLUSTER * surface so the dashboard
// playground can drive cluster admin from the browser. We expose the
// read paths (INFO/NODES/SLOTS/SHARDS/MYID/KEYSLOT/COUNTKEYSINSLOT)
// freely; mutating subcommands (ADDSLOTS, DELSLOTS, SETSLOT, MEET,
// FORGET, RESET) are also exposed because operators ask the dashboard
// to bootstrap a fresh cluster.
func httpCluster(h *handlers, args []string) (any, error) {
	if len(args) < 1 {
		return nil, errors.New("CLUSTER subcommand ...")
	}
	st := h.eng.Cluster
	if st == nil {
		return nil, errors.New("cluster support disabled")
	}
	switch strings.ToUpper(args[0]) {
	case "MYID":
		if m := st.Myself(); m != nil {
			return m.ID, nil
		}
		return "", nil
	case "INFO":
		stats := st.Stats()
		return map[string]any{
			"enabled":          stats.Enabled,
			"state":            stats.State,
			"slots_assigned":   stats.SlotsAssigned,
			"slots_ok":         stats.SlotsOK,
			"slots_pfail":      stats.SlotsPFail,
			"slots_fail":       stats.SlotsFail,
			"known_nodes":      stats.KnownNodes,
			"size":             stats.Size,
			"current_epoch":    stats.CurrentEpoch,
			"my_epoch":         stats.MyEpoch,
		}, nil
	case "NODES":
		myID := ""
		if m := st.Myself(); m != nil {
			myID = m.ID
		}
		out := []map[string]any{}
		for _, n := range st.Nodes() {
			ranges := []map[string]int{}
			for _, r := range n.SlotRanges() {
				ranges = append(ranges, map[string]int{"start": r[0], "end": r[1]})
			}
			out = append(out, map[string]any{
				"id":           n.ID,
				"addr":         n.Addr(),
				"bus":          n.BusAddr(),
				"role":         n.Role.String(),
				"master_id":    n.MasterID,
				"flags":        n.Flags().String(),
				"slot_ranges":  ranges,
				"is_self":      n.ID == myID,
				"config_epoch": n.ConfigEpoch,
			})
		}
		return out, nil
	case "KEYSLOT":
		if len(args) < 2 {
			return nil, errors.New("CLUSTER KEYSLOT key")
		}
		return int64(cluster.KeySlot(args[1])), nil
	case "COUNTKEYSINSLOT":
		if len(args) < 2 {
			return nil, errors.New("CLUSTER COUNTKEYSINSLOT slot")
		}
		slot, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, err
		}
		return int64(st.CountKeysInSlot(slot)), nil
	case "GETKEYSINSLOT":
		if len(args) < 3 {
			return nil, errors.New("CLUSTER GETKEYSINSLOT slot count")
		}
		slot, _ := strconv.Atoi(args[1])
		count, _ := strconv.Atoi(args[2])
		return h.eng.KeysInSlot(slot, count), nil
	case "ADDSLOTS", "ADDSLOTSRANGE":
		myself := st.Myself()
		if myself == nil {
			return nil, errors.New("cluster not enabled")
		}
		if strings.EqualFold(args[0], "ADDSLOTS") {
			for _, a := range args[1:] {
				slot, err := strconv.Atoi(a)
				if err != nil {
					return nil, err
				}
				if _, err := st.AssignSlot(slot, myself.ID); err != nil {
					return nil, err
				}
			}
		} else {
			for i := 1; i+1 < len(args); i += 2 {
				lo, _ := strconv.Atoi(args[i])
				hi, _ := strconv.Atoi(args[i+1])
				for s := lo; s <= hi; s++ {
					if _, err := st.AssignSlot(s, myself.ID); err != nil {
						return nil, err
					}
				}
			}
		}
		st.BumpEpoch()
		return "OK", nil
	case "DELSLOTS":
		for _, a := range args[1:] {
			slot, err := strconv.Atoi(a)
			if err != nil {
				return nil, err
			}
			_ = st.UnassignSlot(slot)
		}
		st.BumpEpoch()
		return "OK", nil
	case "MEET":
		if len(args) < 3 {
			return nil, errors.New("CLUSTER MEET host bus-port")
		}
		if h.eng.Bus == nil {
			return nil, errors.New("cluster bus not running")
		}
		busPort := args[2]
		if len(args) >= 4 {
			busPort = args[3]
		}
		return "OK", h.eng.Bus.Meet(args[1], busPort)
	case "FORGET":
		if len(args) < 2 {
			return nil, errors.New("CLUSTER FORGET node-id")
		}
		if !st.ForgetNode(args[1]) {
			return nil, errors.New("Unknown node")
		}
		return "OK", nil
	case "RESET":
		hard := len(args) >= 2 && strings.EqualFold(args[1], "HARD")
		st.Reset(hard)
		return "OK", nil
	case "BUMPEPOCH":
		return st.BumpEpoch(), nil
	}
	return nil, errors.New("unknown CLUSTER subcommand")
}
