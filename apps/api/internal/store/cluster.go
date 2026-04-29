package store

import "time"

// SlotHasher is the function type the engine plugs in so the store can
// answer CLUSTER COUNTKEYSINSLOT / GETKEYSINSLOT without importing the
// cluster package (which would create a cycle).
type SlotHasher func(key string) int

// KeysInSlot returns up to count live keys hashing to slot. count<=0
// returns every match. Walks all shards under read locks; cluster slot
// queries are a low-frequency observability path (called by SETSLOT
// during re-shards), not on the hot path.
func (s *Store) KeysInSlot(slot, count int, hasher SlotHasher) []string {
	if hasher == nil {
		return nil
	}
	now := time.Now()
	out := make([]string, 0, 16)
	for _, sh := range s.shards {
		sh.mu.RLock()
		for k, e := range sh.data {
			if e.expired(now) {
				continue
			}
			if hasher(k) != slot {
				continue
			}
			out = append(out, k)
			if count > 0 && len(out) >= count {
				sh.mu.RUnlock()
				return out
			}
		}
		sh.mu.RUnlock()
	}
	return out
}

// CountKeysInSlot is the cheap variant — no allocations.
func (s *Store) CountKeysInSlot(slot int, hasher SlotHasher) int {
	if hasher == nil {
		return 0
	}
	now := time.Now()
	n := 0
	for _, sh := range s.shards {
		sh.mu.RLock()
		for k, e := range sh.data {
			if e.expired(now) {
				continue
			}
			if hasher(k) == slot {
				n++
			}
		}
		sh.mu.RUnlock()
	}
	return n
}
