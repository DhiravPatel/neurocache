package store

import (
	"errors"
	"math"
	"sort"
)

// Geo is layered on top of sorted sets: a 52-bit interleaved geohash of
// the (lat, lon) pair becomes the score, the member name is the
// location's label. That mirrors Redis's own implementation and means
// we get ZRANGE-style ordering for free.

// Geographic bounds used by Redis's 52-bit interleaved encoding.
const (
	geoLatMin  = -85.05112878
	geoLatMax  = 85.05112878
	geoLonMin  = -180.0
	geoLonMax  = 180.0
	geoStepMax = 26 // bits per coordinate (26+26 = 52 total)
)

// GeoPoint is a lat/lon pair as returned by GEOPOS.
type GeoPoint struct {
	Lon float64
	Lat float64
}

// geoEncode interleaves the latitude and longitude bits into a 52-bit
// unsigned integer. Precision: ~0.6 meters.
func geoEncode(lat, lon float64) (uint64, error) {
	if lat < geoLatMin || lat > geoLatMax || lon < geoLonMin || lon > geoLonMax {
		return 0, errors.New("invalid longitude,latitude pair")
	}
	latNorm := (lat - geoLatMin) / (geoLatMax - geoLatMin)
	lonNorm := (lon - geoLonMin) / (geoLonMax - geoLonMin)
	latI := uint64(latNorm * (1 << geoStepMax))
	lonI := uint64(lonNorm * (1 << geoStepMax))
	var h uint64
	for i := 0; i < geoStepMax; i++ {
		h |= ((lonI >> i) & 1) << (2 * i)
		h |= ((latI >> i) & 1) << (2*i + 1)
	}
	return h, nil
}

// geoDecode reverses the interleaving. Produces the center of the cell.
func geoDecode(h uint64) (lat, lon float64) {
	var latI, lonI uint64
	for i := 0; i < geoStepMax; i++ {
		lonI |= ((h >> (2 * i)) & 1) << i
		latI |= ((h >> (2*i + 1)) & 1) << i
	}
	lat = geoLatMin + (float64(latI)+0.5)/(1<<geoStepMax)*(geoLatMax-geoLatMin)
	lon = geoLonMin + (float64(lonI)+0.5)/(1<<geoStepMax)*(geoLonMax-geoLonMin)
	return
}

// haversine returns the great-circle distance in meters between two
// points on a sphere of Earth's mean radius (6,372,797.560856 m).
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6372797.560856
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLon := (lon2 - lon1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * R * math.Asin(math.Sqrt(a))
}

// GeoAddEntry is an input tuple for GEOADD.
type GeoAddEntry struct {
	Lon, Lat float64
	Member   string
}

// GeoAdd writes members with their interleaved geohash as the ZSet
// score. Existing members are updated. Returns the count of *new*
// members added.
func (s *Store) GeoAdd(key string, entries ...GeoAddEntry) (int, error) {
	pairs := make([]ZPair, 0, len(entries))
	for _, e := range entries {
		h, err := geoEncode(e.Lat, e.Lon)
		if err != nil {
			return 0, err
		}
		pairs = append(pairs, ZPair{Score: float64(h), Member: e.Member})
	}
	return s.ZAdd(key, pairs...)
}

// GeoPos returns [lon, lat] per requested member; missing members are
// returned as nil by the caller — this method returns (nil, false) for
// them so the dispatch layer can emit nil arrays properly.
func (s *Store) GeoPos(key string, members ...string) ([]*GeoPoint, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil {
		return nil, err
	}
	out := make([]*GeoPoint, len(members))
	if !ok {
		return out, nil
	}
	for i, m := range members {
		sc, had := e.ZSet.Score(m)
		if !had {
			continue
		}
		lat, lon := geoDecode(uint64(sc))
		out[i] = &GeoPoint{Lon: lon, Lat: lat}
	}
	return out, nil
}

// GeoDist returns the distance between two members in the specified
// unit ("m" meters, "km", "mi", "ft"). Returns -1 when either is missing.
func (s *Store) GeoDist(key, a, b, unit string) (float64, bool, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return 0, false, err
	}
	sa, haveA := e.ZSet.Score(a)
	sb, haveB := e.ZSet.Score(b)
	if !haveA || !haveB {
		return 0, false, nil
	}
	latA, lonA := geoDecode(uint64(sa))
	latB, lonB := geoDecode(uint64(sb))
	d := haversine(latA, lonA, latB, lonB)
	return convertUnit(d, unit), true, nil
}

