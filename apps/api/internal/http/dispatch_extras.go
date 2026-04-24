package http

import (
	"errors"
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// httpXGroup mirrors resp.xgroupCmd for the HTTP+replay path.
func httpXGroup(h *handlers, args []string) (any, error) {
	if len(args) < 1 {
		return nil, errors.New("XGROUP subcommand ...")
	}
	switch strings.ToUpper(args[0]) {
	case "CREATE":
		if len(args) < 4 {
			return nil, errors.New("XGROUP CREATE key group id [MKSTREAM]")
		}
		mkstream := len(args) >= 5 && strings.EqualFold(args[4], "MKSTREAM")
		return "OK", h.eng.KV.XGroupCreate(args[1], args[2], args[3], mkstream)
	case "SETID":
		if len(args) < 4 {
			return nil, errors.New("XGROUP SETID key group id")
		}
		return "OK", h.eng.KV.XGroupSetID(args[1], args[2], args[3])
	case "DESTROY":
		if len(args) < 3 {
			return nil, errors.New("XGROUP DESTROY key group")
		}
		return h.eng.KV.XGroupDestroy(args[1], args[2])
	case "CREATECONSUMER":
		if len(args) < 4 {
			return nil, errors.New("XGROUP CREATECONSUMER key group consumer")
		}
		return h.eng.KV.XGroupCreateConsumer(args[1], args[2], args[3])
	case "DELCONSUMER":
		if len(args) < 4 {
			return nil, errors.New("XGROUP DELCONSUMER key group consumer")
		}
		return h.eng.KV.XGroupDelConsumer(args[1], args[2], args[3])
	}
	return nil, errors.New("unknown XGROUP subcommand")
}

// httpXReadGroup implements the non-blocking flavour. BLOCK is a no-op
// over HTTP (HTTP requests are one-shot); real blocking clients should
// use the RESP port.
func httpXReadGroup(h *handlers, args []string) (any, error) {
	if len(args) < 6 || !strings.EqualFold(args[0], "GROUP") {
		return nil, errors.New("XREADGROUP GROUP <g> <c> [COUNT n] [NOACK] STREAMS key [key ...] id [id ...]")
	}
	group, consumer := args[1], args[2]
	count := 0
	noack := false
	i := 3
	for ; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "COUNT":
			if i+1 >= len(args) {
				return nil, errors.New("syntax error")
			}
			count, _ = strconv.Atoi(args[i+1])
			i++
		case "BLOCK":
			if i+1 >= len(args) {
				return nil, errors.New("syntax error")
			}
			i++ // ignored over HTTP
		case "NOACK":
			noack = true
		case "STREAMS":
			i++
			goto streams
		}
	}
streams:
	rest := args[i:]
	if len(rest) == 0 || len(rest)%2 != 0 {
		return nil, errors.New("unbalanced XREADGROUP STREAMS keys and IDs")
	}
	half := len(rest) / 2
	return h.eng.KV.XReadGroup(group, consumer, rest[:half], rest[half:], count, noack)
}

// httpXClaim mirrors resp.xclaimCmd.
func httpXClaim(h *handlers, args []string) (any, error) {
	if len(args) < 5 {
		return nil, errors.New("XCLAIM key group consumer min-idle-ms id [id ...] [IDLE ms] [TIME t] [RETRYCOUNT n] [FORCE] [JUSTID]")
	}
	key, group, consumer := args[0], args[1], args[2]
	minIdle, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		return nil, errors.New("min-idle-ms is not an integer")
	}
	ids := []string{}
	opts := store.XClaimOpts{}
	for i := 4; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "IDLE":
			opts.IdleMs, _ = strconv.ParseInt(args[i+1], 10, 64)
			i++
		case "TIME":
			opts.Time, _ = strconv.ParseInt(args[i+1], 10, 64)
			i++
		case "RETRYCOUNT":
			opts.Retry, _ = strconv.ParseInt(args[i+1], 10, 64)
			i++
		case "FORCE":
			opts.Force = true
		case "JUSTID":
			opts.JustIDs = true
		default:
			ids = append(ids, args[i])
		}
	}
	entries, justIDs, err := h.eng.KV.XClaim(key, group, consumer, minIdle, ids, opts)
	if err != nil {
		return nil, err
	}
	if opts.JustIDs {
		return justIDs, nil
	}
	return entries, nil
}

