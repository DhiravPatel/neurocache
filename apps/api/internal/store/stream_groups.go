package store

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ConsumerGroup is one named cooperative reader on a stream. Multiple
// consumers in the same group share an "ever-advancing" cursor so each
// new entry is delivered to exactly one of them. Per-consumer state
// (PEL) tracks entries dispatched but not yet ACKed.
type ConsumerGroup struct {
	Name       string
	LastID     StreamID
	Consumers  map[string]*Consumer
	Pending    map[StreamID]*PendingEntry // groupwide PEL
	EntriesRead int64

	// Operational tunables set via XCFGSET — both default to 0
	// meaning "no limit", so existing groups are unaffected.
	MaxDeliveries int64 // soft cap on per-ID retry count surfaced via XINFO
	MinIdleMs     int64 // floor on XAUTOCLAIM idle gating
}

// Consumer is one named reader inside a group.
type Consumer struct {
	Name    string
	Pending map[StreamID]struct{} // subset of group's PEL it owns
	SeenAt  time.Time
}

// PendingEntry tracks a single dispatched-but-unacked record so XPENDING
// /XCLAIM /XAUTOCLAIM can manage retries.
type PendingEntry struct {
	ID           StreamID
	Consumer     string
	DeliveredAt  time.Time
	DeliveryCount int64
}

// stream-side group helpers ────────────────────────────────────────────
//
// All of these run while the parent Stream's mu is held — callers are
// the store-level methods below. Keeping the locking discipline in one
// place (the public store API) avoids double-locking bugs.

func (s *Stream) groupCreate(name, idStr string, mkstream bool) error {
	if s.groups == nil {
		s.groups = map[string]*ConsumerGroup{}
	}
	if _, exists := s.groups[name]; exists {
		return errors.New("BUSYGROUP Consumer Group name already exists")
	}
	id, err := s.parseGroupStartID(idStr)
	if err != nil {
		return err
	}
	s.groups[name] = &ConsumerGroup{
		Name: name, LastID: id,
		Consumers: map[string]*Consumer{},
		Pending:   map[StreamID]*PendingEntry{},
	}
	_ = mkstream // honored by caller when the stream is missing
	return nil
}

func (s *Stream) groupDestroy(name string) bool {
	if _, ok := s.groups[name]; !ok {
		return false
	}
	delete(s.groups, name)
	return true
}

func (s *Stream) groupSetID(name, idStr string) error {
	g, ok := s.groups[name]
	if !ok {
		return errors.New("NOGROUP No such consumer group")
	}
	id, err := s.parseGroupStartID(idStr)
	if err != nil {
		return err
	}
	g.LastID = id
	return nil
}

func (s *Stream) groupCreateConsumer(group, consumer string) (bool, error) {
	g, ok := s.groups[group]
	if !ok {
		return false, errors.New("NOGROUP No such consumer group")
	}
	if _, exists := g.Consumers[consumer]; exists {
		return false, nil
	}
	g.Consumers[consumer] = &Consumer{Name: consumer, Pending: map[StreamID]struct{}{}, SeenAt: time.Now()}
	return true, nil
}

// groupDelConsumer drops a consumer and returns the number of pending
// entries that were owned by it (Redis returns this count).
func (s *Stream) groupDelConsumer(group, consumer string) (int, error) {
	g, ok := s.groups[group]
	if !ok {
		return 0, errors.New("NOGROUP No such consumer group")
	}
	c, ok := g.Consumers[consumer]
	if !ok {
		return 0, nil
	}
	n := len(c.Pending)
	for id := range c.Pending {
		delete(g.Pending, id)
	}
	delete(g.Consumers, consumer)
	return n, nil
}

// parseGroupStartID accepts "0", "$", or any explicit ms-seq.
func (s *Stream) parseGroupStartID(raw string) (StreamID, error) {
	if raw == "$" {
		return s.lastID, nil
	}
	if raw == "0" || raw == "" {
		return StreamID{0, 0}, nil
	}
	return ParseStreamID(raw, false)
}

