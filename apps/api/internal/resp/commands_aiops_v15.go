package resp

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"
)

// matryoshkaCmd handles MATRYOSHKA.* — hierarchical embedding retrieval.
//
//   MATRYOSHKA.SET matrix-id row-id v,v,v,...
//   MATRYOSHKA.DEL matrix-id row-id
//   MATRYOSHKA.TOPK matrix-id query-vec K [SHORTLIST n] [FILTER prefix]
//   MATRYOSHKA.LEN matrix-id
//   MATRYOSHKA.FORGET matrix-id
//   MATRYOSHKA.STATS
func (c *conn) matryoshkaCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'matryoshka.set'")
			return
		}
		vec, err := parseVecCSV(args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.Matryoshka.Set(args[0], args[1], vec); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "DEL":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'matryoshka.del'")
			return
		}
		if c.eng.Matryoshka.Del(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "TOPK":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'matryoshka.topk'")
			return
		}
		query, err := parseVecCSV(args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		k, err := strconv.Atoi(args[2])
		if err != nil || k <= 0 {
			writeError(c.bw, "K must be a positive integer")
			return
		}
		opts := llmstack.MatryoshkaOpts{K: k}
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			switch key {
			case "SHORTLIST":
				n, err := strconv.Atoi(val)
				if err != nil || n <= 0 {
					writeError(c.bw, "SHORTLIST must be a positive integer")
					return
				}
				opts.Shortlist = n
			case "FILTER":
				opts.Filter = val
			default:
				writeError(c.bw, "unknown MATRYOSHKA.TOPK option: "+key)
				return
			}
			i += 2
		}
		hits := c.eng.Matryoshka.TopK(args[0], query, opts)
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, []any{
				"row_id", h.RowID,
				"score", strconv.FormatFloat(h.Score, 'f', 6, 64),
			})
		}
		writeValue(c.bw, out)
	case "LEN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'matryoshka.len'")
			return
		}
		n, _ := c.eng.Matryoshka.Len(args[0])
		writeInt(c.bw, int64(n))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'matryoshka.forget'")
			return
		}
		writeInt(c.bw, int64(c.eng.Matryoshka.Forget(args[0])))
	case "STATS":
		s := c.eng.Matryoshka.Stats()
		matsAny := make([]any, 0, len(s.Matrices))
		for _, m := range s.Matrices {
			matsAny = append(matsAny, []any{
				"matrix_id", m.MatrixID,
				"rows", strconv.Itoa(m.Rows),
				"dim", strconv.Itoa(m.Dim),
			})
		}
		writeValue(c.bw, []any{
			"matrices", matsAny,
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"total_topks", strconv.FormatInt(s.TotalTopKs, 10),
			"total_rows", strconv.FormatInt(s.TotalRows, 10),
		})
	default:
		writeError(c.bw, "unknown MATRYOSHKA subcommand: "+sub)
	}
}

// vecQuantCmd handles VEC.QUANT.* — int8 quantized matrix.
//
//   VEC.QUANT.SET matrix-id row-id v,v,v,...
//   VEC.QUANT.DEL matrix-id row-id
//   VEC.QUANT.TOPK matrix-id query-vec K [FILTER prefix]
//   VEC.QUANT.COSINE matrix-id row-a row-b
//   VEC.QUANT.LEN matrix-id
//   VEC.QUANT.FORGET matrix-id
//   VEC.QUANT.STATS
func (c *conn) vecQuantCmd(sub string, args []string) {
	switch sub {
	case "SET":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'vec.quant.set'")
			return
		}
		vec, err := parseVecCSV(args[2])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		if err := c.eng.VecQuant.Set(args[0], args[1], vec); err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeSimple(c.bw, "OK")
	case "DEL":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'vec.quant.del'")
			return
		}
		if c.eng.VecQuant.Del(args[0], args[1]) {
			writeInt(c.bw, 1)
		} else {
			writeInt(c.bw, 0)
		}
	case "TOPK":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'vec.quant.topk'")
			return
		}
		query, err := parseVecCSV(args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		k, err := strconv.Atoi(args[2])
		if err != nil || k <= 0 {
			writeError(c.bw, "K must be a positive integer")
			return
		}
		opts := llmstack.TopKOpts{K: k}
		i := 3
		for i+1 < len(args)+1 && i < len(args) {
			key := strings.ToUpper(args[i])
			if i+1 >= len(args) {
				writeError(c.bw, key+" needs a value")
				return
			}
			val := args[i+1]
			if key != "FILTER" {
				writeError(c.bw, "unknown VEC.QUANT.TOPK option: "+key)
				return
			}
			opts.Filter = val
			i += 2
		}
		hits := c.eng.VecQuant.TopK(args[0], query, opts)
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, []any{
				"row_id", h.RowID,
				"score", strconv.FormatFloat(h.Score, 'f', 6, 64),
			})
		}
		writeValue(c.bw, out)
	case "COSINE":
		if len(args) < 3 {
			writeError(c.bw, "wrong number of arguments for 'vec.quant.cosine'")
			return
		}
		v, ok := c.eng.VecQuant.Cosine(args[0], args[1], args[2])
		if !ok {
			writeNil(c.bw)
			return
		}
		writeBulk(c.bw, strconv.FormatFloat(v, 'f', 6, 64))
	case "LEN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'vec.quant.len'")
			return
		}
		n, _ := c.eng.VecQuant.Len(args[0])
		writeInt(c.bw, int64(n))
	case "FORGET":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'vec.quant.forget'")
			return
		}
		writeInt(c.bw, int64(c.eng.VecQuant.Forget(args[0])))
	case "STATS":
		s := c.eng.VecQuant.Stats()
		matsAny := make([]any, 0, len(s.Matrices))
		for _, m := range s.Matrices {
			matsAny = append(matsAny, []any{
				"matrix_id", m.MatrixID,
				"rows", strconv.Itoa(m.Rows),
				"dim", strconv.Itoa(m.Dim),
			})
		}
		writeValue(c.bw, []any{
			"matrices", matsAny,
			"total_sets", strconv.FormatInt(s.TotalSets, 10),
			"total_topks", strconv.FormatInt(s.TotalTopKs, 10),
			"total_rows", strconv.FormatInt(s.TotalRows, 10),
			"bytes_per_row_sample", strconv.Itoa(s.BytesPerRowSample),
		})
	default:
		writeError(c.bw, "unknown VEC.QUANT subcommand: "+sub)
	}
}

