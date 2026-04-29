package resp

import (
	"bufio"
	"strconv"

	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

// respWriter adapts a bufio.Writer to the modules.Writer interface so
// module command handlers can produce RESP replies without knowing
// anything about the wire format. The engine owns frame ordering.
type respWriter struct{ w *bufio.Writer }

func (r *respWriter) SimpleString(s string) { writeSimple(r.w, s) }
func (r *respWriter) Bulk(s string)         { writeBulk(r.w, s) }
func (r *respWriter) Int(n int64)           { writeInt(r.w, n) }
func (r *respWriter) Float(f float64)       { writeFloat(r.w, f) }
func (r *respWriter) Error(msg string)      { writeError(r.w, msg) }
func (r *respWriter) Nil()                  { writeNil(r.w) }
func (r *respWriter) NilArray()             { writeNilArray(r.w) }
func (r *respWriter) Array(items []any)     { writeValue(r.w, items) }

// _ asserts the interface is satisfied at compile time so accidental
// drift breaks the build, not a runtime client connection.
var _ modules.Writer = (*respWriter)(nil)

// formatTypeID exists for MODULE LIST cosmetics — turn a fixed-byte
// id into a human-friendly trimmed string.
func formatTypeID(id modules.TypeID) string {
	end := len(id)
	for end > 0 && id[end-1] == 0 {
		end--
	}
	if end == 0 {
		return "0x" + strconv.FormatUint(uint64(id[0]), 16)
	}
	return string(id[:end])
}
