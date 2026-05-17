package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// AIWALRegistry is the per-primitive write-ahead log. The honest
// framing of why this exists:
//
//   NeuroCache has a global AOF for the KV layer. Each AI primitive
//   (TRUST, MARKET, SETTLEMENT, …) has its own in-memory state that
//   the global AOF replays by replaying the dispatch path. That works
//   for the simple "one mutation = one command" shape. It does NOT
//   work cleanly for primitives that:
//
//     - Want their own ordered recovery with per-primitive checkpoints
//       (so MARKET doesn't have to replay six months of bids on boot
//       just to reach the current clearing state).
//
//     - Participate in XTXN as 2PC participants and need durable
//       prepare-state independent of the global AOF's commit point.
//
//     - Want compaction semantics tied to their own state machine
//       (e.g. NETTING wants to truncate the obligation log once a
//       cycle has applied + reconciled).
//
// AIWAL is the explicit, opt-in WAL contract those primitives can
// use. It is NOT a replacement for the global AOF; it is a per-
// primitive layer that sits alongside.
//
// What this primitive does NOT do, honestly:
//
//   - It is not on-disk durable in this implementation. Entries
//     live in process memory, with hooks for callers to checkpoint
//     to external durable storage. Wiring the on-disk WAL is a
//     deployment concern; this primitive owns the protocol, not the
//     filesystem layout. For real durability, the engine's AOF
//     subsystem already handles disk — AIWAL is for the
//     in-process ordering + recovery contract.
//
//   - It is not magic. APPEND followed by FSYNC is a *commitment*
//     boundary the primitive can rely on for its own recovery
//     semantics. The actual fsync, if you want one, is at the AOF
//     level outside this primitive.
//
//   - It is not transactional across multiple primitives — XTXN is
//     for that.
//
// Commands:
//
//   AIWAL.APPEND primitive entry
//        → seq (monotonic per-primitive)
//   AIWAL.FSYNC primitive
//        Marks current head as the "fsynced" boundary. Recovery
//        replays only up to the fsynced head.
//   AIWAL.READ primitive [FROM seq] [LIMIT n]
//   AIWAL.CHECKPOINT primitive seq state-blob
//        Mark the primitive's state-as-of-seq. Subsequent RECOVER
//        returns this blob + every entry > seq.
//   AIWAL.RECOVER primitive
//        → (checkpoint_seq, checkpoint_blob, replay-log)
//        The primitive applies the checkpoint then replays the log.
//   AIWAL.TRUNCATE primitive UPTO seq
//        Drop entries with seq <= upto. Caller is responsible for
//        ensuring the corresponding checkpoint exists.
//   AIWAL.STATUS primitive
//   AIWAL.LIST
//   AIWAL.FORGET primitive|ALL
//   AIWAL.STATS
//
// Hot path: APPEND is O(1). RECOVER is O(log + checkpoint_blob).
type AIWALRegistry struct {
	mu   sync.RWMutex
	wals map[string]*aiwalLog

	totalAppends     atomic.Int64
	totalReads       atomic.Int64
	totalCheckpoints atomic.Int64
	totalRecovers    atomic.Int64
	totalTruncates   atomic.Int64
}

type aiwalLog struct {
	mu          sync.Mutex
	entries     []aiwalEntry
	nextSeq     int64
	fsyncedSeq  int64 // head seq that survived an FSYNC
	checkpoint  *aiwalCheckpoint
	createdAt   time.Time
}

type aiwalEntry struct {
	Seq  int64
	At   time.Time
	Data string
}

type aiwalCheckpoint struct {
	Seq  int64
	Blob string
	At   time.Time
}

// NewAIWALRegistry returns an empty registry.
func NewAIWALRegistry() *AIWALRegistry {
	return &AIWALRegistry{wals: map[string]*aiwalLog{}}
}

// AIWALAppendResult is APPEND's return.
type AIWALAppendResult struct {
	Seq int64 `json:"seq"`
}

