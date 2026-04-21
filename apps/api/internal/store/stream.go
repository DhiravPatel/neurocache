package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StreamID is the Redis stream identifier: milliseconds + per-ms sequence.
type StreamID struct {
	Ms  uint64
	Seq uint64
}

func (id StreamID) String() string { return fmt.Sprintf("%d-%d", id.Ms, id.Seq) }

// Less compares two IDs in the natural total order.
func (id StreamID) Less(o StreamID) bool {
	if id.Ms != o.Ms {
		return id.Ms < o.Ms
	}
	return id.Seq < o.Seq
}

// ParseStreamID decodes "ms-seq" or "ms" forms. The sequence defaults to
// the lower bound when parsing "min" / "-", and to the upper bound when
// parsing "max" / "+", matching Redis semantics for XRANGE bounds.
func ParseStreamID(s string, upper bool) (StreamID, error) {
	switch s {
	case "-":
		return StreamID{0, 0}, nil
	case "+":
		return StreamID{^uint64(0), ^uint64(0)}, nil
	case "$":
		// "$" means "the current tail"; callers substitute the stream's
		// last ID before calling — here we just return zero.
		return StreamID{}, nil
	}
	parts := strings.SplitN(s, "-", 2)
	ms, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return StreamID{}, errors.New("invalid stream ID")
	}
	if len(parts) == 1 {
		if upper {
			return StreamID{ms, ^uint64(0)}, nil
		}
		return StreamID{ms, 0}, nil
	}
	seq, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return StreamID{}, errors.New("invalid stream ID")
	}
	return StreamID{ms, seq}, nil
}

// StreamEntry is one appended record: an ID plus alternating field/value
// pairs kept in insertion order for deterministic iteration.
type StreamEntry struct {
	ID     StreamID
	Fields []string // [f1 v1 f2 v2 ...]
}

// Stream is an append-only log keyed by monotonically-increasing IDs.
// Entries are stored in insertion order so XRANGE / XREAD are O(n).
// A production rewrite could index by ID via a radix tree to cut
// XRANGE to O(log n + k), but a slice is fine for realistic workloads.
type Stream struct {
	mu       sync.Mutex
	entries  []StreamEntry
	lastID   StreamID
	waiters  []chan struct{} // XREAD BLOCK clients
	maxWaitN int             // cap so bad clients can't leak memory
}

func newStream() *Stream { return &Stream{maxWaitN: 1024} }

func (s *Stream) approxBytes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.entries {
		for _, f := range e.Fields {
			n += len(f)
		}
		n += 16
	}
	return n
}

// generateID picks the next ID. "*" means "auto" — pick max(lastID+1,
// now()). An explicit ID must strictly exceed the last one or we error.
func (s *Stream) generateID(explicit string) (StreamID, error) {
	if explicit == "" || explicit == "*" {
		now := uint64(time.Now().UnixMilli())
		id := StreamID{Ms: now, Seq: 0}
		if !s.lastID.Less(id) {
			id = StreamID{Ms: s.lastID.Ms, Seq: s.lastID.Seq + 1}
		}
		return id, nil
	}
	id, err := ParseStreamID(explicit, false)
	if err != nil {
		return StreamID{}, err
	}
	// explicit MS with no sequence -> auto-sequence
	if !strings.Contains(explicit, "-") {
		if id.Ms == s.lastID.Ms {
			id.Seq = s.lastID.Seq + 1
		}
	}
	if !s.lastID.Less(id) {
		return StreamID{}, errors.New("The ID specified in XADD is equal or smaller than the target stream top item")
	}
	return id, nil
}

// append adds a new entry, signaling any XREAD BLOCK waiters.
func (s *Stream) append(id StreamID, fields []string) {
	s.mu.Lock()
	s.entries = append(s.entries, StreamEntry{ID: id, Fields: fields})
	s.lastID = id
	// release all waiters; they re-query after waking
	for _, ch := range s.waiters {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.waiters = s.waiters[:0]
	s.mu.Unlock()
}

// rangeEntries returns entries with IDs in [min,max], up to count (0 =
// unlimited). reverse walks from the tail.
func (s *Stream) rangeEntries(min, max StreamID, count int, reverse bool) []StreamEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []StreamEntry{}
	if reverse {
		for i := len(s.entries) - 1; i >= 0; i-- {
			e := s.entries[i]
			if e.ID.Less(min) {
				break
			}
			if max.Less(e.ID) {
				continue
			}
			out = append(out, e)
			if count > 0 && len(out) >= count {
				break
			}
		}
	} else {
		for _, e := range s.entries {
			if e.ID.Less(min) {
				continue
			}
			if max.Less(e.ID) {
				break
			}
			out = append(out, e)
			if count > 0 && len(out) >= count {
				break
			}
		}
	}
	return out
}

