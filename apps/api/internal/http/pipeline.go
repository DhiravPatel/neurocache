package http

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ─── Pipelining over HTTP ────────────────────────────────────────────
//
// /api/exec runs one command per request — fine for a playground, but a
// throughput killer when an app issues many ops, since each pays a full HTTP
// round-trip (connection, headers, and TLS if enabled). /api/pipeline batches
// an arbitrary list of commands into ONE request and returns one result per
// command, in order — the same idea that makes RESP pipelining fast, brought
// to the HTTP/SDK surface. N round-trips collapse to 1.

const maxPipelineCommands = 1000

type pipelineReq struct {
	// Each command is [name, arg1, arg2, …], e.g. ["SET","k","v"].
	Commands [][]string `json:"commands"`
	// Stop at the first command that errors (Redis pipelining otherwise
	// runs every command regardless). Off by default.
	StopOnError bool `json:"stop_on_error,omitempty"`
}

type pipelineResult struct {
	Ok     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// pipeline → POST /api/pipeline {commands:[["SET","k","v"],["GET","k"]], stop_on_error?}
//   → {results:[{ok,result}|{ok:false,error}, …]}
func (h *handlers) pipeline(w http.ResponseWriter, r *http.Request) {
	// Same auth posture as /api/exec — a pipeline can run arbitrary commands.
	if !h.httpAuthed(r) {
		writeErr(w, 401, "NOAUTH Authentication required")
		return
	}
	var req pipelineReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if len(req.Commands) == 0 {
		writeErr(w, 400, "commands required")
		return
	}
	if len(req.Commands) > maxPipelineCommands {
		writeErr(w, 400, fmt.Sprintf("at most %d commands per pipeline", maxPipelineCommands))
		return
	}

	results := make([]pipelineResult, 0, len(req.Commands))
	for _, c := range req.Commands {
		if len(c) == 0 {
			results = append(results, pipelineResult{Ok: false, Error: "empty command"})
			if req.StopOnError {
				break
			}
			continue
		}
		cmd := strings.ToUpper(c[0])
		args := c[1:]

		// Admin/replication/cluster verbs stay off the HTTP surface, exactly
		// as on /api/exec — refused per-command rather than failing the batch.
		if httpDangerousCmd[cmd] {
			results = append(results, pipelineResult{
				Ok:    false,
				Error: "command '" + strings.ToLower(cmd) + "' is not allowed over the HTTP API; use the RESP interface",
			})
			if req.StopOnError {
				break
			}
			continue
		}

		start := time.Now()
		res, err := h.dispatch(cmd, args)
		h.record(cmd, start)
		if err != nil {
			results = append(results, pipelineResult{Ok: false, Error: err.Error()})
			if req.StopOnError {
				break
			}
			continue
		}
		if isWriteCommand(cmd) {
			h.eng.RecordWrite(cmd, args)
		}
		results = append(results, pipelineResult{Ok: true, Result: res})
	}

	writeJSON(w, 200, map[string]any{"results": results})
}
