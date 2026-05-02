package store

import "strconv"

// Scan walks the keyspace with a cursor. The cursor is a flat offset — not
// the bit-reversed Redis cursor — but it satisfies the contract that
// callers iterate until cursor == "0". count is a hint; match filters by
// glob. A missing typeFilter ("") returns everything.
func (s *Store) Scan(cursor string, match, typeFilter string, count int) (string, []string) {
	unlock := s.lockAllR()
	defer unlock()
	total := 0
	for _, sh := range s.shards {
		total += len(sh.data)
	}
	keys := make([]string, 0, total)
	for _, sh := range s.shards {
		for k := range sh.data {
			keys = append(keys, k)
		}
	}
	sortKeys(keys)

	off, _ := strconv.Atoi(cursor)
	if off < 0 {
		off = 0
	}
	if count <= 0 {
		count = 10
	}
	out := []string{}
	end := off + count
	if end > len(keys) {
		end = len(keys)
	}
	for i := off; i < end; i++ {
		k := keys[i]
		sh := s.shardForKey(k)
		e := sh.data[k]
		if e == nil {
			continue
		}
		if typeFilter != "" && e.Type.String() != typeFilter {
			continue
		}
		if match != "" && match != "*" && !globMatch(match, k) {
			continue
		}
		out = append(out, k)
	}
	next := "0"
	if end < len(keys) {
		next = strconv.Itoa(end)
	}
	return next, out
}

// HScan iterates a hash. Returns field+value pairs interleaved.
func (s *Store) HScan(key, cursor, match string, count int) (string, []string, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeHash)
	if err != nil || !ok {
		return "0", []string{}, err
	}
	fields := make([]string, 0, len(e.Hash))
	for f := range e.Hash {
		fields = append(fields, f)
	}
	sortKeys(fields)
	off, _ := strconv.Atoi(cursor)
	if off < 0 {
		off = 0
	}
	if count <= 0 {
		count = 10
	}
	out := []string{}
	end := off + count
	if end > len(fields) {
		end = len(fields)
	}
	for i := off; i < end; i++ {
		f := fields[i]
		if match != "" && match != "*" && !globMatch(match, f) {
			continue
		}
		out = append(out, f, e.Hash[f])
	}
	next := "0"
	if end < len(fields) {
		next = strconv.Itoa(end)
	}
	return next, out, nil
}

// SScan iterates a set.
func (s *Store) SScan(key, cursor, match string, count int) (string, []string, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeSet)
	if err != nil || !ok {
		return "0", []string{}, err
	}
	members := make([]string, 0, len(e.Set))
	for m := range e.Set {
		members = append(members, m)
	}
	sortKeys(members)
	off, _ := strconv.Atoi(cursor)
	if off < 0 {
		off = 0
	}
	if count <= 0 {
		count = 10
	}
	out := []string{}
	end := off + count
	if end > len(members) {
		end = len(members)
	}
	for i := off; i < end; i++ {
		m := members[i]
		if match != "" && match != "*" && !globMatch(match, m) {
			continue
		}
		out = append(out, m)
	}
	next := "0"
	if end < len(members) {
		next = strconv.Itoa(end)
	}
	return next, out, nil
}

// ZScan iterates a sorted set returning member+score pairs interleaved.
func (s *Store) ZScan(key, cursor, match string, count int) (string, []string, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return "0", []string{}, err
	}
	members := e.ZSet.members()
	off, _ := strconv.Atoi(cursor)
	if off < 0 {
		off = 0
	}
	if count <= 0 {
		count = 10
	}
	out := []string{}
	end := off + count
	if end > len(members) {
		end = len(members)
	}
	for i := off; i < end; i++ {
		m := members[i]
		if match != "" && match != "*" && !globMatch(match, m) {
			continue
		}
		sc, _ := e.ZSet.Score(m)
		out = append(out, m, strconv.FormatFloat(sc, 'f', -1, 64))
	}
	next := "0"
	if end < len(members) {
		next = strconv.Itoa(end)
	}
	return next, out, nil
}

// sortKeys is a tiny in-place sort so scan ordering stays stable between
// calls (makes cursor-based pagination deterministic).
func sortKeys(a []string) {
	// insertion sort: fine for the page-size slices this is called on
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
