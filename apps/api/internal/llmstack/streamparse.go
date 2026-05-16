package llmstack

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
)

// StreamParser extracts complete top-level fields from streaming
// LLM JSON output as tokens arrive — instead of forcing apps to
// wait for the entire response. Real production pain: a 4-second
// LLM call producing structured output means 4 seconds before the
// UI can render anything; STREAM.PARSE lets the first field
// (often produced in the first ~200ms) hit the UI immediately.
//
// STREAM.PARSE.* commands:
//
//   STREAM.PARSE.OPEN stream-id
//   STREAM.PARSE.PUSH stream-id chunk
//        → array of {key, value, json_type} for newly-completed
//          top-level fields. Nested values are emitted as raw
//          JSON strings (caller can recursively parse if needed).
//   STREAM.PARSE.COMPLETE stream-id
//        → final flush + cleanup
//   STREAM.PARSE.STATUS stream-id
//   STREAM.PARSE.FORGET stream-id
//   STREAM.PARSE.STATS
//
// Implementation: a simple state machine over the input buffer.
// We assume valid JSON producing a top-level object (the standard
// shape for LLM structured output). Strings are escaped per JSON
// rules; objects and arrays are tracked by depth counter, emitted
// as raw JSON when their balanced closing delimiter arrives.
//
// Throughput target: PUSH at <2 µs per typical chunk (~50 bytes).
type StreamParser struct {
	mu      sync.Mutex
	streams map[string]*parseStream

	totalOpens     atomic.Int64
	totalPushes    atomic.Int64
	totalCompletes atomic.Int64
	totalFields    atomic.Int64
}

type parseStream struct {
	mu        sync.Mutex
	buf       []byte
	pos       int        // index into buf
	state     parseState
	depth     int        // 0 = outside top object, 1 = directly inside top object
	stringEsc bool       // inside a string + escape character active
	pendingKey string
	keyStart  int
	valStart  int
	emitted   int
}

type parseState int

const (
	psSeekOpen parseState = iota // before the top-level '{'
	psSeekKey                    // inside top object, expecting a "key" or '}'
	psInKey                      // inside the key string
	psSeekColon                  // after key, expecting ':'
	psSeekValue                  // after colon, expecting a value
	psInString                   // inside a string value
	psInPrimitive                // inside number / true / false / null
	psInObject                   // inside a nested object value
	psInArray                    // inside an array value
	psSeekComma                  // after value, expecting ',' or '}'
	psDone                       // saw top-level '}'
)

// NewStreamParser returns an empty parser registry.
func NewStreamParser() *StreamParser {
	return &StreamParser{streams: map[string]*parseStream{}}
}

// Open registers a new stream.
func (s *StreamParser) Open(streamID string) error {
	if streamID == "" {
		return errors.New("stream_id required")
	}
	s.mu.Lock()
	s.streams[streamID] = &parseStream{
		buf:   make([]byte, 0, 256),
		state: psSeekOpen,
	}
	s.mu.Unlock()
	s.totalOpens.Add(1)
	return nil
}

// ParsedField is one row of PUSH output.
type ParsedField struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	JSONType string `json:"json_type"` // string | number | boolean | null | object | array
}

// Push appends a chunk to the stream buffer and returns any
// newly-completed top-level fields.
func (s *StreamParser) Push(streamID string, chunk string) ([]ParsedField, error) {
	s.totalPushes.Add(1)
	s.mu.Lock()
	st, ok := s.streams[streamID]
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("unknown stream_id: " + streamID)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.buf = append(st.buf, chunk...)
	return st.advance(s)
}

// Complete returns any remaining unparsed buffer + drops the stream.
type CompleteResult struct {
	UnparsedBytes int    `json:"unparsed_bytes"`
	Buffer        string `json:"buffer,omitempty"`
	FieldsEmitted int    `json:"fields_emitted"`
}