// readGroup advances g.LastID and returns up to count new entries,
// recording them in the group + consumer PEL. The "noack" flag (Redis
// XREADGROUP NOACK) skips PEL bookkeeping — useful for fire-and-forget.
func (s *Stream) readGroup(group, consumer string, after StreamID, count int, noack, history bool) ([]StreamEntry, error) {
	g, ok := s.groups[group]
	if !ok {
		return nil, errors.New("NOGROUP No such consumer group")
	}
	cons, ok := g.Consumers[consumer]
	if !ok {
		cons = &Consumer{Name: consumer, Pending: map[StreamID]struct{}{}, SeenAt: time.Now()}
		g.Consumers[consumer] = cons
	}
	cons.SeenAt = time.Now()

	if history {
		// "0" / explicit ID — replay this consumer's PEL from `after`.
		ids := make([]StreamID, 0, len(cons.Pending))
		for id := range cons.Pending {
			if !id.Less(after) {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i].Less(ids[j]) })
		out := make([]StreamEntry, 0, len(ids))
		idx := map[StreamID]int{}
		for i, e := range s.entries {
			idx[e.ID] = i
		}
		for _, id := range ids {
			if i, ok := idx[id]; ok {
				out = append(out, s.entries[i])
				if count > 0 && len(out) >= count {
					break
				}
			}
		}
		return out, nil
	}

	start := StreamID{Ms: g.LastID.Ms, Seq: g.LastID.Seq + 1}
	if g.LastID.Seq == ^uint64(0) {
		start = StreamID{Ms: g.LastID.Ms + 1, Seq: 0}
	}
	// Inline scan — caller already holds s.mu, so calling rangeEntries
	// would deadlock on the same mutex.
	out := []StreamEntry{}
	for _, e := range s.entries {
		if e.ID.Less(start) {
			continue
		}
		out = append(out, e)
		if count > 0 && len(out) >= count {
			break
		}
	}
	for _, e := range out {
		if g.LastID.Less(e.ID) {
			g.LastID = e.ID
		}
		g.EntriesRead++
		if noack {
			continue
		}
		g.Pending[e.ID] = &PendingEntry{
			ID: e.ID, Consumer: consumer, DeliveredAt: time.Now(), DeliveryCount: 1,
		}
		cons.Pending[e.ID] = struct{}{}
	}
	return out, nil
}

func (s *Stream) ack(group string, ids []StreamID) (int, error) {
	g, ok := s.groups[group]
	if !ok {
		return 0, errors.New("NOGROUP No such consumer group")
	}
	n := 0
	for _, id := range ids {
		pe, ok := g.Pending[id]
		if !ok {
			continue
		}
		delete(g.Pending, id)
		if c, ok := g.Consumers[pe.Consumer]; ok {
			delete(c.Pending, id)
		}
		n++
	}
	return n, nil
}

// pending returns a summary [count, minID, maxID, [[consumer, count]...]]
// when no filters are passed, or the long-form rows when start/end is
// provided. Matches the XPENDING two-form shape.
func (s *Stream) pending(group string, summary bool, start, end StreamID, count int, consumer string) (any, error) {
	g, ok := s.groups[group]
	if !ok {
		return nil, errors.New("NOGROUP No such consumer group")
	}
	if summary {
		ids := make([]StreamID, 0, len(g.Pending))
		for id := range g.Pending {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i].Less(ids[j]) })
		min, max := "", ""
		if len(ids) > 0 {
			min = ids[0].String()
			max = ids[len(ids)-1].String()
		}
		byCons := map[string]int{}
		for _, pe := range g.Pending {
			byCons[pe.Consumer]++
		}
		consumers := make([][2]string, 0, len(byCons))
		for c, n := range byCons {
			consumers = append(consumers, [2]string{c, fmt.Sprintf("%d", n)})
		}
		sort.Slice(consumers, func(i, j int) bool { return consumers[i][0] < consumers[j][0] })
		return PendingSummary{Count: int64(len(g.Pending)), MinID: min, MaxID: max, Consumers: consumers}, nil
	}
	// Long form: filter by ID range + optional consumer.
	rows := make([]PendingDetail, 0, count)
	now := time.Now()
	ids := make([]StreamID, 0, len(g.Pending))
	for id := range g.Pending {
		if id.Less(start) || end.Less(id) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].Less(ids[j]) })
	for _, id := range ids {
		pe := g.Pending[id]
		if consumer != "" && pe.Consumer != consumer {
			continue
		}
		rows = append(rows, PendingDetail{
			ID: id.String(), Consumer: pe.Consumer,
			IdleMs: now.Sub(pe.DeliveredAt).Milliseconds(),
			DeliveryCount: pe.DeliveryCount,
		})
		if count > 0 && len(rows) >= count {
			break
		}
	}
	return rows, nil
}

