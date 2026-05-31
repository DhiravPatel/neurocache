package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// LicenseTracker is the source-license tracker that nobody ships.
// PROV records lineage; this maps a source ref to its license and
// flags incompatibilities for a declared use (commercial, research,
// internal-only, redistribution).
//
// The compatibility matrix is a small table the operator configures;
// canonical defaults cover the common cases (MIT/Apache OK for
// commercial; GPL viral; CC-BY-NC blocks commercial; CC-BY-SA
// requires same-license redistribution).
//
// Commands:
//
//   LICENSE.TAG source-ref LICENSE "MIT" [URL u] [AUTHOR a]
//   LICENSE.UNTAG source-ref
//   LICENSE.GET source-ref
//   LICENSE.MATRIX license use → compatible (0/1) + note
//   LICENSE.CHECK use SOURCES s1,s2,...
//        → blocked, incompatible_sources, reasons
//   LICENSE.COMPAT_SET license use compatible|incompatible "note"
//   LICENSE.LIST
//   LICENSE.STATS
type LicenseTracker struct {
	mu       sync.RWMutex
	tags     map[string]*licenseTag
	compat   map[string]licenseCompat // key = license|use

	totalTags   atomic.Int64
	totalChecks atomic.Int64
	totalBlocks atomic.Int64
}

type licenseTag struct {
	Source  string
	License string
	URL     string
	Author  string
}

type licenseCompat struct {
	Compatible bool
	Note       string
}

// NewLicenseTracker returns a registry pre-seeded with a small default
// compatibility matrix.
func NewLicenseTracker() *LicenseTracker {
	l := &LicenseTracker{
		tags:   map[string]*licenseTag{},
		compat: map[string]licenseCompat{},
	}
	// Seed common defaults
	l.seed("MIT", "commercial", true, "permissive")
	l.seed("MIT", "redistribution", true, "permissive with attribution")
	l.seed("Apache-2.0", "commercial", true, "permissive with patent grant")
	l.seed("BSD-3-Clause", "commercial", true, "permissive")
	l.seed("GPL-3.0", "commercial", false, "viral copyleft — derivative must be GPL")
	l.seed("AGPL-3.0", "commercial", false, "AGPL extends GPL viral to network use")
	l.seed("CC-BY-4.0", "commercial", true, "ok with attribution")
	l.seed("CC-BY-NC-4.0", "commercial", false, "non-commercial only")
	l.seed("CC-BY-SA-4.0", "redistribution", false, "share-alike — derivative must be CC-BY-SA")
	l.seed("proprietary", "commercial", false, "no commercial use without license")
	return l
}

func (l *LicenseTracker) seed(license, use string, ok bool, note string) {
	l.compat[strings.ToLower(license)+"|"+strings.ToLower(use)] = licenseCompat{
		Compatible: ok, Note: note,
	}
}

// Tag attaches a license to a source ref.
func (l *LicenseTracker) Tag(source, license, url, author string) error {
	if source == "" {
		return errors.New("source_ref required")
	}
	if license == "" {
		return errors.New("license required")
	}
	l.totalTags.Add(1)
	l.mu.Lock()
	l.tags[source] = &licenseTag{
		Source: source, License: license, URL: url, Author: author,
	}
	l.mu.Unlock()
	return nil
}

// Untag drops a tag.
func (l *LicenseTracker) Untag(source string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.tags[source]; ok {
		delete(l.tags, source)
		return 1
	}
	return 0
}

// LicenseGetView is GET's return.
type LicenseGetView struct {
	Source  string `json:"source"`
	License string `json:"license"`
	URL     string `json:"url,omitempty"`
	Author  string `json:"author,omitempty"`
}

// Get returns a source's tag.
func (l *LicenseTracker) Get(source string) (LicenseGetView, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	t, ok := l.tags[source]
	if !ok {
		return LicenseGetView{}, false
	}
	return LicenseGetView{
		Source: t.Source, License: t.License, URL: t.URL, Author: t.Author,
	}, true
}