// Complete flushes + drops the stream.
func (s *StreamParser) Complete(streamID string) (CompleteResult, bool) {
	s.totalCompletes.Add(1)
	s.mu.Lock()
	st, ok := s.streams[streamID]
	if !ok {
		s.mu.Unlock()
		return CompleteResult{}, false
	}
	delete(s.streams, streamID)
	s.mu.Unlock()
	st.mu.Lock()
	defer st.mu.Unlock()
	return CompleteResult{
		UnparsedBytes: len(st.buf) - st.pos,
		Buffer:        string(st.buf[st.pos:]),
		FieldsEmitted: st.emitted,
	}, true
}

// StatusResult is the STATUS return.
type StatusResult struct {
	Pos           int    `json:"pos"`
	Bytes         int    `json:"bytes"`
	Depth         int    `json:"depth"`
	Done          bool   `json:"done"`
	FieldsEmitted int    `json:"fields_emitted"`
}

// Status returns the per-stream snapshot.
func (s *StreamParser) Status(streamID string) (StatusResult, bool) {
	s.mu.Lock()
	st, ok := s.streams[streamID]
	s.mu.Unlock()
	if !ok {
		return StatusResult{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return StatusResult{
		Pos:           st.pos,
		Bytes:         len(st.buf),
		Depth:         st.depth,
		Done:          st.state == psDone,
		FieldsEmitted: st.emitted,
	}, true
}

// Forget drops a stream without flushing.
func (s *StreamParser) Forget(streamID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.streams[streamID]
	delete(s.streams, streamID)
	return ok
}

// StreamParseStats is the global snapshot.
type StreamParseStats struct {
	ActiveStreams  int   `json:"active_streams"`
	TotalOpens     int64 `json:"total_opens"`
	TotalPushes    int64 `json:"total_pushes"`
	TotalCompletes int64 `json:"total_completes"`
	TotalFields    int64 `json:"total_fields"`
}

func (s *StreamParser) Stats() StreamParseStats {
	s.mu.Lock()
	n := len(s.streams)
	s.mu.Unlock()
	return StreamParseStats{
		ActiveStreams:  n,
		TotalOpens:     s.totalOpens.Load(),
		TotalPushes:    s.totalPushes.Load(),
		TotalCompletes: s.totalCompletes.Load(),
		TotalFields:    s.totalFields.Load(),
	}
}

// ─── state machine ──────────────────────────────────────────────

func (st *parseStream) advance(parent *StreamParser) ([]ParsedField, error) {
	out := make([]ParsedField, 0, 4)
	for st.pos < len(st.buf) {
		c := st.buf[st.pos]
		switch st.state {
		case psSeekOpen:
			if isJSONSpace(c) {
				st.pos++
				continue
			}
			if c != '{' {
				return out, errors.New("expected '{' at start of top object, got " + string(c))
			}
			st.depth = 1
			st.state = psSeekKey
			st.pos++
		case psSeekKey:
			if isJSONSpace(c) || c == ',' {
				st.pos++
				continue
			}
			if c == '}' {
				st.state = psDone
				st.pos++
				return out, nil
			}
			if c != '"' {
				return out, errors.New("expected '\"' to start key, got " + string(c))
			}
			st.pos++
			st.keyStart = st.pos
			st.state = psInKey
			st.stringEsc = false
		case psInKey:
			if st.stringEsc {
				st.stringEsc = false
				st.pos++
				continue
			}
			if c == '\\' {
				st.stringEsc = true
				st.pos++
				continue
			}
			if c == '"' {
				rawKey := string(st.buf[st.keyStart:st.pos])
				// Unescape the JSON key string
				var k string
				if err := json.Unmarshal([]byte("\""+rawKey+"\""), &k); err != nil {
					return out, err
				}
				st.pendingKey = k
				st.pos++
				st.state = psSeekColon
				continue
			}
			st.pos++
		case psSeekColon:
			if isJSONSpace(c) {
				st.pos++
				continue
			}
			if c != ':' {
				return out, errors.New("expected ':' after key, got " + string(c))
			}
			st.pos++
			st.state = psSeekValue
		case psSeekValue:
			if isJSONSpace(c) {
				st.pos++
				continue
			}
			st.valStart = st.pos
			switch c {
			case '"':
				st.pos++
				st.state = psInString
				st.stringEsc = false
			case '{':
				st.pos++
				st.depth++
				st.state = psInObject
			case '[':
				st.pos++
				st.depth++
				st.state = psInArray
			default:
				st.state = psInPrimitive
			}
		case psInString:
			if st.stringEsc {
				st.stringEsc = false
				st.pos++
				continue
			}
			if c == '\\' {
				st.stringEsc = true
				st.pos++
				continue
			}
			if c == '"' {
				st.pos++
				raw := st.buf[st.valStart:st.pos]
				var v string
				if err := json.Unmarshal(raw, &v); err != nil {
					return out, err
				}
				out = append(out, ParsedField{Key: st.pendingKey, Value: v, JSONType: "string"})
				st.emitted++
				parent.totalFields.Add(1)
				st.state = psSeekComma
				continue
			}
			st.pos++
		case psInPrimitive:
			if c == ',' || c == '}' || isJSONSpace(c) {
				raw := st.buf[st.valStart:st.pos]
				jsonType, parsed, err := classifyPrimitive(raw)
				if err != nil {
					return out, err
				}
				out = append(out, ParsedField{Key: st.pendingKey, Value: parsed, JSONType: jsonType})
				st.emitted++
				parent.totalFields.Add(1)
				st.state = psSeekComma
				continue
			}
			st.pos++
		case psInObject, psInArray:
			// Track balanced braces/brackets (string-aware)
			if st.stringEsc {
				st.stringEsc = false
				st.pos++
				continue
			}
			if c == '\\' {
				st.stringEsc = true
				st.pos++
				continue
			}
			// Need a sub-state for "inside a string nested in a value".
			// Simplest: track an extra `inNestedString` flag.
			// To keep state count low, track it via a separate field
			// not in psState. Use stringEsc + check current byte: if
			// we hit '"' and previous wasn't an escape, toggle.
			if c == '"' {
				// toggle nested string mode (reusing stringEsc as inString)
				// — we treat stringEsc as the nested-string flag here.
				st.stringEsc = true // sentinel: we are now inside a nested string
				st.pos++
				// Walk until the closing '"' (handling escapes inline)
				escape := false
				for st.pos < len(st.buf) {
					nc := st.buf[st.pos]
					if escape {
						escape = false
						st.pos++
						continue
					}
					if nc == '\\' {
						escape = true
						st.pos++
						continue
					}
					if nc == '"' {
						st.pos++
						break
					}
					st.pos++
				}
				st.stringEsc = false
				continue
			}
			if c == '{' || c == '[' {
				st.depth++
				st.pos++
				continue
			}
			if c == '}' || c == ']' {
				st.depth--
				st.pos++
				if st.depth == 1 {
					raw := st.buf[st.valStart:st.pos]
					t := "object"
					if c == ']' {
						t = "array"
					}
					out = append(out, ParsedField{Key: st.pendingKey, Value: string(raw), JSONType: t})
					st.emitted++
					parent.totalFields.Add(1)
					st.state = psSeekComma
				}
				continue
			}
			st.pos++
		case psSeekComma:
			if isJSONSpace(c) {
				st.pos++
				continue
			}
			if c == ',' {
				st.pos++
				st.state = psSeekKey
				continue
			}
			if c == '}' {
				st.depth--
				st.state = psDone
				st.pos++
				return out, nil
			}
			return out, errors.New("expected ',' or '}' after value, got " + string(c))
		case psDone:
			st.pos++ // consume trailing whitespace silently
		}
	}
	return out, nil
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func classifyPrimitive(raw []byte) (string, string, error) {
	// Trim trailing whitespace if any
	end := len(raw)
	for end > 0 && isJSONSpace(raw[end-1]) {
		end--
	}
	s := string(raw[:end])
	switch s {
	case "true", "false":
		return "boolean", s, nil
	case "null":
		return "null", s, nil
	}
	if len(s) == 0 {
		return "", "", errors.New("empty primitive")
	}
	c := s[0]
	if (c >= '0' && c <= '9') || c == '-' {
		return "number", s, nil
	}
	return "", "", errors.New("unrecognised primitive: " + s)
}
