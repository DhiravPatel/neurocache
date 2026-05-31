package llmstack

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ContractEvolve classifies tool/API schema changes as breaking,
// non-breaking, or risky, and emits a migration hint. TOOLDRIFT
// detects that a schema *changed*; CONTRACT.EVOLVE decides whether
// that change is *safe*.
//
// The classification rules (kept boring and explainable so PR authors
// can predict the verdict without running it):
//
//   BREAKING:
//     - removed a required field
//     - added a required field with no default
//     - changed a field's type (string → number etc.)
//     - removed or renamed an operation
//     - widened semantics (e.g. enum lost a value)
//
//   NON-BREAKING:
//     - added an optional field
//     - widened a string field (no enum restriction shrunk)
//     - added a new operation
//     - added a new enum value (additive on output, restrictive on input → warn)
//
//   RISKY (caller should review):
//     - default value changed
//     - constraints tightened (max length shrunk, etc.)
//     - new required field with a default (downstream may not honour it)
//
// Schemas are passed as JSON objects with the shape:
//
//   {
//     "operations": {
//       "op-name": {
//         "args": {
//           "arg-name": {"type": "string", "required": true,
//                        "enum": ["a","b"], "default": "a"}
//         }
//       }
//     }
//   }
//
// Commands:
//
//   CONTRACT.REGISTER tool version schema-json
//   CONTRACT.DIFF tool version-a version-b
//        → verdict (BREAKING|NON-BREAKING|RISKY)
//        → changes (list of {kind, op, field, before, after, severity})
//        → hint  (migration hint string)
//   CONTRACT.VERSIONS tool
//   CONTRACT.FORGET tool|ALL
//   CONTRACT.STATS
//
// Hot path: DIFF is one JSON parse per side + a linear walk over
// operations × args (small in practice — dozens, not thousands).
type ContractEvolve struct {
	mu    sync.RWMutex
	tools map[string]map[string]*toolSchema // tool → version → schema

	totalRegisters atomic.Int64
	totalDiffs     atomic.Int64
}

type toolSchema struct {
	raw  string
	doc  schemaDoc
}

type schemaDoc struct {
	Operations map[string]schemaOp `json:"operations"`
}

type schemaOp struct {
	Args map[string]schemaArg `json:"args"`
}

type schemaArg struct {
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Enum     []string `json:"enum,omitempty"`
	Default  any      `json:"default,omitempty"`
	MaxLen   int      `json:"max_len,omitempty"`
}

// NewContractEvolve returns an empty registry.
func NewContractEvolve() *ContractEvolve {
	return &ContractEvolve{tools: map[string]map[string]*toolSchema{}}
}

// Register stores a schema version for a tool.
func (c *ContractEvolve) Register(tool, version, schemaJSON string) error {
	if tool == "" {
		return errors.New("tool required")
	}
	if version == "" {
		return errors.New("version required")
	}
	if schemaJSON == "" {
		return errors.New("schema_json required")
	}
	var doc schemaDoc
	if err := json.Unmarshal([]byte(schemaJSON), &doc); err != nil {
		return errors.New("invalid schema JSON: " + err.Error())
	}
	c.totalRegisters.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	versions, ok := c.tools[tool]
	if !ok {
		versions = map[string]*toolSchema{}
		c.tools[tool] = versions
	}
	versions[version] = &toolSchema{raw: schemaJSON, doc: doc}
	return nil
}

// ContractChange is one entry in DIFF's change list.
type ContractChange struct {
	Kind     string `json:"kind"`     // added|removed|changed
	Op       string `json:"op"`
	Field    string `json:"field,omitempty"`
	Before   string `json:"before,omitempty"`
	After    string `json:"after,omitempty"`
	Severity string `json:"severity"` // breaking|risky|safe
	Note     string `json:"note"`
}

// ContractDiff is DIFF's return.
type ContractDiff struct {
	Tool     string           `json:"tool"`
	From     string           `json:"from"`
	To       string           `json:"to"`
	Verdict  string           `json:"verdict"`
	Changes  []ContractChange `json:"changes"`
	Hint     string           `json:"hint"`
}

