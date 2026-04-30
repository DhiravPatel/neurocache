package store

import (
	"errors"
	"math/rand"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/vectorindex"
)

// TypeVector is the first-class vector-set type backing the V*
// command surface (VADD / VSIM / VEMB / …).
//
// Like TypeModule, this constant lives outside the iota block in
// store.go so adding it doesn't churn every existing type switch
// (none of those switches need to know about vector sets — the V*
// commands call dedicated Store helpers).
const TypeVector ValueType = 101

// VectorSet is the per-key payload. Wraps a vectorindex.Index so the
// store layer can stay storage-agnostic (FLAT vs HNSW is decided at
// VADD time and persisted in the index options).
type VectorSet struct {
	Index *vectorindex.Index
}

// VAdd inserts (id, vec) into the named vector set. If the set
// doesn't exist yet, it's created with the supplied options. If the
// set already exists, opts is consulted only for dimension validation
// — every other field is ignored (you can't change M / EFC / metric
// after creation without VREM-then-recreate).
//
// Returns:
//
//	1 — id was new
//	0 — id already existed (vector was replaced)
func (s *Store) VAdd(key, id string, vec []float32, opts vectorindex.Options) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if ok && !e.expired(time.Now()) {
		if e.Type != TypeVector || e.Vector == nil {
			return 0, ErrWrongType
		}
		if e.Vector.Index.Dim() != opts.Dim && opts.Dim > 0 {
			return 0, errors.New("ERR vector dim does not match the existing set")
		}
	} else {
		idx, err := vectorindex.New(opts)
		if err != nil {
			return 0, err
		}
		e = &Entry{
			Key: key, Type: TypeVector,
			CreatedAt: time.Now(), LastRead: time.Now(),
			Vector: &VectorSet{Index: idx},
		}
		s.data[key] = e
	}
	_, hadBefore := e.Vector.Index.Get(id)
	if err := e.Vector.Index.Set(id, vec); err != nil {
		return 0, err
	}
	s.recomputeBytes(e)
	s.fire("vadd", key)
	if hadBefore {
		return 0, nil
	}
	return 1, nil
}

// VRem deletes one or more ids from the set. Returns the count of
// ids that were actually present and removed. The set itself stays
// alive even when emptied — clients tear it down via DEL.
func (s *Store) VRem(key string, ids ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return 0, err
	}
	removed := 0
	for _, id := range ids {
		if e.Vector.Index.Del(id) {
			removed++
		}
	}
	s.recomputeBytes(e)
	s.fire("vrem", key)
	return removed, nil
}

// VSim runs KNN. Returns up to count nearest neighbours sorted
// ascending by distance (i.e., descending by similarity). count <= 0
// returns the entire set.
func (s *Store) VSim(key string, query []float32, count int) ([]vectorindex.Result, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return nil, err
	}
	return e.Vector.Index.KNN(query, count), nil
}

// VEmb returns the stored vector for id (snapshot copy — safe to
// retain). ok=false when the id is not present.
func (s *Store) VEmb(key, id string) ([]float32, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return nil, false, err
	}
	v, present := e.Vector.Index.Get(id)
	if !present {
		return nil, false, nil
	}
	cp := make([]float32, len(v))
	copy(cp, v)
	return cp, true, nil
}

// VSetAttr / VGetAttr / VDelAttr manage the optional JSON attribute
// blob attached to each id. The store treats the value as opaque —
// callers serialize whatever JSON they like.
func (s *Store) VSetAttr(key, id, json string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return false, err
	}
	ok = e.Vector.Index.SetAttr(id, json)
	s.recomputeBytes(e)
	return ok, nil
}

func (s *Store) VGetAttr(key, id string) (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return "", false, err
	}
	v, present := e.Vector.Index.GetAttr(id)
	return v, present, nil
}

func (s *Store) VDelAttr(key, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return false, err
	}
	ok = e.Vector.Index.DelAttr(id)
	s.recomputeBytes(e)
	return ok, nil
}

// VLinks returns the HNSW neighbour lists per layer for id. Empty
// slice on FLAT indexes or when id is missing.
func (s *Store) VLinks(key, id string) ([][]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return nil, err
	}
	return e.Vector.Index.Links(id), nil
}

// VInfo returns a snapshot of the index configuration + size.
type VectorInfo struct {
	Algo        string
	Dim         int
	Metric      string
	M           int
	EFC         int
	EFR         int
	Card        int
	BytesApprox int64
}

