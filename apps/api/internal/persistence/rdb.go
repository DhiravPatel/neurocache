package persistence

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RDB is a point-in-time binary snapshot. We use JSON + gzip rather than
// Redis's proprietary binary format — it's portable, debuggable, and
// plenty fast for the workloads this engine targets.

// Snapshot is the on-disk format. Typed per-key bodies let us reconstruct
// lists/hashes/sets/zsets/streams without schema guessing.
type Snapshot struct {
	Version   int           `json:"version"`
	CreatedAt time.Time     `json:"created_at"`
	Keys      []KeySnapshot `json:"keys"`
}

// KeySnapshot is one key's value + TTL. Only the field matching Type is
// populated — the encoder uses omitempty so unused fields never pay
// their byte cost.
type KeySnapshot struct {
	Key      string              `json:"k"`
	Type     string              `json:"t"`
	ExpireAt int64               `json:"x,omitempty"` // unix-milli, 0 = no expiry
	Str      string              `json:"s,omitempty"`
	List     []string            `json:"l,omitempty"`
	Hash     map[string]string   `json:"h,omitempty"`
	Set      []string            `json:"m,omitempty"`
	ZSet     []ZMember           `json:"z,omitempty"`
	Stream   []StreamSnapshotEntry `json:"sx,omitempty"`
}

// ZMember is one sorted-set entry in the snapshot.
type ZMember struct {
	Member string  `json:"m"`
	Score  float64 `json:"s"`
}

// StreamSnapshotEntry is one stream entry (id + flat field-value list).
type StreamSnapshotEntry struct {
	ID     string   `json:"i"`
	Fields []string `json:"f"`
}

// RDB owns the snapshot file and an optional background ticker that
// writes one out every N seconds.
type RDB struct {
	path     string
	interval time.Duration
	mu       sync.Mutex
	stopCh   chan struct{}
	doneCh   chan struct{}
	snapFn   func() Snapshot
}

// OpenRDB prepares the target path (creating the directory). Call Start
// to begin the periodic-write loop; call SaveNow for an on-demand dump.
func OpenRDB(path string, interval time.Duration, snapFn func() Snapshot) (*RDB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &RDB{
		path:     path,
		interval: interval,
		snapFn:   snapFn,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}, nil
}

// Start launches the periodic-snapshot goroutine. A zero interval keeps
// snapshots manual (SaveNow only).
func (r *RDB) Start() {
	if r == nil || r.interval <= 0 {
		close(r.doneCh)
		return
	}
	go r.loop()
}

func (r *RDB) loop() {
	defer close(r.doneCh)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-r.stopCh:
			_ = r.SaveNow()
			return
		case <-t.C:
			if err := r.SaveNow(); err != nil {
				fmt.Fprintf(os.Stderr, "rdb: snapshot failed: %v\n", err)
			}
		}
	}
}

// Stop flushes a final snapshot and returns once the loop exits.
func (r *RDB) Stop() {
	if r == nil {
		return
	}
	close(r.stopCh)
	<-r.doneCh
}

// SaveNow writes the snapshot to a temp file and atomically renames it.
// The two-step dance means a crash during write can't corrupt the file.
func (r *RDB) SaveNow() error {
	if r == nil || r.snapFn == nil {
		return nil
	}
	snap := r.snapFn()
	tmp := r.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	enc := json.NewEncoder(gz)
	if err := enc.Encode(snap); err != nil {
		_ = gz.Close()
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := gz.Close(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return os.Rename(tmp, r.path)
}

// LoadRDB reads a snapshot from path; missing file is OK (returns nil).
func LoadRDB(path string) (*Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	var snap Snapshot
	if err := json.NewDecoder(gz).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}
