// Package searchmod implements a production-ready subset of the
// RediSearch (FT.*) command surface as a NeuroCache module.
//
// Subset scope:
//
//   - Field types: TEXT, NUMERIC, TAG (GEO and VECTOR are deferred).
//   - Schema flags: SORTABLE, NOINDEX, NOSTEM, WEIGHT.
//   - Boolean queries: AND (whitespace), OR (|), NOT (-).
//   - Field queries: @field:term, @field:[lo hi], @field:{tag1|tag2}.
//   - Phrase queries: "exact phrase".
//   - Prefix queries: term*.
//   - BM25-style scoring with inverse document frequency + per-field weights.
//   - FT.AGGREGATE pipeline: GROUPBY, REDUCE (COUNT/SUM/MIN/MAX/AVG/
//     COUNT_DISTINCT/FIRST_VALUE/TOLIST), SORTBY, LIMIT, APPLY (simple
//     arithmetic + field references).
//
// Deferred — each warrants its own session:
//   - GEO and VECTOR fields, fuzzy queries (~), suggestions (FT.SUGADD/
//     SUGGET), spellcheck, synonyms, server-side cursors, profiling.
package searchmod

import (
	"errors"
	"strconv"
	"strings"
)

// FieldType enumerates the indexable field shapes.
type FieldType int

const (
	FieldText FieldType = iota
	FieldNumeric
	FieldTag
	FieldGeo
	FieldVector
)

// String renders the field type for FT.INFO output.
func (f FieldType) String() string {
	switch f {
	case FieldNumeric:
		return "NUMERIC"
	case FieldTag:
		return "TAG"
	case FieldGeo:
		return "GEO"
	case FieldVector:
		return "VECTOR"
	}
	return "TEXT"
}

// FieldSpec describes one schema field. Multiple fields can be marked
// SORTABLE (cheap range scans) or NOINDEX (stored but not searchable).
type FieldSpec struct {
	Name     string
	Type     FieldType
	Weight   float64 // TEXT only; default 1.0
	Sortable bool
	NoIndex  bool
	NoStem   bool
	TagSep   string // TAG only; default ","

	// VECTOR-only attributes.
	VectorAlgo     string // "FLAT" | "HNSW"
	VectorDim      int
	VectorMetric   string // "L2" | "IP" | "COSINE"
	VectorM        int    // HNSW: graph degree (default 16)
	VectorEFConstr int    // HNSW: ef_construction (default 200)
	VectorEFRun    int    // HNSW: ef_runtime (default 10)
}

// Schema is the index's field definition.
type Schema struct {
	Fields []*FieldSpec
}

// Field returns the spec for name, or nil.
func (s *Schema) Field(name string) *FieldSpec {
	for _, f := range s.Fields {
		if strings.EqualFold(f.Name, name) {
			return f
		}
	}
	return nil
}

// ParseSchema reads a SCHEMA clause: alternating name + type + flags.
//
//	SCHEMA name TEXT [WEIGHT n] [SORTABLE] [NOINDEX] [NOSTEM]
//	       qty  NUMERIC [SORTABLE] [NOINDEX]
//	       tags TAG [SEPARATOR ,] [SORTABLE]
func ParseSchema(args []string) (*Schema, error) {
	s := &Schema{}
	i := 0
	for i < len(args) {
		name := args[i]
		i++
		if i >= len(args) {
			return nil, errors.New("schema: missing type for field " + name)
		}
		typ := strings.ToUpper(args[i])
		i++
		f := &FieldSpec{Name: name, Weight: 1.0, TagSep: ","}
		switch typ {
		case "TEXT":
			f.Type = FieldText
		case "NUMERIC":
			f.Type = FieldNumeric
		case "TAG":
			f.Type = FieldTag
		case "GEO":
			f.Type = FieldGeo
		case "VECTOR":
			f.Type = FieldVector
			// VECTOR fields take a sub-clause:
			//   VECTOR <algo> <numattrs> attr value [attr value ...]
			if i+1 >= len(args) {
				return nil, errors.New("VECTOR field needs an algorithm")
			}
			f.VectorAlgo = strings.ToUpper(args[i])
			i++
			f.VectorM, f.VectorEFConstr, f.VectorEFRun = 16, 200, 10
			if i >= len(args) {
				return nil, errors.New("VECTOR field needs an attr count")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil {
				return nil, errors.New("VECTOR: attr count must be integer")
			}
			i++
			if i+n*2 > len(args) {
				return nil, errors.New("VECTOR: attr list too short")
			}
			for j := 0; j < n; j++ {
				key := strings.ToUpper(args[i])
				val := args[i+1]
				i += 2
				switch key {
				case "TYPE":
					// FLOAT32 only — we don't store binary precision flag
				case "DIM":
					f.VectorDim, _ = strconv.Atoi(val)
				case "DISTANCE_METRIC":
					f.VectorMetric = strings.ToUpper(val)
				case "M":
					f.VectorM, _ = strconv.Atoi(val)
				case "EF_CONSTRUCTION":
					f.VectorEFConstr, _ = strconv.Atoi(val)
				case "EF_RUNTIME":
					f.VectorEFRun, _ = strconv.Atoi(val)
				}
			}
		default:
			return nil, errors.New("schema: unknown field type " + typ)
		}
		// Consume per-field flags until we hit the next field name (a token
		// not in the flag vocabulary).
		for i < len(args) {
			switch strings.ToUpper(args[i]) {
			case "WEIGHT":
				if i+1 >= len(args) {
					return nil, errors.New("WEIGHT needs a value")
				}
				w, err := strconv.ParseFloat(args[i+1], 64)
				if err != nil {
					return nil, errors.New("invalid WEIGHT")
				}
				f.Weight = w
				i += 2
			case "SORTABLE":
				f.Sortable = true
				i++
			case "NOINDEX":
				f.NoIndex = true
				i++
			case "NOSTEM":
				f.NoStem = true
				i++
			case "SEPARATOR":
				if i+1 >= len(args) {
					return nil, errors.New("SEPARATOR needs a value")
				}
				f.TagSep = args[i+1]
				i += 2
			default:
				goto done
			}
		}
	done:
		s.Fields = append(s.Fields, f)
	}
	return s, nil
}