func (s *Store) VInfo(key string) (VectorInfo, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return VectorInfo{}, false, err
	}
	idx := e.Vector.Index
	return VectorInfo{
		Algo:        string(idx.Algo()),
		Dim:         idx.Dim(),
		Metric:      string(idx.Metric()),
		M:           idx.M(),
		EFC:         idx.EFC(),
		EFR:         idx.EFR(),
		Card:        idx.Card(),
		BytesApprox: idx.MemUsage(),
	}, true, nil
}

// VCard returns the member count.
func (s *Store) VCard(key string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return 0, err
	}
	return e.Vector.Index.Card(), nil
}

// VDim returns the configured vector dimension.
func (s *Store) VDim(key string) (int, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return 0, false, err
	}
	return e.Vector.Index.Dim(), true, nil
}

// VRandMember returns up to count random ids. Behaviour matches
// SRANDMEMBER:
//
//	count == 0 → one random id (caller-side wrapper picks)
//	count > 0  → unique ids, capped at the set size
//	count < 0  → may repeat; |count| samples drawn with replacement
func (s *Store) VRandMember(key string, count int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		return nil, err
	}
	all := e.Vector.Index.IDs()
	if len(all) == 0 {
		return nil, nil
	}
	if count == 0 {
		return []string{all[rand.Intn(len(all))]}, nil
	}
	if count > 0 {
		n := count
		if n > len(all) {
			n = len(all)
		}
		shuffled := append([]string(nil), all...)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		return shuffled[:n], nil
	}
	n := -count
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = all[rand.Intn(len(all))]
	}
	return out, nil
}

// VScan is a cursor-based iteration over member ids — same shape as
// SCAN / HSCAN. Pass cursor=0 to start. Pattern is glob-style; "" or
// "*" matches everything. Returns (next-cursor, page).
//
// The cursor is a slice offset into a sort-stabilised id list. Sorting
// is the easiest way to give SCAN's "see every key at least once"
// guarantee without holding state across calls — without it, map
// iteration randomness would shift items in and out of each cursor
// window. Mutations between calls may still surface or hide ids
// (added ids might re-shuffle the sort position) — same caveat as
// SCAN, where consistency is best-effort.
func (s *Store) VScan(key string, cursor int, pattern string, count int) (int, []string, error) {
	if count <= 0 {
		count = 10
	}
	s.mu.RLock()
	e, ok, err := s.get(key, TypeVector)
	if err != nil || !ok {
		s.mu.RUnlock()
		return 0, nil, err
	}
	all := e.Vector.Index.IDs()
	s.mu.RUnlock()
	sortStrings(all)
	if cursor < 0 || cursor >= len(all) {
		return 0, nil, nil
	}
	end := cursor + count
	if end > len(all) {
		end = len(all)
	}
	page := all[cursor:end]
	out := make([]string, 0, len(page))
	for _, id := range page {
		if pattern == "" || pattern == "*" || globMatch(pattern, id) {
			out = append(out, id)
		}
	}
	if end >= len(all) {
		return 0, out, nil
	}
	return end, out, nil
}

// sortStrings is a tiny helper to keep the vector.go file self-
// contained without dragging in `sort` (already imported elsewhere
// in the package, but a direct dependency here keeps the file's
// imports honest).
func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}

// newVectorIndexFromExport rebuilds a vectorindex.Index from snapshot
// options. Used by snapshot.Restore and the Copy / DUMP / RESTORE
// paths so the same construction logic isn't duplicated three times.
func newVectorIndexFromExport(o ExportVectorOpts) (*vectorindex.Index, error) {
	algo := vectorindex.AlgoHNSW
	if o.Algo != "" {
		algo = vectorindex.Algo(o.Algo)
	}
	metric := vectorindex.MetricCosine
	if o.Metric != "" {
		metric = vectorindex.Metric(o.Metric)
	}
	return vectorindex.New(vectorindex.Options{
		Algo: algo, Dim: o.Dim, Metric: metric,
		M: o.M, EFC: o.EFC, EFR: o.EFR,
	})
}

// encodeVectorString / decodeVectorString are thin shims around the
// vectorindex codec, kept here so snapshot.go doesn't need to import
// vectorindex directly (preserves the existing layering rule that
// snapshot is type-agnostic).
func encodeVectorString(vec []float32) string { return vectorindex.EncodeVector(vec) }
func decodeVectorString(s string, dim int) ([]float32, error) {
	return vectorindex.ParseVector(s, dim)
}