// trim keeps the newest 'maxLen' entries (MAXLEN ~N capping).
func (s *Stream) trim(maxLen int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if maxLen < 0 || len(s.entries) <= maxLen {
		return 0
	}
	removed := len(s.entries) - maxLen
	s.entries = s.entries[removed:]
	return removed
}

// del removes entries whose IDs are in the provided set; returns count.
func (s *Stream) del(ids []StreamID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[StreamID]struct{}{}
	for _, id := range ids {
		set[id] = struct{}{}
	}
	kept := s.entries[:0]
	removed := 0
	for _, e := range s.entries {
		if _, drop := set[e.ID]; drop {
			removed++
			continue
		}
		kept = append(kept, e)
	}
	s.entries = kept
	return removed
}

// len returns the current entry count.
func (s *Stream) length() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// ─── store-level API ───────────────────────────────────────────────────

// XAdd appends an entry. ID == "*" auto-generates; otherwise must be
// strictly greater than the last ID. maxLen > 0 trims the stream to that
// cap after insertion. Returns the assigned ID.
func (s *Store) XAdd(key, id string, fields []string, maxLen int) (string, error) {
	if len(fields) == 0 || len(fields)%2 != 0 {
		return "", errors.New("XADD requires at least one field/value pair")
	}
	s.mu.Lock()
	e, err := s.getOrCreate(key, TypeStream)
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	assigned, err := e.Stream.generateID(id)
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	e.Stream.append(assigned, fields)
	if maxLen > 0 {
		e.Stream.trim(maxLen)
	}
	s.recomputeBytes(e)
	s.mu.Unlock()
	s.fire("xadd", key)
	return assigned.String(), nil
}

// XLen returns stream length (0 if missing).
func (s *Store) XLen(key string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeStream)
	if err != nil || !ok {
		return 0, err
	}
	return e.Stream.length(), nil
}

// XRange returns entries whose IDs fall in [startStr,endStr]. count == 0
// means unlimited.
func (s *Store) XRange(key, startStr, endStr string, count int, reverse bool) ([]StreamEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeStream)
	if err != nil || !ok {
		return nil, err
	}
	low, err := ParseStreamID(startStr, false)
	if err != nil {
		return nil, err
	}
	high, err := ParseStreamID(endStr, true)
	if err != nil {
		return nil, err
	}
	if reverse {
		low, high = high, low
		// swap so high >= low invariant holds for the scan
		if high.Less(low) {
			low, high = high, low
		}
	}
	return e.Stream.rangeEntries(low, high, count, reverse), nil
}

// XDel removes entries by ID; returns removed count.
func (s *Store) XDel(key string, ids ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeStream)
	if err != nil || !ok {
		return 0, err
	}
	parsed := make([]StreamID, 0, len(ids))
	for _, raw := range ids {
		id, err := ParseStreamID(raw, false)
		if err != nil {
			return 0, err
		}
		parsed = append(parsed, id)
	}
	n := e.Stream.del(parsed)
	s.recomputeBytes(e)
	return n, nil
}

// XTrim caps the stream at maxLen; returns number of deleted entries.
func (s *Store) XTrim(key string, maxLen int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeStream)
	if err != nil || !ok {
		return 0, err
	}
	n := e.Stream.trim(maxLen)
	s.recomputeBytes(e)
	return n, nil
}

// XRead returns entries newer than `lastIDs[i]` for each stream key.
// This is the non-blocking flavour; the RESP layer implements BLOCK by
// calling this in a loop with its own timeout.
func (s *Store) XRead(keys, lastIDs []string, count int) (map[string][]StreamEntry, error) {
	if len(keys) != len(lastIDs) {
		return nil, errors.New("XREAD: keys and IDs must be the same length")
	}
	out := map[string][]StreamEntry{}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, k := range keys {
		e, ok, err := s.get(k, TypeStream)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		raw := lastIDs[i]
		var after StreamID
		if raw == "$" {
			after = e.Stream.lastID
		} else {
			after, err = ParseStreamID(raw, false)
			if err != nil {
				return nil, err
			}
		}
		// "after" is *exclusive* — advance sequence by one so we skip it.
		start := StreamID{Ms: after.Ms, Seq: after.Seq + 1}
		if after.Seq == ^uint64(0) {
			start = StreamID{Ms: after.Ms + 1, Seq: 0}
		}
		entries := e.Stream.rangeEntries(start, StreamID{^uint64(0), ^uint64(0)}, count, false)
		if len(entries) > 0 {
			out[k] = entries
		}
	}
	return out, nil
}

// XLast returns the latest ID for a stream (for XREAD "$" bookkeeping).
func (s *Store) XLast(key string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeStream)
	if err != nil || !ok {
		return "", false, err
	}
	return e.Stream.lastID.String(), true, nil
}
