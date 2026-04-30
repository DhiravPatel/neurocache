package aiops

import (
	"sync"
)

// ChurnTags solves "cache invalidation" — by tagging keys at write time
// and invalidating by tag at read time. Standard Redis forces you to
// either: (a) keep a side-channel set of "keys to drop when X changes"
// in app code, or (b) re-architect around a TTL. Neither scales.
//
// CHURN gives you O(1) tagging and O(tagged-keys) invalidation directly
// at the cache layer. Tags don't have to be pre-declared; they're just
// strings. Invalidation returns the keys that were actually expired so
// the caller can decide what to refetch.
//
// Concurrency: a single sync.RWMutex covers the whole tag index. We
// don't shard because tag operations are typically low-frequency
// compared to GET/SET — they happen at write time and at invalidation
// events, not on the hot read path.
type ChurnTags struct {
	mu       sync.RWMutex
	keyToTag map[string]map[string]bool // key → set of tags
	tagToKey map[string]map[string]bool // tag → set of keys

	// invalidator is plugged in by the engine — given a list of keys,
	// it deletes them from the keyspace. Defaults to nil (no-op);
	// CHURN.INVALIDATE then just returns the matched keys without
	// actually evicting them, useful for inspection / dry-runs.
	invalidator KeyInvalidator
}

// KeyInvalidator is the engine-supplied function that drops keys.
type KeyInvalidator func(keys []string) int

// NewChurnTags returns an empty tag index.
func NewChurnTags() *ChurnTags {
	return &ChurnTags{
		keyToTag: map[string]map[string]bool{},
		tagToKey: map[string]map[string]bool{},
	}
}

// SetInvalidator wires the engine's keyspace deleter. Until set,
// Invalidate runs in dry-run mode (returns matched keys but doesn't
// drop them).
func (c *ChurnTags) SetInvalidator(fn KeyInvalidator) {
	c.mu.Lock()
	c.invalidator = fn
	c.mu.Unlock()
}

// Tag attaches one or more tags to a key. Returns the number of
// new tag-key associations created (existing pairs don't count).
func (c *ChurnTags) Tag(key string, tags ...string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.keyToTag[key]; !ok {
		c.keyToTag[key] = map[string]bool{}
	}
	added := 0
	for _, t := range tags {
		if c.keyToTag[key][t] {
			continue
		}
		c.keyToTag[key][t] = true
		if _, ok := c.tagToKey[t]; !ok {
			c.tagToKey[t] = map[string]bool{}
		}
		c.tagToKey[t][key] = true
		added++
	}
	return added
}

// Untag removes the (key, tag) associations. With no tags, removes
// the key from every tag it carries.
func (c *ChurnTags) Untag(key string, tags ...string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(tags) == 0 {
		// Drop the key from every tag it was attached to.
		set, ok := c.keyToTag[key]
		if !ok {
			return 0
		}
		n := len(set)
		for t := range set {
			delete(c.tagToKey[t], key)
			if len(c.tagToKey[t]) == 0 {
				delete(c.tagToKey, t)
			}
		}
		delete(c.keyToTag, key)
		return n
	}
	removed := 0
	for _, t := range tags {
		if c.keyToTag[key] != nil && c.keyToTag[key][t] {
			delete(c.keyToTag[key], t)
			delete(c.tagToKey[t], key)
			removed++
		}
		if len(c.keyToTag[key]) == 0 {
			delete(c.keyToTag, key)
		}
		if len(c.tagToKey[t]) == 0 {
			delete(c.tagToKey, t)
		}
	}
	return removed
}

// KeysFor returns every key carrying the given tag (sorted is the
// caller's job — we iterate in map-iteration order).
func (c *ChurnTags) KeysFor(tag string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	set, ok := c.tagToKey[tag]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// TagsOf returns every tag attached to a key.
func (c *ChurnTags) TagsOf(key string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	set, ok := c.keyToTag[key]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	return out
}

// Invalidate drops every key carrying any of the given tags. Returns
// the keys that were dropped (so the caller can re-prime them) and
// cleans up the tag→key index. With invalidator unset, returns the
// matched keys without deleting (dry-run mode useful for inspection).
func (c *ChurnTags) Invalidate(tags ...string) []string {
	c.mu.Lock()
	keys := map[string]bool{}
	for _, t := range tags {
		for k := range c.tagToKey[t] {
			keys[k] = true
		}
	}
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
		// Clean every tag→key edge for this key
		for t := range c.keyToTag[k] {
			delete(c.tagToKey[t], k)
			if len(c.tagToKey[t]) == 0 {
				delete(c.tagToKey, t)
			}
		}
		delete(c.keyToTag, k)
	}
	inv := c.invalidator
	c.mu.Unlock()
	if inv != nil && len(out) > 0 {
		inv(out)
	}
	return out
}

// ChurnStats snapshots the tag index.
type ChurnStats struct {
	Keys int `json:"tagged_keys"`
	Tags int `json:"unique_tags"`
}

// Stats returns a snapshot.
func (c *ChurnTags) Stats() ChurnStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return ChurnStats{Keys: len(c.keyToTag), Tags: len(c.tagToKey)}
}

// Tags returns every known tag (for CHURN.TAGS introspection).
func (c *ChurnTags) Tags() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.tagToKey))
	for t := range c.tagToKey {
		out = append(out, t)
	}
	return out
}
