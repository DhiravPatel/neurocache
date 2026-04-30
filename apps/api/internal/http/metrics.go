package http

import (
	"net/http"
	"strconv"
)

func (h *handlers) metricsSummary(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, h.eng.Metrics.Summary())
}

func (h *handlers) metricsTimeline(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"samples": h.eng.Metrics.Timeline()})
}

func (h *handlers) metricsHotKeys(w http.ResponseWriter, r *http.Request) {
	k := 10
	if v, err := strconv.Atoi(r.URL.Query().Get("k")); err == nil && v > 0 {
		k = v
	}
	writeJSON(w, 200, map[string]any{"keys": h.eng.Metrics.HotKeys(k)})
}

func (h *handlers) metricsBreakdown(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"commands": h.eng.Metrics.CommandBreakdown()})
}

// vectorSets surfaces the keyspace's vector-set inventory: every key
// of TypeVector with its index config + cardinality. Backs the
// "Vector Sets" dashboard page.
func (h *handlers) vectorSets(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, key := range h.eng.KV.Keys("*") {
		info, ok, err := h.eng.KV.VInfo(key)
		if err != nil || !ok {
			continue
		}
		out = append(out, map[string]any{
			"key":          key,
			"algo":         info.Algo,
			"dim":          info.Dim,
			"metric":       info.Metric,
			"m":            info.M,
			"ef_construct": info.EFC,
			"ef_runtime":   info.EFR,
			"card":         info.Card,
			"bytes_approx": info.BytesApprox,
		})
	}
	writeJSON(w, 200, map[string]any{"sets": out})
}

// hotKeysTracker surfaces the runtime HeavyKeeper-backed top-K tracker
// (HOTKEYS command). Distinct from /api/metrics/hot-keys, which only
// tracks GET hits — this endpoint captures every keyspace mutation
// the engine notifier sees, downsampled.
func (h *handlers) hotKeysTracker(w http.ResponseWriter, r *http.Request) {
	if h.eng.HotKeys == nil {
		writeJSON(w, 503, map[string]any{"error": "hotkeys tracker disabled"})
		return
	}
	k := 0 // 0 = whole heap
	if v, err := strconv.Atoi(r.URL.Query().Get("k")); err == nil && v > 0 {
		k = v
	}
	rows := h.eng.HotKeys.Top(k)
	out := make([]map[string]any, len(rows))
	for i, r := range rows {
		out[i] = map[string]any{"key": r.Key, "count": r.Count}
	}
	stats := h.eng.HotKeys.Stats()
	writeJSON(w, 200, map[string]any{
		"keys":  out,
		"stats": stats,
	})
}