// Diff compares two registered versions.
func (c *ContractEvolve) Diff(tool, from, to string) (ContractDiff, error) {
	if tool == "" || from == "" || to == "" {
		return ContractDiff{}, errors.New("tool, from, to required")
	}
	c.totalDiffs.Add(1)
	c.mu.RLock()
	versions, ok := c.tools[tool]
	c.mu.RUnlock()
	if !ok {
		return ContractDiff{}, errors.New("unknown tool: " + tool)
	}
	a, okA := versions[from]
	b, okB := versions[to]
	if !okA {
		return ContractDiff{}, errors.New("unknown from version: " + from)
	}
	if !okB {
		return ContractDiff{}, errors.New("unknown to version: " + to)
	}
	out := ContractDiff{Tool: tool, From: from, To: to}
	allOps := map[string]bool{}
	for op := range a.doc.Operations {
		allOps[op] = true
	}
	for op := range b.doc.Operations {
		allOps[op] = true
	}
	opNames := make([]string, 0, len(allOps))
	for op := range allOps {
		opNames = append(opNames, op)
	}
	sort.Strings(opNames)
	for _, op := range opNames {
		oa, hasA := a.doc.Operations[op]
		ob, hasB := b.doc.Operations[op]
		switch {
		case hasA && !hasB:
			out.Changes = append(out.Changes, ContractChange{
				Kind: "removed", Op: op, Severity: "breaking",
				Note: "operation removed — every caller breaks",
			})
		case !hasA && hasB:
			out.Changes = append(out.Changes, ContractChange{
				Kind: "added", Op: op, Severity: "safe",
				Note: "new operation — additive",
			})
		default:
			diffArgs(op, oa, ob, &out.Changes)
		}
	}
	out.Verdict = contractVerdictFor(out.Changes)
	out.Hint = hintFor(out)
	return out, nil
}