// CompatSet overrides the compatibility for one (license, use) pair.
func (l *LicenseTracker) CompatSet(license, use string, compatible bool, note string) error {
	if license == "" || use == "" {
		return errors.New("license and use required")
	}
	l.mu.Lock()
	l.compat[strings.ToLower(license)+"|"+strings.ToLower(use)] = licenseCompat{
		Compatible: compatible, Note: note,
	}
	l.mu.Unlock()
	return nil
}

// LicenseMatrixResult is MATRIX's return.
type LicenseMatrixResult struct {
	License    string `json:"license"`
	Use        string `json:"use"`
	Compatible bool   `json:"compatible"`
	Note       string `json:"note"`
	Known      bool   `json:"known"`
}

// Matrix looks up the (license, use) pair.
func (l *LicenseTracker) Matrix(license, use string) LicenseMatrixResult {
	l.mu.RLock()
	defer l.mu.RUnlock()
	c, ok := l.compat[strings.ToLower(license)+"|"+strings.ToLower(use)]
	r := LicenseMatrixResult{License: license, Use: use, Known: ok}
	if ok {
		r.Compatible = c.Compatible
		r.Note = c.Note
	} else {
		// Default deny for unknown pairs
		r.Compatible = false
		r.Note = "unknown license/use pair — default deny"
	}
	return r
}

// LicenseCheckResult is CHECK's return.
type LicenseCheckResult struct {
	Use                  string                `json:"use"`
	Blocked              bool                  `json:"blocked"`
	IncompatibleSources  []LicenseIncompatRow  `json:"incompatible_sources"`
}

// LicenseIncompatRow is one row of incompatible_sources.
type LicenseIncompatRow struct {
	Source  string `json:"source"`
	License string `json:"license"`
	Reason  string `json:"reason"`
}

// Check tests an answer's lineage (list of source refs) against a
// declared use. Sources without a tag are treated as unknown →
// default deny (callers should TAG every source for full coverage).
func (l *LicenseTracker) Check(use string, sources []string) LicenseCheckResult {
	l.totalChecks.Add(1)
	out := LicenseCheckResult{Use: use}
	for _, s := range sources {
		l.mu.RLock()
		t, ok := l.tags[s]
		l.mu.RUnlock()
		if !ok {
			out.IncompatibleSources = append(out.IncompatibleSources, LicenseIncompatRow{
				Source: s, License: "(unknown)", Reason: "no license tag — default deny",
			})
			continue
		}
		m := l.Matrix(t.License, use)
		if !m.Compatible {
			out.IncompatibleSources = append(out.IncompatibleSources, LicenseIncompatRow{
				Source: s, License: t.License, Reason: m.Note,
			})
		}
	}
	if len(out.IncompatibleSources) > 0 {
		out.Blocked = true
		l.totalBlocks.Add(1)
	}
	sort.Slice(out.IncompatibleSources, func(i, j int) bool {
		return out.IncompatibleSources[i].Source < out.IncompatibleSources[j].Source
	})
	return out
}

// List enumerates tags.
func (l *LicenseTracker) List() []LicenseGetView {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]LicenseGetView, 0, len(l.tags))
	for _, t := range l.tags {
		out = append(out, LicenseGetView{
			Source: t.Source, License: t.License, URL: t.URL, Author: t.Author,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// LicenseStats is the global snapshot.
type LicenseStats struct {
	Tags        int   `json:"tags"`
	MatrixSize  int   `json:"matrix_size"`
	TotalTags   int64 `json:"total_tags"`
	TotalChecks int64 `json:"total_checks"`
	TotalBlocks int64 `json:"total_blocks"`
}

func (l *LicenseTracker) Stats() LicenseStats {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return LicenseStats{
		Tags: len(l.tags), MatrixSize: len(l.compat),
		TotalTags: l.totalTags.Load(),
		TotalChecks: l.totalChecks.Load(),
		TotalBlocks: l.totalBlocks.Load(),
	}
}