// Append adds one entry. entry is opaque to us; the primitive owns
// the serialisation format.
func (a *AIWALRegistry) Append(primitive, entry string) (AIWALAppendResult, error) {
	if primitive == "" {
		return AIWALAppendResult{}, errors.New("primitive required")
	}
	if entry == "" {
		return AIWALAppendResult{}, errors.New("entry required")
	}
	a.totalAppends.Add(1)
	w := a.walOrCreate(primitive)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nextSeq++
	w.entries = append(w.entries, aiwalEntry{
		Seq: w.nextSeq, At: time.Now(), Data: entry,
	})
	return AIWALAppendResult{Seq: w.nextSeq}, nil
}

// Fsync marks the current head as the fsynced boundary. This is the
// commit point — RECOVER replays only entries with seq <= fsynced.
// The actual disk fsync is the engine's AOF concern, not ours; this
// primitive owns the *ordering* contract.
func (a *AIWALRegistry) Fsync(primitive string) (int64, error) {
	if primitive == "" {
		return 0, errors.New("primitive required")
	}
	a.mu.RLock()
	w, ok := a.wals[primitive]
	a.mu.RUnlock()
	if !ok {
		return 0, errors.New("unknown primitive: " + primitive)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fsyncedSeq = w.nextSeq
	return w.fsyncedSeq, nil
}

// AIWALEntryRow is one row of READ.
type AIWALEntryRow struct {
	Seq    int64  `json:"seq"`
	AtUnix int64  `json:"at_unix"`
	Data   string `json:"data"`
}

// Read returns entries. By default returns all fsynced entries; pass
// limit to cap. From=0 means start at seq 1.
func (a *AIWALRegistry) Read(primitive string, from int64, limit int) ([]AIWALEntryRow, bool) {
	if primitive == "" {
		return nil, false
	}
	if limit <= 0 {
		limit = 1000
	}
	a.totalReads.Add(1)
	a.mu.RLock()
	w, ok := a.wals[primitive]
	a.mu.RUnlock()
	if !ok {
		return nil, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]AIWALEntryRow, 0, limit)
	for _, e := range w.entries {
		if e.Seq < from {
			continue
		}
		out = append(out, AIWALEntryRow{
			Seq: e.Seq, AtUnix: e.At.Unix(), Data: e.Data,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, true
}

// Checkpoint stores a state-as-of-seq blob. The primitive computes
// the blob from its own state machine after applying entries up to
// seq. Subsequent RECOVER returns this blob + entries after seq.
func (a *AIWALRegistry) Checkpoint(primitive string, seq int64, blob string) error {
	if primitive == "" {
		return errors.New("primitive required")
	}
	if seq < 0 {
		return errors.New("seq must be non-negative")
	}
	a.totalCheckpoints.Add(1)
	w := a.walOrCreate(primitive)
	w.mu.Lock()
	defer w.mu.Unlock()
	if seq > w.nextSeq {
		return errors.New("checkpoint seq exceeds head")
	}
	w.checkpoint = &aiwalCheckpoint{Seq: seq, Blob: blob, At: time.Now()}
	return nil
}

// AIWALRecoverResult is RECOVER's return.
type AIWALRecoverResult struct {
	Primitive     string          `json:"primitive"`
	CheckpointSeq int64           `json:"checkpoint_seq"`
	CheckpointBlob string         `json:"checkpoint_blob"`
	Replay        []AIWALEntryRow `json:"replay"`
	FsyncedHead   int64           `json:"fsynced_head"`
}

// Recover returns the checkpoint + every fsynced entry after the
// checkpoint. If no checkpoint exists, the replay is the full log
// (every entry from seq 1).
func (a *AIWALRegistry) Recover(primitive string) (AIWALRecoverResult, bool) {
	if primitive == "" {
		return AIWALRecoverResult{}, false
	}
	a.totalRecovers.Add(1)
	a.mu.RLock()
	w, ok := a.wals[primitive]
	a.mu.RUnlock()
	if !ok {
		return AIWALRecoverResult{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := AIWALRecoverResult{Primitive: primitive, FsyncedHead: w.fsyncedSeq}
	startSeq := int64(0)
	if w.checkpoint != nil {
		out.CheckpointSeq = w.checkpoint.Seq
		out.CheckpointBlob = w.checkpoint.Blob
		startSeq = w.checkpoint.Seq
	}
	for _, e := range w.entries {
		if e.Seq <= startSeq {
			continue
		}
		if e.Seq > w.fsyncedSeq {
			break
		}
		out.Replay = append(out.Replay, AIWALEntryRow{
			Seq: e.Seq, AtUnix: e.At.Unix(), Data: e.Data,
		})
	}
	return out, true
}

// Truncate drops entries with seq <= upto. The caller is responsible
// for ensuring the corresponding checkpoint exists; we refuse to
// truncate past the checkpoint as a safety check.
func (a *AIWALRegistry) Truncate(primitive string, upto int64) (int, error) {
	if primitive == "" {
		return 0, errors.New("primitive required")
	}
	a.totalTruncates.Add(1)
	a.mu.RLock()
	w, ok := a.wals[primitive]
	a.mu.RUnlock()
	if !ok {
		return 0, errors.New("unknown primitive: " + primitive)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.checkpoint == nil || w.checkpoint.Seq < upto {
		return 0, errors.New("refusing to truncate past checkpoint (checkpoint first)")
	}
	dropped := 0
	keep := make([]aiwalEntry, 0, len(w.entries))
	for _, e := range w.entries {
		if e.Seq <= upto {
			dropped++
			continue
		}
		keep = append(keep, e)
	}
	w.entries = keep
	return dropped, nil
}

// AIWALStatus is STATUS's return.
type AIWALStatus struct {
	Primitive     string `json:"primitive"`
	HeadSeq       int64  `json:"head_seq"`
	FsyncedSeq    int64  `json:"fsynced_seq"`
	CheckpointSeq int64  `json:"checkpoint_seq"`
	EntryCount    int    `json:"entry_count"`
}

// Status returns the per-primitive snapshot.
func (a *AIWALRegistry) Status(primitive string) (AIWALStatus, bool) {
	a.mu.RLock()
	w, ok := a.wals[primitive]
	a.mu.RUnlock()
	if !ok {
		return AIWALStatus{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	out := AIWALStatus{
		Primitive: primitive,
		HeadSeq:    w.nextSeq,
		FsyncedSeq: w.fsyncedSeq,
		EntryCount: len(w.entries),
	}
	if w.checkpoint != nil {
		out.CheckpointSeq = w.checkpoint.Seq
	}
	return out, true
}

// List returns every primitive that has a WAL.
func (a *AIWALRegistry) List() []string {
	a.mu.RLock()
	out := make([]string, 0, len(a.wals))
	for k := range a.wals {
		out = append(out, k)
	}
	a.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Forget drops a WAL (or all). Destructive; obviously.
func (a *AIWALRegistry) Forget(primitive string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if primitive == "ALL" {
		n := len(a.wals)
		a.wals = map[string]*aiwalLog{}
		return n
	}
	if _, ok := a.wals[primitive]; ok {
		delete(a.wals, primitive)
		return 1
	}
	return 0
}

// AIWALStats is the global snapshot.
type AIWALStats struct {
	WALs             int   `json:"wals"`
	TotalAppends     int64 `json:"total_appends"`
	TotalReads       int64 `json:"total_reads"`
	TotalCheckpoints int64 `json:"total_checkpoints"`
	TotalRecovers    int64 `json:"total_recovers"`
	TotalTruncates   int64 `json:"total_truncates"`
}

func (a *AIWALRegistry) Stats() AIWALStats {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return AIWALStats{
		WALs: len(a.wals),
		TotalAppends: a.totalAppends.Load(),
		TotalReads: a.totalReads.Load(),
		TotalCheckpoints: a.totalCheckpoints.Load(),
		TotalRecovers: a.totalRecovers.Load(),
		TotalTruncates: a.totalTruncates.Load(),
	}
}

func (a *AIWALRegistry) walOrCreate(name string) *aiwalLog {
	a.mu.RLock()
	w, ok := a.wals[name]
	a.mu.RUnlock()
	if ok {
		return w
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if w, ok := a.wals[name]; ok {
		return w
	}
	w = &aiwalLog{createdAt: time.Now()}
	a.wals[name] = w
	return w
}