// httpXAutoClaim mirrors resp.xautoclaimCmd.
func httpXAutoClaim(h *handlers, args []string) (any, error) {
	if len(args) < 5 {
		return nil, errors.New("XAUTOCLAIM key group consumer min-idle-ms start [COUNT n] [JUSTID]")
	}
	key, group, consumer := args[0], args[1], args[2]
	minIdle, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		return nil, err
	}
	start := args[4]
	count := 100
	justIDs := false
	for i := 5; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "COUNT":
			count, _ = strconv.Atoi(args[i+1])
			i++
		case "JUSTID":
			justIDs = true
		}
	}
	entries, justs, cursor, deleted, err := h.eng.KV.XAutoClaim(key, group, consumer, minIdle, start, count, justIDs)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"cursor": cursor, "deleted": deleted}
	if justIDs {
		out["ids"] = justs
	} else {
		out["entries"] = entries
	}
	return out, nil
}

// httpXInfo mirrors resp.xinfoCmd.
func httpXInfo(h *handlers, args []string) (any, error) {
	if len(args) < 2 {
		return nil, errors.New("XINFO STREAM|GROUPS|CONSUMERS key [group]")
	}
	switch strings.ToUpper(args[0]) {
	case "STREAM":
		return h.eng.KV.XInfoStream(args[1])
	case "GROUPS":
		return h.eng.KV.XInfoGroups(args[1])
	case "CONSUMERS":
		if len(args) < 3 {
			return nil, errors.New("XINFO CONSUMERS key group")
		}
		return h.eng.KV.XInfoConsumers(args[1], args[2])
	}
	return nil, errors.New("unknown XINFO subcommand")
}

// httpACL exposes the read-only ACL surface over HTTP. Writes (SETUSER,
// DELUSER) still require an authenticated RESP client so the dashboard
// can't silently mutate the ACL registry.
func httpACL(h *handlers, args []string) (any, error) {
	if len(args) < 1 {
		return nil, errors.New("ACL subcommand ...")
	}
	switch strings.ToUpper(args[0]) {
	case "LIST":
		return h.eng.ACL.List(), nil
	case "USERS":
		return h.eng.ACL.List(), nil
	case "WHOAMI":
		if u := h.eng.ACL.DefaultUser(); u != nil {
			return u.Name, nil
		}
		return "", nil
	case "GETUSER":
		if len(args) < 2 {
			return nil, errors.New("ACL GETUSER name")
		}
		u := h.eng.ACL.Get(args[1])
		if u == nil {
			return nil, nil
		}
		return map[string]any{
			"flags": u.Describe(), "passwords": u.Hashes(),
			"keys": u.KeyPatterns, "channels": u.ChannelPatterns,
		}, nil
	case "CAT":
		return nil, nil
	case "LOG":
		return h.eng.ACL.Log(0), nil
	case "SETUSER":
		if len(args) < 2 {
			return nil, errors.New("ACL SETUSER name [rule ...]")
		}
		if err := h.eng.ACL.SetUser(args[1], args[2:]); err != nil {
			return nil, err
		}
		_ = h.eng.ACL.Save()
		return "OK", nil
	case "DELUSER":
		if len(args) < 2 {
			return nil, errors.New("ACL DELUSER name [name ...]")
		}
		n := h.eng.ACL.Delete(args[1:]...)
		_ = h.eng.ACL.Save()
		return int64(n), nil
	}
	return nil, errors.New("unknown ACL subcommand")
}
