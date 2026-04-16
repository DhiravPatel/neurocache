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
