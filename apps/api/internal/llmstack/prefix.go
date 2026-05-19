package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// PrefixRouter tracks which LLM-serving workers have which prompt
// prefixes warm in their KV cache. Modern serving stacks (vLLM,
// TGI, SGLang) reuse the KV-cache when the prompt prefix matches an
// already-computed one — frequently a 5-10x speedup on the prefill
// phase. But this only helps if your routing layer KNOWS which
// worker has the prefix loaded. Random / round-robin routing leaves
// most of that win on the table.
//
// PREFIX.* gives the cache a single coordination point:
//
//   PREFIX.REGISTER prefix-hash worker-id [TTL ms]
//        Worker just processed a prompt with this prefix. Other
//        workers can still serve it, but THIS worker has the KV
//        cache already populated.
//
//   PREFIX.LOOKUP prefix-hash
//        Returns the list of workers that have the prefix warm,
//        ordered by most-recently-registered (LRU). Apps pick the
//        first as their routing target.
//
//   PREFIX.HASH text         → sha256 prefix of the text (16 hex
//                              chars). Apps usually hash the
//                              system-prompt + few-shot block, NOT
//                              the per-request tail.
//
//   PREFIX.FORGET prefix-hash [WORKER w]
//        Drop the prefix entirely or just one worker's claim.
//
//   PREFIX.EVICT worker-id   Worker is going away — drop ALL its
//                            prefix claims in one call.
//
//   PREFIX.STATS             hits / misses / hit_rate, drives the
//                            "how much KV-cache reuse are we
//                            getting" dashboard.
//
// Implementation:
//   - sync.Map[prefix_hash] -> *prefixEntry{workers: sync.Map[worker_id]registeredAt}
//   - Atomic counters. LOOKUP is two sync.Map reads + a small sort
//     by registered-at — sub-microsecond at typical N (~100 workers).
//   - Per-entry TTL via lazy expiry on LOOKUP (cheap).
//
// Storage is bounded by (number of distinct prefix hashes) × (number
// of workers per prefix). At the scale of typical agentic apps —
// thousands of prefixes, tens of workers — memory is trivial.
type PrefixRouter struct {
	entries sync.Map // prefix_hash -> *prefixEntry

	totalLookups atomic.Int64
	totalHits    atomic.Int64
	totalMisses  atomic.Int64
	totalRegisters atomic.Int64
	totalEvictions atomic.Int64
}

type prefixEntry struct {
	workers sync.Map // worker_id -> *workerClaim
	// We also keep a single atomic counter of active workers for
	// LOOKUP's fast-path "is anyone home?" check.
	count atomic.Int32
}

type workerClaim struct {
	registeredAtNS int64
	expiresAtNS    int64 // 0 = no expiry
}

// NewPrefixRouter returns an empty router.
func NewPrefixRouter() *PrefixRouter {
	return &PrefixRouter{}
}

// HashPrefix returns a stable 16-hex-char prefix hash from `text`.
// Apps usually hash the system-prompt + few-shot block. Computed
// client-side OR by calling PREFIX.HASH on the cache.
func HashPrefix(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:8])
}

// Register records that `worker` has `prefixHash` warm. Replaces
// any previous registration for the same (prefix, worker) pair.
// TTL <= 0 means no expiry (worker explicitly evicts).
func (p *PrefixRouter) Register(prefixHash, worker string, ttl time.Duration) {
	p.totalRegisters.Add(1)
	v, _ := p.entries.LoadOrStore(prefixHash, &prefixEntry{})
	e := v.(*prefixEntry)
	now := time.Now().UnixNano()
	exp := int64(0)
	if ttl > 0 {
		exp = now + ttl.Nanoseconds()
	}
	_, existed := e.workers.LoadOrStore(worker, &workerClaim{
		registeredAtNS: now,
		expiresAtNS:    exp,
	})
	if !existed {
		e.count.Add(1)
	} else {
		// Already present — refresh in place.
		actual, _ := e.workers.Load(worker)
		c := actual.(*workerClaim)
		c.registeredAtNS = now
		c.expiresAtNS = exp
	}
}

// WorkerRow is one row of LOOKUP.
type WorkerRow struct {
	Worker         string `json:"worker"`
	RegisteredAtMS int64  `json:"registered_at_ms"`
	AgeMS          int64  `json:"age_ms"`
}

