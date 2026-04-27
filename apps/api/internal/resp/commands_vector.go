package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/vectorindex"
)

// vaddCmd implements:
//
//	VADD key id vec [DIM n] [METRIC L2|IP|COSINE] [TYPE FLAT|HNSW]
//	     [M m] [EFCONSTRUCTION n] [EFRUNTIME n]
//	     [SETATTR json]
//
// On a fresh key, the trailing options configure the new index. On an
// existing key the options are ignored (you can't change the geometry
// after creation — VREM the key and start over). vec is the standard
// FP32 binary form (`dim*4` bytes) or a "1.0,2.0,3.0" CSV.
//
// Reply: 1 (id was new), 0 (id replaced an existing vector).
func (c *conn) vaddCmd(args []string) {
	if !c.wantArgs("VADD", args, 3) {
		return
	}
	key, id, raw := args[0], args[1], args[2]
	opts := vectorindex.Options{
		Algo:   vectorindex.AlgoHNSW,
		Metric: vectorindex.MetricCosine,
	}
	attr := ""
	hasAttr := false
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "DIM":
			if i+1 >= len(args) {
				writeError(c.bw, "DIM requires an integer")
				return
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil || v <= 0 {
				writeError(c.bw, "DIM must be a positive integer")
				return
			}
			opts.Dim = v
			i++
		case "METRIC":
			if i+1 >= len(args) {
				writeError(c.bw, "METRIC requires a value")
				return
			}
			m := strings.ToUpper(args[i+1])
			if m != "COSINE" && m != "L2" && m != "IP" {
				writeError(c.bw, "METRIC must be COSINE | L2 | IP")
				return
			}
			opts.Metric = vectorindex.Metric(m)
			i++
		case "TYPE":
			if i+1 >= len(args) {
				writeError(c.bw, "TYPE requires a value")
				return
			}
			t := strings.ToUpper(args[i+1])
			if t != "FLAT" && t != "HNSW" {
				writeError(c.bw, "TYPE must be FLAT | HNSW")
				return
			}
			opts.Algo = vectorindex.Algo(t)
			i++
		case "M":
			if i+1 >= len(args) {
				writeError(c.bw, "M requires an integer")
				return
			}
			opts.M, _ = strconv.Atoi(args[i+1])
			i++
		case "EFCONSTRUCTION":
			if i+1 >= len(args) {
				writeError(c.bw, "EFCONSTRUCTION requires an integer")
				return
			}
			opts.EFC, _ = strconv.Atoi(args[i+1])
			i++
		case "EFRUNTIME":
			if i+1 >= len(args) {
				writeError(c.bw, "EFRUNTIME requires an integer")
				return
			}
			opts.EFR, _ = strconv.Atoi(args[i+1])
			i++
		case "SETATTR":
			if i+1 >= len(args) {
				writeError(c.bw, "SETATTR requires a JSON value")
				return
			}
			attr = args[i+1]
			hasAttr = true
			i++
		}
	}
	// On a fresh key DIM is required; on an existing key it gets read
	// from the live index. Defer the dim check to the store helper.
	if existing, _, _ := c.eng.KV.VDim(key); existing > 0 {
		opts.Dim = existing
	}
	if opts.Dim == 0 {
		writeError(c.bw, "DIM is required for the first VADD on this key")
		return
	}
	vec, err := vectorindex.ParseVector(raw, opts.Dim)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	n, err := c.eng.KV.VAdd(key, id, vec, opts)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if hasAttr {
		// SetAttr is best-effort right after VADD — we already added
		// the vector so the id exists.
		if _, err := c.eng.KV.VSetAttr(key, id, attr); err != nil {
			c.writeStoreErr(err)
			return
		}
	}
	writeInt(c.bw, int64(n))
}

// vremCmd implements VREM key id [id ...]. Returns the count actually
// removed.
func (c *conn) vremCmd(args []string) {
	if !c.wantArgs("VREM", args, 2) {
		return
	}
	n, err := c.eng.KV.VRem(args[0], args[1:]...)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// vsimCmd implements VSIM key vec [COUNT n] [WITHSCORES] [WITHATTRS].
// Reply: array of ids; with WITHSCORES every id is followed by its
// distance; with WITHATTRS by its JSON attribute (or empty bulk when
// no attribute is set).
func (c *conn) vsimCmd(args []string) {
	if !c.wantArgs("VSIM", args, 2) {
		return
	}
	key, raw := args[0], args[1]
	count, withScores, withAttrs := 10, false, false
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "COUNT":
			if i+1 >= len(args) {
				writeError(c.bw, "COUNT requires an integer")
				return
			}
			count, _ = strconv.Atoi(args[i+1])
			i++
		case "WITHSCORES":
			withScores = true
		case "WITHATTRS":
			withAttrs = true
		}
	}
	dim, ok, err := c.eng.KV.VDim(key)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !ok {
		writeArray(c.bw, []string{})
		return
	}
	query, err := vectorindex.ParseVector(raw, dim)
	if err != nil {
		writeError(c.bw, err.Error())
		return
	}
	results, err := c.eng.KV.VSim(key, query, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	out := make([]any, 0, len(results)*3)
	for _, r := range results {
		out = append(out, r.ID)
		if withScores {
			out = append(out, strconv.FormatFloat(r.Distance, 'f', -1, 64))
		}
		if withAttrs {
			if v, present, _ := c.eng.KV.VGetAttr(key, r.ID); present {
				out = append(out, v)
			} else {
				out = append(out, "")
			}
		}
	}
	writeValue(c.bw, out)
}

