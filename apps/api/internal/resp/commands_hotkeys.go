package resp

import (
	"strconv"
	"strings"
)

// hotkeysCmd implements the HOTKEYS command surface — NeuroCache's
// runtime top-K key access tracker.
//
//   HOTKEYS [count]                    — top-N keys by estimated frequency
//   HOTKEYS RESET                      — clear counters
//   HOTKEYS STATS                      — tracker configuration + observation count
//   HOTKEYS COUNT key                  — estimated frequency for one key
//   HOTKEYS THRESHOLD min              — minimum count to surface a key (0 = all)
//   HOTKEYS RESIZE k                   — rebuild the heavy-keeper with new K (resets)
//   HOTKEYS SAMPLE every               — 1-in-N sampling rate (1 = every event)
//   HOTKEYS ENABLE | DISABLE           — toggle the tracker
//
// The HOTKEYS form (without a subcommand) returns a flat array of
// key/count pairs ordered by descending count, capped at the optional
// count argument or the configured K when no count is supplied.
func (c *conn) hotkeysCmd(args []string) {
	hk := c.eng.HotKeys
	if hk == nil {
		writeError(c.bw, "ERR HOTKEYS tracker is not enabled")
		return
	}
	if len(args) == 0 {
		c.writeHotKeysTop(0)
		return
	}
	switch strings.ToUpper(args[0]) {
	case "RESET":
		hk.Reset()
		writeSimple(c.bw, "OK")
	case "STATS":
		s := hk.Stats()
		writeValue(c.bw, []any{
			"enabled", boolToInt64(s.Enabled),
			"k", int64(s.K),
			"width", int64(s.Width),
			"depth", int64(s.Depth),
			"decay", strconv.FormatFloat(s.Decay, 'f', -1, 64),
			"sample-every", int64(s.SampleEvery),
			"threshold", int64(s.Threshold),
			"tracked", int64(s.Tracked),
			"observations", int64(s.Observations),
			"events", int64(s.Events),
			"bytes-approx", s.BytesApprox,
		})
	case "COUNT":
		// HOTKEYS COUNT <key> returns the estimated frequency for one
		// specific key. We expose this via the tracker's underlying
		// HeavyKeeper Count to keep estimator semantics consistent.
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'hotkeys|count'")
			return
		}
		// Top(0) returns the whole heap — find this key's bucket.
		// Avoids reaching into HeavyKeeper internals from the dispatcher.
		for _, hkey := range hk.Top(0) {
			if hkey.Key == args[1] {
				writeInt(c.bw, int64(hkey.Count))
				return
			}
		}
		writeInt(c.bw, 0)
	case "THRESHOLD":
		if len(args) < 2 {
			// Read current threshold.
			writeInt(c.bw, int64(hk.Threshold()))
			return
		}
		v, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not a non-negative integer")
			return
		}
		hk.SetThreshold(v)
		writeSimple(c.bw, "OK")
	case "RESIZE":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'hotkeys|resize'")
			return
		}
		k, err := strconv.Atoi(args[1])
		if err != nil || k <= 0 {
			writeError(c.bw, "K must be a positive integer")
			return
		}
		hk.SetK(k)
		writeSimple(c.bw, "OK")
	case "SAMPLE":
		if len(args) < 2 {
			writeInt(c.bw, int64(hk.SampleRate()))
			return
		}
		v, err := strconv.ParseUint(args[1], 10, 64)
		if err != nil {
			writeError(c.bw, "value is not a non-negative integer")
			return
		}
		hk.SetSampleRate(v)
		writeSimple(c.bw, "OK")
	case "ENABLE":
		hk.SetEnabled(true)
		writeSimple(c.bw, "OK")
	case "DISABLE":
		hk.SetEnabled(false)
		writeSimple(c.bw, "OK")
	case "HELP":
		writeArray(c.bw, []string{
			"HOTKEYS [count]",
			"HOTKEYS RESET",
			"HOTKEYS STATS",
			"HOTKEYS COUNT <key>",
			"HOTKEYS THRESHOLD [min]",
			"HOTKEYS RESIZE <k>",
			"HOTKEYS SAMPLE [every]",
			"HOTKEYS ENABLE | DISABLE",
		})
	default:
		// HOTKEYS <count> with no subcommand keyword — treat the first
		// arg as a numeric cap, matching the Redis convention of
		// optional integer arguments without a flag.
		if n, err := strconv.Atoi(args[0]); err == nil && n > 0 {
			c.writeHotKeysTop(n)
			return
		}
		writeError(c.bw, "Unknown HOTKEYS subcommand "+args[0])
	}
}

// writeHotKeysTop emits the HOTKEYS array reply: a flat sequence of
// [key, count] pairs ordered by descending count, threshold-filtered.
func (c *conn) writeHotKeysTop(limit int) {
	rows := c.eng.HotKeys.Top(limit)
	out := make([]any, 0, len(rows)*2)
	for _, r := range rows {
		out = append(out, r.Key, int64(r.Count))
	}
	writeValue(c.bw, out)
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
