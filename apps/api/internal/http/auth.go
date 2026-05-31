package http

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// httpAuthed reports whether a request to a command/data endpoint is
// authorized. It mirrors the RESP posture exactly: when the operator
// enables protected mode (the same switch that forces AUTH on the RESP
// port), the HTTP surface — which can run arbitrary commands via
// /api/exec and exposes keyspace metadata — must authenticate too.
// Otherwise it stays open for the local dashboard, the same way RESP
// allows unauthenticated access when no password is configured.
//
// Without this, /api/exec was an unauthenticated arbitrary-command
// channel: a browser (CORS lets the request through even if it can't
// read the reply) or any network client could POST a command and read
// or destroy the keyspace, or pivot the server outbound (SSRF).
func (h *handlers) httpAuthed(r *http.Request) bool {
	if !h.cfg.ProtectedMode {
		return true
	}
	user, pass, ok := parseHTTPCreds(r)
	if !ok {
		return false
	}
	_, err := h.eng.ACL.Authenticate(user, pass)
	return err == nil
}

// requireAuth wraps a handler so it is reachable only when httpAuthed
// passes. Used for endpoints that disclose keyspace contents (hot keys,
// vector sets) so they don't leak key names to unauthenticated callers
// when the server is in protected mode.
func (h *handlers) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.httpAuthed(r) {
			writeErr(w, 401, "NOAUTH Authentication required")
			return
		}
		next(w, r)
	}
}

// parseHTTPCreds pulls credentials from the Authorization header.
// Supports "Bearer <password>" (validated against the default user — the
// requirepass equivalent) and "Basic <base64(user:pass)>".
func parseHTTPCreds(r *http.Request) (user, pass string, ok bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", "", false
	}
	if v, found := strings.CutPrefix(h, "Bearer "); found {
		return "default", strings.TrimSpace(v), true
	}
	if v, found := strings.CutPrefix(h, "Basic "); found {
		dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil {
			return "", "", false
		}
		uu, pp, found := strings.Cut(string(dec), ":")
		if !found {
			return "", "", false
		}
		return uu, pp, true
	}
	return "", "", false
}

// httpDangerousCmd is the set of commands refused on the HTTP API
// regardless of auth. These are admin / replication / cluster verbs that
// can make the server dial outbound (SSRF to cloud metadata or internal
// services), load code, or shut down — they belong on the authenticated
// RESP admin channel, not a browser-reachable HTTP endpoint. Blocking
// them removes the CSRF/SSRF surface entirely (e.g. a malicious page
// POSTing {"command":"REPLICAOF","args":["169.254.169.254","80"]}).
var httpDangerousCmd = map[string]bool{
	"REPLICAOF": true, "SLAVEOF": true, "CLUSTER": true, "MIGRATE": true,
	"MODULE": true, "SHUTDOWN": true, "DEBUG": true, "FAILOVER": true,
	"PSYNC": true, "SYNC": true, "REPLCONF": true,
}
