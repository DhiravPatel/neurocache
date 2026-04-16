package http

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/memory"
)

// dispatch runs a Redis-style command via the HTTP exec endpoint.
// Kept small on purpose — mirrors the core AI-native and KV commands.
func (h *handlers) dispatch(cmd string, args []string) (any, error) {
	switch cmd {
	case "PING":
		return "PONG", nil

	case "SET":
		if len(args) < 2 {
			return nil, errors.New("SET key value [EX seconds]")
		}
		ttl := time.Duration(0)
		for i := 2; i+1 < len(args); i += 2 {
			if strings.EqualFold(args[i], "EX") {
				if n, err := strconv.Atoi(args[i+1]); err == nil {
					ttl = time.Duration(n) * time.Second
				}
			}
		}
		h.eng.KV.Set(args[0], args[1], ttl)
		return "OK", nil

	case "GET":
		if len(args) < 1 {
			return nil, errors.New("GET key")
		}
		v, ok := h.eng.KV.Get(args[0])
		h.eng.Metrics.RecordKVHit(args[0], ok)
		if !ok {
			return nil, nil
		}
		return v, nil

	case "DEL":
		if len(args) < 1 {
			return nil, errors.New("DEL key [key ...]")
		}
		return h.eng.KV.Del(args...), nil

	case "EXISTS":
		if len(args) < 1 {
			return nil, errors.New("EXISTS key")
		}
		n := 0
		for _, k := range args {
			if h.eng.KV.Exists(k) {
				n++
			}
		}
		return n, nil

	case "EXPIRE":
		if len(args) < 2 {
			return nil, errors.New("EXPIRE key seconds")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, err
		}
		return h.eng.KV.Expire(args[0], time.Duration(n)*time.Second), nil

	case "TTL":
		if len(args) < 1 {
			return nil, errors.New("TTL key")
		}
		return int64(h.eng.KV.TTL(args[0]).Seconds()), nil

	case "INCR":
		if len(args) < 1 {
			return nil, errors.New("INCR key")
		}
		return h.eng.KV.Incr(args[0], 1)

	case "DECR":
		if len(args) < 1 {
			return nil, errors.New("DECR key")
		}
		return h.eng.KV.Incr(args[0], -1)

	case "INCRBY":
		if len(args) < 2 {
			return nil, errors.New("INCRBY key delta")
		}
		d, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return nil, err
		}
		return h.eng.KV.Incr(args[0], d)

	case "KEYS":
		return h.eng.KV.Keys(), nil

	case "FLUSHDB", "FLUSHALL":
		h.eng.KV.FlushAll()
		return "OK", nil

	case "SEMANTIC_SET":
		if len(args) < 2 {
			return nil, errors.New("SEMANTIC_SET key value")
		}
		h.eng.Semantic.Set(args[0], args[1])
		return "OK", nil

	case "SEMANTIC_GET":
		if len(args) < 1 {
			return nil, errors.New("SEMANTIC_GET query")
		}
		v, score, ok := h.eng.Semantic.Get(args[0], float32(h.cfg.SemThreshold))
		h.eng.Metrics.RecordSemantic(ok)
		if !ok {
			return map[string]any{"hit": false, "score": score}, nil
		}
		return map[string]any{"hit": true, "value": v, "score": score}, nil

	case "CACHE_LLM":
		if len(args) < 2 {
			return nil, errors.New("CACHE_LLM prompt response")
		}
		h.eng.LLM.Set(args[0], args[1])
		return "OK", nil

	case "CACHE_LLM_GET":
		if len(args) < 1 {
			return nil, errors.New("CACHE_LLM_GET prompt")
		}
		v, score, ok := h.eng.LLM.Get(args[0], 0.88)
		h.eng.Metrics.RecordLLM(ok)
		if !ok {
			return map[string]any{"hit": false, "score": score}, nil
		}
		return map[string]any{"hit": true, "response": v, "score": score}, nil

	case "CACHE_LLM_STATS":
		return h.eng.LLM.Stats(), nil

	case "MEMORY_ADD":
		if len(args) < 2 {
			return nil, errors.New("MEMORY_ADD user text")
		}
		return h.eng.Memory.Add(args[0], strings.Join(args[1:], " "), nil), nil

	case "MEMORY_QUERY":
		if len(args) < 2 {
			return nil, errors.New("MEMORY_QUERY user query")
		}
		hits := h.eng.Memory.Query(args[0], strings.Join(args[1:], " "), 5, 0.3)
		return map[string]any{"hits": hits, "context": memory.Synthesize(hits)}, nil

	case "MEMORY_LIST":
		if len(args) < 1 {
			return nil, errors.New("MEMORY_LIST user")
		}
		return h.eng.Memory.List(args[0]), nil

	case "INFO":
		return h.eng.Info(), nil
	}

	return nil, errors.New("unknown command: " + cmd)
}