// embedPoolCmd handles EMBED.POOL.* — stateless bulk pooling.
//
//   EMBED.POOL.MEAN v1,...|v2,...|v3,...
//   EMBED.POOL.MAX v1,...|v2,...
//   EMBED.POOL.WEIGHTED w1,w2,w3 v1,...|v2,...|v3,...
//   EMBED.POOL.NORM_SUM v1,...|v2,...
//   EMBED.POOL.STATS
func (c *conn) embedPoolCmd(sub string, args []string) {
	switch sub {
	case "MEAN":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'embed.pool.mean'")
			return
		}
		vecs, err := parsePoolVecs(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out, err := c.eng.EmbedPool.Mean(vecs)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, formatVecCSV(out))
	case "MAX":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'embed.pool.max'")
			return
		}
		vecs, err := parsePoolVecs(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out, err := c.eng.EmbedPool.Max(vecs)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, formatVecCSV(out))
	case "WEIGHTED":
		if len(args) < 2 {
			writeError(c.bw, "wrong number of arguments for 'embed.pool.weighted'")
			return
		}
		weights, err := parseVecCSV(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		vecs, err := parsePoolVecs(args[1])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out, err := c.eng.EmbedPool.Weighted(weights, vecs)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, formatVecCSV(out))
	case "NORM_SUM":
		if len(args) < 1 {
			writeError(c.bw, "wrong number of arguments for 'embed.pool.norm_sum'")
			return
		}
		vecs, err := parsePoolVecs(args[0])
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		out, err := c.eng.EmbedPool.NormSum(vecs)
		if err != nil {
			writeError(c.bw, err.Error())
			return
		}
		writeBulk(c.bw, formatVecCSV(out))
	case "STATS":
		s := c.eng.EmbedPool.Stats()
		writeArray(c.bw, []string{
			"total_means", strconv.FormatInt(s.TotalMeans, 10),
			"total_maxes", strconv.FormatInt(s.TotalMaxes, 10),
			"total_weighted", strconv.FormatInt(s.TotalWeighted, 10),
			"total_norm_sum", strconv.FormatInt(s.TotalNormSum, 10),
			"total_vecs_in", strconv.FormatInt(s.TotalVecsIn, 10),
		})
	default:
		writeError(c.bw, "unknown EMBED.POOL subcommand: "+sub)
	}
}

// parsePoolVecs splits "v1,v2,v3|v1,v2,v3|v1,v2,v3" into [][]float64.
func parsePoolVecs(arg string) ([][]float64, error) {
	parts := strings.Split(arg, "|")
	out := make([][]float64, 0, len(parts))
	for _, p := range parts {
		vec, err := parseVecCSV(p)
		if err != nil {
			return nil, err
		}
		out = append(out, vec)
	}
	return out, nil
}

func formatVecCSV(vec []float64) string {
	var b strings.Builder
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(v, 'f', 6, 64))
	}
	return b.String()
}