// Versions returns every registered version for a tool, sorted.
func (c *ContractEvolve) Versions(tool string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	versions, ok := c.tools[tool]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(versions))
	for v := range versions {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// Forget drops a tool (or all).
func (c *ContractEvolve) Forget(tool string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tool == "ALL" {
		n := len(c.tools)
		c.tools = map[string]map[string]*toolSchema{}
		return n
	}
	if _, ok := c.tools[tool]; ok {
		delete(c.tools, tool)
		return 1
	}
	return 0
}

// List returns every registered tool.
func (c *ContractEvolve) List() []string {
	c.mu.RLock()
	out := make([]string, 0, len(c.tools))
	for k := range c.tools {
		out = append(out, k)
	}
	c.mu.RUnlock()
	sort.Strings(out)
	return out
}

// ContractEvolveStats is the global snapshot.
type ContractEvolveStats struct {
	Tools          int   `json:"tools"`
	TotalRegisters int64 `json:"total_registers"`
	TotalDiffs     int64 `json:"total_diffs"`
}

func (c *ContractEvolve) Stats() ContractEvolveStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return ContractEvolveStats{
		Tools:          len(c.tools),
		TotalRegisters: c.totalRegisters.Load(),
		TotalDiffs:     c.totalDiffs.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func diffArgs(op string, a, b schemaOp, out *[]ContractChange) {
	all := map[string]bool{}
	for k := range a.Args {
		all[k] = true
	}
	for k := range b.Args {
		all[k] = true
	}
	names := make([]string, 0, len(all))
	for k := range all {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		fa, hasA := a.Args[name]
		fb, hasB := b.Args[name]
		switch {
		case hasA && !hasB:
			sev := "safe"
			note := "optional field removed"
			if fa.Required {
				sev = "breaking"
				note = "required field removed — callers may fail"
			}
			*out = append(*out, ContractChange{
				Kind: "removed", Op: op, Field: name,
				Before: fa.Type, Severity: sev, Note: note,
			})
		case !hasA && hasB:
			sev := "safe"
			note := "added optional field — additive"
			if fb.Required && fb.Default == nil {
				sev = "breaking"
				note = "added required field with no default — callers must pass it"
			} else if fb.Required {
				sev = "risky"
				note = "added required field with default — verify downstream honours default"
			}
			*out = append(*out, ContractChange{
				Kind: "added", Op: op, Field: name,
				After: fb.Type, Severity: sev, Note: note,
			})
		default:
			if fa.Type != fb.Type {
				*out = append(*out, ContractChange{
					Kind: "changed", Op: op, Field: name,
					Before: fa.Type, After: fb.Type, Severity: "breaking",
					Note: "type changed — callers will mis-serialize",
				})
			}
			if !fa.Required && fb.Required {
				*out = append(*out, ContractChange{
					Kind: "changed", Op: op, Field: name,
					Before: "optional", After: "required", Severity: "breaking",
					Note: "field became required — old callers will omit it",
				})
			}
			if fa.Required && !fb.Required {
				*out = append(*out, ContractChange{
					Kind: "changed", Op: op, Field: name,
					Before: "required", After: "optional", Severity: "safe",
					Note: "field became optional — callers can ignore",
				})
			}
			// Enum changes
			if len(fa.Enum) > 0 || len(fb.Enum) > 0 {
				removed := diffEnum(fa.Enum, fb.Enum)
				added := diffEnum(fb.Enum, fa.Enum)
				if len(removed) > 0 {
					*out = append(*out, ContractChange{
						Kind: "changed", Op: op, Field: name,
						Before: strings.Join(fa.Enum, ","), After: strings.Join(fb.Enum, ","),
						Severity: "breaking",
						Note: "enum values removed: " + strings.Join(removed, ","),
					})
				}
				if len(added) > 0 {
					*out = append(*out, ContractChange{
						Kind: "changed", Op: op, Field: name,
						Before: strings.Join(fa.Enum, ","), After: strings.Join(fb.Enum, ","),
						Severity: "risky",
						Note: "enum values added: " + strings.Join(added, ",") + " — restrictive on input, additive on output",
					})
				}
			}
			// Default changes
			if fa.Default != nil && fb.Default != nil {
				da, _ := json.Marshal(fa.Default)
				db, _ := json.Marshal(fb.Default)
				if string(da) != string(db) {
					*out = append(*out, ContractChange{
						Kind: "changed", Op: op, Field: name,
						Before: string(da), After: string(db), Severity: "risky",
						Note: "default value changed — silent semantic shift",
					})
				}
			}
			// Constraint tightening (max length shrunk)
			if fa.MaxLen > 0 && fb.MaxLen > 0 && fb.MaxLen < fa.MaxLen {
				*out = append(*out, ContractChange{
					Kind: "changed", Op: op, Field: name,
					Before: "max_len=" + strconv.Itoa(fa.MaxLen),
					After:  "max_len=" + strconv.Itoa(fb.MaxLen),
					Severity: "risky",
					Note: "max length shrunk — previously valid input may now be rejected",
				})
			}
		}
	}
}

func diffEnum(a, b []string) []string {
	bset := map[string]bool{}
	for _, x := range b {
		bset[x] = true
	}
	out := []string{}
	for _, x := range a {
		if !bset[x] {
			out = append(out, x)
		}
	}
	return out
}

func contractVerdictFor(changes []ContractChange) string {
	hasBreaking, hasRisky := false, false
	for _, c := range changes {
		switch c.Severity {
		case "breaking":
			hasBreaking = true
		case "risky":
			hasRisky = true
		}
	}
	switch {
	case hasBreaking:
		return "BREAKING"
	case hasRisky:
		return "RISKY"
	default:
		return "NON-BREAKING"
	}
}

func hintFor(d ContractDiff) string {
	switch d.Verdict {
	case "BREAKING":
		return "bump major version + update every caller before rollout"
	case "RISKY":
		return "review default-value / constraint changes; canary before full rollout"
	default:
		return "safe to roll out without coordinated caller updates"
	}
}