// vembCmd implements VEMB key id. Returns the FP32 binary form.
func (c *conn) vembCmd(args []string) {
	if !c.wantArgs("VEMB", args, 2) {
		return
	}
	vec, ok, err := c.eng.KV.VEmb(args[0], args[1])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !ok {
		writeNil(c.bw)
		return
	}
	writeBulk(c.bw, vectorindex.EncodeVector(vec))
}

// vsetattrCmd implements VSETATTR key id <json>. Returns 1 (id
// existed and attr was set) or 0 (id missing — attr ignored).
func (c *conn) vsetattrCmd(args []string) {
	if !c.wantArgs("VSETATTR", args, 3) {
		return
	}
	ok, err := c.eng.KV.VSetAttr(args[0], args[1], args[2])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if ok {
		writeInt(c.bw, 1)
	} else {
		writeInt(c.bw, 0)
	}
}

// vgetattrCmd implements VGETATTR key id. Returns the JSON blob or
// nil when absent.
func (c *conn) vgetattrCmd(args []string) {
	if !c.wantArgs("VGETATTR", args, 2) {
		return
	}
	v, ok, err := c.eng.KV.VGetAttr(args[0], args[1])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !ok {
		writeNil(c.bw)
		return
	}
	writeBulk(c.bw, v)
}

// vdelattrCmd implements VDELATTR key id. Returns 1/0.
func (c *conn) vdelattrCmd(args []string) {
	if !c.wantArgs("VDELATTR", args, 2) {
		return
	}
	ok, err := c.eng.KV.VDelAttr(args[0], args[1])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if ok {
		writeInt(c.bw, 1)
	} else {
		writeInt(c.bw, 0)
	}
}

// vlinksCmd implements VLINKS key id. Returns nested arrays — one
// inner array per HNSW layer with that layer's neighbour ids. Empty
// outer array on FLAT indexes or when the id is missing.
func (c *conn) vlinksCmd(args []string) {
	if !c.wantArgs("VLINKS", args, 2) {
		return
	}
	layers, err := c.eng.KV.VLinks(args[0], args[1])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	out := make([]any, len(layers))
	for i, layer := range layers {
		row := make([]any, len(layer))
		for j, id := range layer {
			row[j] = id
		}
		out[i] = row
	}
	writeValue(c.bw, out)
}

// vinfoCmd implements VINFO key. Returns a flat field/value map.
func (c *conn) vinfoCmd(args []string) {
	if !c.wantArgs("VINFO", args, 1) {
		return
	}
	info, ok, err := c.eng.KV.VInfo(args[0])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !ok {
		writeNilArray(c.bw)
		return
	}
	writeValue(c.bw, []any{
		"algo", info.Algo,
		"dim", int64(info.Dim),
		"metric", info.Metric,
		"m", int64(info.M),
		"ef-construction", int64(info.EFC),
		"ef-runtime", int64(info.EFR),
		"card", int64(info.Card),
		"bytes-approx", info.BytesApprox,
	})
}

// vcardCmd implements VCARD key.
func (c *conn) vcardCmd(args []string) {
	if !c.wantArgs("VCARD", args, 1) {
		return
	}
	n, err := c.eng.KV.VCard(args[0])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	writeInt(c.bw, int64(n))
}

// vdimCmd implements VDIM key.
func (c *conn) vdimCmd(args []string) {
	if !c.wantArgs("VDIM", args, 1) {
		return
	}
	d, ok, err := c.eng.KV.VDim(args[0])
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !ok {
		writeNil(c.bw)
		return
	}
	writeInt(c.bw, int64(d))
}

// vrandmemberCmd implements VRANDMEMBER key [count]. Behaviour mirrors
// SRANDMEMBER (single id when no count, unique cap when positive,
// with-replacement sample when negative).
func (c *conn) vrandmemberCmd(args []string) {
	if !c.wantArgs("VRANDMEMBER", args, 1) {
		return
	}
	hasCount := false
	count := 0
	if len(args) >= 2 {
		hasCount = true
		v, err := strconv.Atoi(args[1])
		if err != nil {
			writeError(c.bw, "value is not an integer")
			return
		}
		count = v
	}
	out, err := c.eng.KV.VRandMember(args[0], count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	if !hasCount {
		if len(out) == 0 {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, out[0])
		return
	}
	writeArray(c.bw, out)
}

// vscanCmd implements VSCAN key cursor [MATCH pattern] [COUNT n].
func (c *conn) vscanCmd(args []string) {
	if !c.wantArgs("VSCAN", args, 2) {
		return
	}
	cursor, err := strconv.Atoi(args[1])
	if err != nil {
		writeError(c.bw, "cursor must be an integer")
		return
	}
	pattern := ""
	count := 10
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "MATCH":
			if i+1 < len(args) {
				pattern = args[i+1]
				i++
			}
		case "COUNT":
			if i+1 < len(args) {
				count, _ = strconv.Atoi(args[i+1])
				i++
			}
		}
	}
	next, page, err := c.eng.KV.VScan(args[0], cursor, pattern, count)
	if err != nil {
		c.writeStoreErr(err)
		return
	}
	pageOut := make([]any, len(page))
	for i, id := range page {
		pageOut[i] = id
	}
	writeValue(c.bw, []any{strconv.Itoa(next), pageOut})
}