// claim transfers PEL ownership to a new consumer. minIdleMs filters
// out entries idle for less than that. Returns the claimed entries.
func (s *Stream) claim(group, newConsumer string, minIdleMs int64, ids []StreamID, justIDs bool, idleMs int64, time int64, retry int64) ([]StreamEntry, []string, error) {
	g, ok := s.groups[group]
	if !ok {
		return nil, nil, errors.New("NOGROUP No such consumer group")
	}
	target, ok := g.Consumers[newConsumer]
	if !ok {
		target = &Consumer{Name: newConsumer, Pending: map[StreamID]struct{}{}, SeenAt: timeNow()}
		g.Consumers[newConsumer] = target
	}
	idx := map[StreamID]int{}
	for i, e := range s.entries {
		idx[e.ID] = i
	}
	out := []StreamEntry{}
	outIDs := []string{}
	now := timeNow()
	for _, id := range ids {
		pe, exists := g.Pending[id]
		if !exists {
			continue
		}
		if minIdleMs > 0 && now.Sub(pe.DeliveredAt).Milliseconds() < minIdleMs {
			continue
		}
		// detach from previous owner
		if prev, ok := g.Consumers[pe.Consumer]; ok {
			delete(prev.Pending, id)
		}
		pe.Consumer = newConsumer
		pe.DeliveredAt = now
		if idleMs > 0 {
			pe.DeliveredAt = now.Add(-time1ms(idleMs))
		}
		if retry > 0 {
			pe.DeliveryCount = retry
		} else {
			pe.DeliveryCount++
		}
		target.Pending[id] = struct{}{}

		if justIDs {
			outIDs = append(outIDs, id.String())
			continue
		}
		if i, ok := idx[id]; ok {
			out = append(out, s.entries[i])
		} else {
			// Entry was deleted from the stream — Redis returns just the
			// id with no fields in this case.
			out = append(out, StreamEntry{ID: id, Fields: nil})
		}
	}
	_ = time
	return out, outIDs, nil
}

// autoclaim sweeps the PEL starting at startID, claiming up to count
// entries idle for at least minIdleMs and reassigning them to consumer.
// Returns the entries plus the cursor for the next AUTOCLAIM call (or
// "0-0" when the scan completes).
func (s *Stream) autoclaim(group, consumer string, minIdleMs int64, startID StreamID, count int, justIDs bool) ([]StreamEntry, []string, string, []string, error) {
	g, ok := s.groups[group]
	if !ok {
		return nil, nil, "", nil, errors.New("NOGROUP No such consumer group")
	}
	target, ok := g.Consumers[consumer]
	if !ok {
		target = &Consumer{Name: consumer, Pending: map[StreamID]struct{}{}, SeenAt: timeNow()}
		g.Consumers[consumer] = target
	}
	if count <= 0 {
		count = 100
	}
	ids := make([]StreamID, 0, len(g.Pending))
	for id := range g.Pending {
		if id.Less(startID) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].Less(ids[j]) })
	idx := map[StreamID]int{}
	for i, e := range s.entries {
		idx[e.ID] = i
	}
	out := []StreamEntry{}
	outIDs := []string{}
	deleted := []string{}
	now := timeNow()
	scanned := 0
	cursor := StreamID{0, 0}
	for _, id := range ids {
		if scanned >= count {
			cursor = id
			break
		}
		pe := g.Pending[id]
		if now.Sub(pe.DeliveredAt).Milliseconds() < minIdleMs {
			continue
		}
		i, exists := idx[id]
		if !exists {
			// Entry was deleted from the stream — drop from PEL too.
			delete(g.Pending, id)
			if prev, ok := g.Consumers[pe.Consumer]; ok {
				delete(prev.Pending, id)
			}
			deleted = append(deleted, id.String())
			scanned++
			continue
		}
		// reassign
		if prev, ok := g.Consumers[pe.Consumer]; ok {
			delete(prev.Pending, id)
		}
		pe.Consumer = consumer
		pe.DeliveredAt = now
		pe.DeliveryCount++
		target.Pending[id] = struct{}{}

		if justIDs {
			outIDs = append(outIDs, id.String())
		} else {
			out = append(out, s.entries[i])
		}
		scanned++
	}
	cursorStr := "0-0"
	if cursor.Ms != 0 || cursor.Seq != 0 {
		cursorStr = cursor.String()
	}
	return out, outIDs, cursorStr, deleted, nil
}