// Lookup returns workers with the prefix warm, ordered MOST recently
// registered first (LRU-front). Expired claims are lazily dropped.
func (p *PrefixRouter) Lookup(prefixHash string) []WorkerRow {
	p.totalLookups.Add(1)
	v, ok := p.entries.Load(prefixHash)
	if !ok {
		p.totalMisses.Add(1)
		return nil
	}
	e := v.(*prefixEntry)
	now := time.Now().UnixNano()
	rows := make([]WorkerRow, 0, 8)
	e.workers.Range(func(k, v any) bool {
		c := v.(*workerClaim)
		if c.expiresAtNS != 0 && now > c.expiresAtNS {
			e.workers.Delete(k)
			e.count.Add(-1)
			return true
		}
		rows = append(rows, WorkerRow{
			Worker:         k.(string),
			RegisteredAtMS: c.registeredAtNS / int64(time.Millisecond),
			AgeMS:          (now - c.registeredAtNS) / int64(time.Millisecond),
		})
		return true
	})
	if len(rows) == 0 {
		p.totalMisses.Add(1)
		// All claims expired — drop the entry entirely.
		p.entries.Delete(prefixHash)
		return nil
	}
	p.totalHits.Add(1)
	sort.Slice(rows, func(i, j int) bool { return rows[i].RegisteredAtMS > rows[j].RegisteredAtMS })
	return rows
}

// Forget drops one prefix (worker="") or one (prefix, worker) claim.
// Returns true if anything was dropped.
func (p *PrefixRouter) Forget(prefixHash, worker string) bool {
	if worker == "" {
		_, was := p.entries.LoadAndDelete(prefixHash)
		return was
	}
	v, ok := p.entries.Load(prefixHash)
	if !ok {
		return false
	}
	e := v.(*prefixEntry)
	_, was := e.workers.LoadAndDelete(worker)
	if was {
		e.count.Add(-1)
		if e.count.Load() == 0 {
			p.entries.Delete(prefixHash)
		}
	}
	return was
}

// EvictWorker drops every prefix claim held by `worker`. Used when
// a worker is shutting down (graceful) or detected dead (heartbeat
// timeout). Returns the number of prefix claims dropped.
func (p *PrefixRouter) EvictWorker(worker string) int {
	dropped := 0
	p.entries.Range(func(k, v any) bool {
		e := v.(*prefixEntry)
		if _, was := e.workers.LoadAndDelete(worker); was {
			dropped++
			e.count.Add(-1)
			if e.count.Load() == 0 {
				p.entries.Delete(k)
			}
		}
		return true
	})
	if dropped > 0 {
		p.totalEvictions.Add(int64(dropped))
	}
	return dropped
}

// PrefixStats is the global counters snapshot.
type PrefixStats struct {
	Prefixes       int     `json:"prefixes"`
	TotalLookups   int64   `json:"total_lookups"`
	TotalHits      int64   `json:"total_hits"`
	TotalMisses    int64   `json:"total_misses"`
	TotalRegisters int64   `json:"total_registers"`
	TotalEvictions int64   `json:"total_evictions"`
	HitRate        float64 `json:"hit_rate"`
}

func (p *PrefixRouter) Stats() PrefixStats {
	n := 0
	p.entries.Range(func(_, _ any) bool { n++; return true })
	lookups := p.totalLookups.Load()
	hits := p.totalHits.Load()
	rate := 0.0
	if lookups > 0 {
		rate = float64(hits) / float64(lookups)
	}
	return PrefixStats{
		Prefixes:       n,
		TotalLookups:   lookups,
		TotalHits:      hits,
		TotalMisses:    p.totalMisses.Load(),
		TotalRegisters: p.totalRegisters.Load(),
		TotalEvictions: p.totalEvictions.Load(),
		HitRate:        rate,
	}
}

// Prefixes returns every registered prefix-hash and its worker count.
// Useful for dashboards and debugging.
type PrefixRow struct {
	PrefixHash string `json:"prefix_hash"`
	Workers    int    `json:"workers"`
}

func (p *PrefixRouter) Prefixes() []PrefixRow {
	out := []PrefixRow{}
	p.entries.Range(func(k, v any) bool {
		e := v.(*prefixEntry)
		out = append(out, PrefixRow{
			PrefixHash: k.(string),
			Workers:    int(e.count.Load()),
		})
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Workers > out[j].Workers })
	return out
}
