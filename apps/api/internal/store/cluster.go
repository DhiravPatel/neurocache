package store

import "time"

// SlotHasher is the function type the engine plugs in so the store can
// answer CLUSTER COUNTKEYSINSLOT / GETKEYSINSLOT without importing the
// cluster package (which would create a cycle).
type SlotHasher func(key string) int

// KeysInSlot returns up to count live keys hashing to slot. count<=0
// returns every match.
func (s *Store) KeysInSlot(slot, count int, hasher SlotHasher) []string {
	if hasher == nil {
		return nil
	}
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, 16)
	for k, e := range s.data {
		if e.expired(now) {
			continue
		}
		if hasher(k) != slot {
			continue
		}
		out = append(out, k)
		if count > 0 && len(out) >= count {
			break
		}
	}
	return out
}

// CountKeysInSlot is the cheap variant — no allocations.
func (s *Store) CountKeysInSlot(slot int, hasher SlotHasher) int {
	if hasher == nil {
		return 0
	}
	now := time.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for k, e := range s.data {
		if e.expired(now) {
			continue
		}
		if hasher(k) == slot {
			n++
		}
	}
	return n
}