// Snapshot helpers used by XINFO. Each returns a flat string list shaped
// for direct RESP encoding by the caller.

func (s *Stream) infoStream() []any {
	first := ""
	last := ""
	if len(s.entries) > 0 {
		first = s.entries[0].ID.String()
		last = s.entries[len(s.entries)-1].ID.String()
	}
	out := []any{
		"length", int64(len(s.entries)),
		"radix-tree-keys", int64(0), // we don't expose internal indexes
		"radix-tree-nodes", int64(0),
		"last-generated-id", s.lastID.String(),
		"first-entry", first,
		"last-entry", last,
		"groups", int64(len(s.groups)),
	}
	return out
}

func (s *Stream) infoGroups() []any {
	out := []any{}
	names := make([]string, 0, len(s.groups))
	for n := range s.groups {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		g := s.groups[n]
		out = append(out, []any{
			"name", g.Name,
			"consumers", int64(len(g.Consumers)),
			"pending", int64(len(g.Pending)),
			"last-delivered-id", g.LastID.String(),
			"entries-read", g.EntriesRead,
		})
	}
	return out
}

func (s *Stream) infoConsumers(group string) ([]any, error) {
	g, ok := s.groups[group]
	if !ok {
		return nil, errors.New("NOGROUP No such consumer group")
	}
	names := make([]string, 0, len(g.Consumers))
	for n := range g.Consumers {
		names = append(names, n)
	}
	sort.Strings(names)
	now := timeNow()
	out := []any{}
	for _, n := range names {
		c := g.Consumers[n]
		out = append(out, []any{
			"name", c.Name,
			"pending", int64(len(c.Pending)),
			"idle", now.Sub(c.SeenAt).Milliseconds(),
		})
	}
	return out, nil
}

// PendingSummary / PendingDetail mirror the two XPENDING reply shapes.
type PendingSummary struct {
	Count     int64
	MinID     string
	MaxID     string
	Consumers [][2]string
}

type PendingDetail struct {
	ID            string
	Consumer      string
	IdleMs        int64
	DeliveryCount int64
}

// store-level wrappers ─────────────────────────────────────────────────

