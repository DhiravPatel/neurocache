package searchmod

import (
	"math"
	"strconv"
	"strings"
	"sync"
)

// geoIndex is a tiny lat/lon store keyed by docID. We use a flat slice
// — a real geohash B-tree would beat it on huge corpora, but for the
// document counts a search index typically holds (≤ a few million
// rows) brute-force radius is dominated by haversine math anyway.
type geoIndex struct {
	mu      sync.RWMutex
	entries []geoEntry
	docs    map[string]int // docID -> entries idx
}

type geoEntry struct {
	docID string
	lon   float64
	lat   float64
}

func newGeoIndex() *geoIndex { return &geoIndex{docs: map[string]int{}} }

// Set records (or replaces) the doc's geo coords. raw is the field
// value as stored on the document — we accept both "lon,lat" (Redis
// JSON v2 form) and "lat,lon" interchangeably by checking ranges.
func (g *geoIndex) Set(docID, raw string) bool {
	parts := strings.Split(raw, ",")
	if len(parts) != 2 {
		return false
	}
	a, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	b, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil {
		return false
	}
	// Heuristic: latitude is bounded to [-90, 90]; longitude can hit
	// [-180, 180]. If the first value is outside the lat range, we
	// assume the convention is "lon,lat".
	lat, lon := a, b
	if math.Abs(a) > 90 {
		lon, lat = a, b
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if i, ok := g.docs[docID]; ok {
		g.entries[i].lon = lon
		g.entries[i].lat = lat
		return true
	}
	g.docs[docID] = len(g.entries)
	g.entries = append(g.entries, geoEntry{docID: docID, lon: lon, lat: lat})
	return true
}

// Del removes a doc's entry.
func (g *geoIndex) Del(docID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	i, ok := g.docs[docID]
	if !ok {
		return
	}
	last := len(g.entries) - 1
	if i != last {
		g.entries[i] = g.entries[last]
		g.docs[g.entries[i].docID] = i
	}
	g.entries = g.entries[:last]
	delete(g.docs, docID)
}

// Within returns docIDs inside the radius (meters) of (lat, lon).
// Uses the haversine formula — accurate to a few centimetres at the
// scales geo searches care about.
func (g *geoIndex) Within(lat, lon, radiusM float64) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := []string{}
	for _, e := range g.entries {
		if haversine(lat, lon, e.lat, e.lon) <= radiusM {
			out = append(out, e.docID)
		}
	}
	return out
}

const earthRadiusM = 6_371_000.0

func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(a))
}
