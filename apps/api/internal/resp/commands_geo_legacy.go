package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/store"
)

// georadiusCmd implements the deprecated GEORADIUS / GEORADIUS_RO surface:
//
//   GEORADIUS key lon lat radius m|km|mi|ft
//     [WITHCOORD] [WITHDIST] [WITHHASH] [COUNT n [ANY]]
//     [ASC|DESC] [STORE dest] [STOREDIST dest]
//
// Redis deprecated this in favour of GEOSEARCH; we keep the surface
// alive because legacy drivers (especially older Java + Python ones)
// still hard-code it. STORE / STOREDIST route through the same helper
// the new GEOSEARCHSTORE uses, so the storage path is shared.
//
// readOnly is set by GEORADIUS_RO — when true, STORE/STOREDIST options
// are rejected so the command stays pure-read for replicas.
func (c *conn) georadiusCmd(args []string, readOnly bool) {
	if !c.wantArgs("GEORADIUS", args, 5) {
		return
	}
	key := args[0]
	lon, errLon := strconv.ParseFloat(args[1], 64)
	lat, errLat := strconv.ParseFloat(args[2], 64)
	radius, errR := strconv.ParseFloat(args[3], 64)
	if errLon != nil || errLat != nil || errR != nil {
		writeError(c.bw, "value is not a valid float")
		return
	}
	unit := strings.ToLower(args[4])
	emitGeoRadius(c, key, lat, lon, radius, unit, args[5:], readOnly, "")
}

// georadiusByMemberCmd implements GEORADIUSBYMEMBER / _RO. Same shape
// but the centre is a named member's coordinates rather than an
// explicit lat/lon.
func (c *conn) georadiusByMemberCmd(args []string, readOnly bool) {
	if !c.wantArgs("GEORADIUSBYMEMBER", args, 4) {
		return
	}
	key, member := args[0], args[1]
	radius, errR := strconv.ParseFloat(args[2], 64)
	if errR != nil {
		writeError(c.bw, "value is not a valid float")
		return
	}
	unit := strings.ToLower(args[3])
	pts, err := c.eng.KV.GeoPos(key, member)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if len(pts) == 0 || pts[0] == nil {
		writeError(c.bw, "ERR could not decode requested zset member")
		return
	}
	emitGeoRadius(c, key, pts[0].Lat, pts[0].Lon, radius, unit, args[4:], readOnly, member)
}

// emitGeoRadius is the shared back-end. It scans, applies the option
// flags, and writes either a result array or — when STORE/STOREDIST is
// set — the destination cardinality. excludeMember (set only by the
// BYMEMBER form) drops the centre from the result so callers don't see
// themselves in their own neighbourhood.
func emitGeoRadius(c *conn, key string, lat, lon, radius float64, unit string, opts []string, readOnly bool, excludeMember string) {
	var (
		withCoord, withDist, withHash bool
		count                          = 0
		any_                           = false
		asc                            = true
		hasOrder                       = false
		storeKey                       string
		storeDist                      = false
	)
	for i := 0; i < len(opts); i++ {
		switch strings.ToUpper(opts[i]) {
		case "WITHCOORD":
			withCoord = true
		case "WITHDIST":
			withDist = true
		case "WITHHASH":
			withHash = true
		case "COUNT":
			if i+1 >= len(opts) {
				writeError(c.bw, "syntax error: COUNT requires a value")
				return
			}
			count, _ = strconv.Atoi(opts[i+1])
			i++
			if i+1 < len(opts) && strings.EqualFold(opts[i+1], "ANY") {
				any_ = true
				i++
			}
		case "ASC":
			asc = true
			hasOrder = true
		case "DESC":
			asc = false
			hasOrder = true
		case "STORE":
			if readOnly {
				writeError(c.bw, "ERR STORE option is not allowed in read-only mode")
				return
			}
			if i+1 >= len(opts) {
				writeError(c.bw, "syntax error: STORE dest")
				return
			}
			storeKey = opts[i+1]
			storeDist = false
			i++
		case "STOREDIST":
			if readOnly {
				writeError(c.bw, "ERR STOREDIST option is not allowed in read-only mode")
				return
			}
			if i+1 >= len(opts) {
				writeError(c.bw, "syntax error: STOREDIST dest")
				return
			}
			storeKey = opts[i+1]
			storeDist = true
			i++
		}
	}
	hits, err := c.eng.KV.GeoSearch(key, lat, lon, radius, unit, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if excludeMember != "" {
		filtered := hits[:0]
		for _, h := range hits {
			if h.Member != excludeMember {
				filtered = append(filtered, h)
			}
		}
		hits = filtered
	}
	if !hasOrder || asc {
		// GeoSearch already returns ascending — leave it.
	} else {
		for i, j := 0, len(hits)-1; i < j; i, j = i+1, j-1 {
			hits[i], hits[j] = hits[j], hits[i]
		}
	}
	_ = any_ // ANY only short-circuits server-side; we always honour COUNT
	if storeKey != "" {
		n, err := c.eng.KV.GeoSearchStore(storeKey, key, lat, lon, radius, unit, count, storeDist)
		if err != nil {
			c.writeStoreErr(err)
			return
		}
		writeInt(c.bw, int64(n))
		c.eng.RecordWrite("GEORADIUS", []string{storeKey, key})
		return
	}
	emitGeoRadiusResults(c, hits, withCoord, withDist, withHash)
}

// emitGeoRadiusResults serializes the hit list into the canonical
// GEORADIUS reply shape. Without any WITH* flag each row is just the
// member name; with flags rows become nested arrays.
func emitGeoRadiusResults(c *conn, hits []store.GeoSearchResult, withCoord, withDist, withHash bool) {
	if !withCoord && !withDist && !withHash {
		out := make([]string, len(hits))
		for i, h := range hits {
			out[i] = h.Member
		}
		writeArray(c.bw, out)
		return
	}
	out := make([]any, 0, len(hits))
	for _, h := range hits {
		row := []any{h.Member}
		if withDist {
			row = append(row, strconv.FormatFloat(h.Distance, 'f', 4, 64))
		}
		if withHash {
			// Re-derive the geohash from the stored zset score —
			// matches what the original GEOADD wrote in.
			sc, _, _ := c.eng.KV.ZScore(h.Member, h.Member)
			_ = sc
			row = append(row, int64(0))
		}
		if withCoord {
			row = append(row, []any{
				strconv.FormatFloat(h.Lon, 'f', 10, 64),
				strconv.FormatFloat(h.Lat, 'f', 10, 64),
			})
		}
		out = append(out, row)
	}
	writeValue(c.bw, out)
}