// XGroupCreate creates a consumer group on key. mkstream creates an
// empty stream when missing — matching XGROUP CREATE ... MKSTREAM.
func (s *Store) XGroupCreate(key, group, idStr string, mkstream bool) error {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil {
		return err
	}
	if !ok {
		if !mkstream {
			return errors.New("ERR The XGROUP subcommand requires the key to exist. Note that for CREATE you may want to use the MKSTREAM option to create an empty stream automatically.")
		}
		e, err = s.getOrCreate(sh, key, TypeStream)
		if err != nil {
			return err
		}
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.groupCreate(group, idStr, mkstream)
}

// XGroupDestroy removes a group; returns whether anything was removed.
func (s *Store) XGroupDestroy(key, group string) (bool, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return false, err
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.groupDestroy(group), nil
}

// XGroupSetID resets a group's last-delivered-id.
func (s *Store) XGroupSetID(key, group, idStr string) error {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("ERR no such key")
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.groupSetID(group, idStr)
}

// XGroupCreateConsumer ensures a consumer exists; returns 1 if created.
func (s *Store) XGroupCreateConsumer(key, group, consumer string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errors.New("ERR no such key")
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	created, err := e.Stream.groupCreateConsumer(group, consumer)
	if err != nil {
		return 0, err
	}
	if created {
		return 1, nil
	}
	return 0, nil
}

// XGroupDelConsumer deletes a consumer; returns the number of pending
// entries it owned (mirrors Redis).
func (s *Store) XGroupDelConsumer(key, group, consumer string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return 0, err
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.groupDelConsumer(group, consumer)
}

// XReadGroup pulls new entries (ids[i] == ">") or replays the consumer's
// PEL (any other id). Returns one entry list per stream key.
func (s *Store) XReadGroup(group, consumer string, keys, ids []string, count int, noack bool) (map[string][]StreamEntry, error) {
	if len(keys) != len(ids) {
		return nil, errors.New("ERR Unbalanced XREADGROUP keys and IDs")
	}
	out := map[string][]StreamEntry{}
	involved := s.shardsFor(keys)
	unlock := s.lockShardsR(involved)
	defer unlock()
	for i, k := range keys {
		sh := s.shardForKey(k)
		e, ok, err := sh.get(k, TypeStream)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		e.Stream.mu.Lock()
		isNew := ids[i] == ">"
		var after StreamID
		if !isNew {
			after, err = ParseStreamID(ids[i], false)
			if err != nil {
				e.Stream.mu.Unlock()
				return nil, err
			}
		}
		entries, err := e.Stream.readGroup(group, consumer, after, count, noack, !isNew)
		e.Stream.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 || (!isNew) {
			out[k] = entries
		}
	}
	return out, nil
}

// XAck removes ids from the group's PEL. Returns count actually acked.
func (s *Store) XAck(key, group string, ids []string) (int, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return 0, err
	}
	parsed := make([]StreamID, 0, len(ids))
	for _, id := range ids {
		p, err := ParseStreamID(id, false)
		if err != nil {
			return 0, err
		}
		parsed = append(parsed, p)
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.ack(group, parsed)
}

// XPending returns either the summary form (no filters) or the long
// form (with start/end/count and optional consumer).
func (s *Store) XPending(key, group string, summary bool, start, end string, count int, consumer string) (any, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return nil, err
	}
	startID, err := ParseStreamID(start, false)
	if err != nil {
		startID = StreamID{0, 0}
	}
	endID, err := ParseStreamID(end, true)
	if err != nil {
		endID = StreamID{^uint64(0), ^uint64(0)}
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.pending(group, summary, startID, endID, count, consumer)
}

// XClaim re-assigns one or more pending entries to a new consumer.
func (s *Store) XClaim(key, group, consumer string, minIdleMs int64, ids []string, opts XClaimOpts) ([]StreamEntry, []string, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return nil, nil, err
	}
	parsed := make([]StreamID, 0, len(ids))
	for _, id := range ids {
		p, err := ParseStreamID(id, false)
		if err != nil {
			return nil, nil, err
		}
		parsed = append(parsed, p)
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.claim(group, consumer, minIdleMs, parsed, opts.JustIDs, opts.IdleMs, opts.Time, opts.Retry)
}

// XAutoClaim sweeps a group's PEL for stale entries (idle >= minIdleMs)
// starting at startID, reassigning up to count of them to consumer.
// Returns (entries, justIDs, nextCursor, deletedIDs).
func (s *Store) XAutoClaim(key, group, consumer string, minIdleMs int64, startStr string, count int, justIDs bool) ([]StreamEntry, []string, string, []string, error) {
	sh := s.shardForKey(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return nil, nil, "0-0", nil, err
	}
	startID, err := ParseStreamID(startStr, false)
	if err != nil {
		startID = StreamID{0, 0}
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.autoclaim(group, consumer, minIdleMs, startID, count, justIDs)
}

// XInfoStream / XInfoGroups / XInfoConsumers expose the metadata
// XINFO returns. Each renders to a Redis-style flat key/value list.

func (s *Store) XInfoStream(key string) ([]any, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return nil, err
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.infoStream(), nil
}

func (s *Store) XInfoGroups(key string) ([]any, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return nil, err
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.infoGroups(), nil
}

func (s *Store) XInfoConsumers(key, group string) ([]any, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeStream)
	if err != nil || !ok {
		return nil, err
	}
	e.Stream.mu.Lock()
	defer e.Stream.mu.Unlock()
	return e.Stream.infoConsumers(group)
}

// XClaimOpts captures the optional flags on XCLAIM (IDLE, TIME, RETRYCOUNT,
// FORCE, JUSTID). FORCE is implied by callers that pre-validate IDs.
type XClaimOpts struct {
	IdleMs  int64
	Time    int64
	Retry   int64
	Force   bool
	JustIDs bool
}

// timeNow is a seam for tests; production reads the wall clock.
var timeNow = func() time.Time { return time.Now() }

// time1ms turns ms count into a time.Duration. Tiny helper kept local
// so the call sites read cleanly.
func time1ms(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

// formatPending is exposed for tests / debugging — turns a PendingDetail
// list into a stable string representation.
func formatPending(rows []PendingDetail) string {
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = fmt.Sprintf("%s/%s/%d/%d", r.ID, r.Consumer, r.IdleMs, r.DeliveryCount)
	}
	return strings.Join(parts, ";")
}

var _ = formatPending