// GeoSearchResult carries a single GEOSEARCH hit.
type GeoSearchResult struct {
	Member   string
	Distance float64
	Lat, Lon float64
}

// GeoSearch returns members within the radius (center lat/lon, radius +
// unit). Simple O(n) scan; a production build with huge datasets would
// use geohash prefix buckets, but this keeps the algorithm transparent.
func (s *Store) GeoSearch(key string, centerLat, centerLon, radius float64, unit string, count int) ([]GeoSearchResult, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil || !ok {
		return []GeoSearchResult{}, err
	}
	// radius is in the chosen unit; normalize to meters for haversine.
	radMeters := unitToMeters(radius, unit)
	out := []GeoSearchResult{}
	for _, m := range e.ZSet.members() {
		sc, _ := e.ZSet.Score(m)
		lat, lon := geoDecode(uint64(sc))
		d := haversine(centerLat, centerLon, lat, lon)
		if d <= radMeters {
			out = append(out, GeoSearchResult{
				Member:   m,
				Distance: convertUnit(d, unit),
				Lat:      lat,
				Lon:      lon,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	if count > 0 && len(out) > count {
		out = out[:count]
	}
	return out, nil
}

// GeoSearchStore copies the result of a GeoSearch into a destination
// zset. The destination's scores are either the source members'
// original geohashes (when storeDist=false — Redis default) or the
// haversine distances in the requested unit (when storeDist=true,
// matching Redis's STOREDIST flag). Returns the resulting cardinality.
//
// Atomicity: the search and the write happen under separate
// critical sections — concurrent GEOADDs on src after the snapshot
// won't be visible in dst. This matches Redis's behaviour, which
// reads src into a temporary buffer before writing dst.
func (s *Store) GeoSearchStore(dest, src string, centerLat, centerLon, radius float64, unit string, count int, storeDist bool) (int, error) {
	hits, err := s.GeoSearch(src, centerLat, centerLon, radius, unit, count)
	if err != nil {
		return 0, err
	}
	merged := map[string]float64{}
	if storeDist {
		for _, h := range hits {
			merged[h.Member] = h.Distance
		}
	} else {
		// preserve original geohash scores for ZRANGE-style ordering
		shS := s.shardForKey(src)
		shS.mu.RLock()
		se, ok, _ := shS.get(src, TypeZSet)
		if ok {
			for _, h := range hits {
				if sc, had := se.ZSet.Score(h.Member); had {
					merged[h.Member] = sc
				}
			}
		}
		shS.mu.RUnlock()
	}
	return s.replaceZSet(dest, merged)
}

// GeoHash returns the standard 11-char base32 geohash for each member.
// Missing members come back as empty strings.
func (s *Store) GeoHash(key string, members ...string) ([]string, error) {
	sh := s.shardForKey(key)
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	e, ok, err := sh.get(key, TypeZSet)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(members))
	if !ok {
		return out, nil
	}
	for i, m := range members {
		sc, had := e.ZSet.Score(m)
		if !had {
			continue
		}
		lat, lon := geoDecode(uint64(sc))
		out[i] = base32GeoHash(lat, lon, 11)
	}
	return out, nil
}

// ─── helpers ────────────────────────────────────────────────────────────

func unitToMeters(v float64, unit string) float64 {
	switch unit {
	case "km":
		return v * 1000
	case "mi":
		return v * 1609.344
	case "ft":
		return v * 0.3048
	default:
		return v
	}
}

func convertUnit(meters float64, unit string) float64 {
	switch unit {
	case "km":
		return meters / 1000
	case "mi":
		return meters / 1609.344
	case "ft":
		return meters / 0.3048
	default:
		return meters
	}
}

// base32GeoHash computes the traditional alphanumeric geohash.
func base32GeoHash(lat, lon float64, precision int) string {
	const base32 = "0123456789bcdefghjkmnpqrstuvwxyz"
	latLo, latHi := -90.0, 90.0
	lonLo, lonHi := -180.0, 180.0
	even := true
	var bit, ch uint8
	var out []byte
	for len(out) < precision {
		if even {
			mid := (lonLo + lonHi) / 2
			if lon >= mid {
				ch |= 1 << (4 - bit)
				lonLo = mid
			} else {
				lonHi = mid
			}
		} else {
			mid := (latLo + latHi) / 2
			if lat >= mid {
				ch |= 1 << (4 - bit)
				latLo = mid
			} else {
				latHi = mid
			}
		}
		even = !even
		if bit < 4 {
			bit++
		} else {
			out = append(out, base32[ch])
			bit, ch = 0, 0
		}
	}
	return string(out)
}
