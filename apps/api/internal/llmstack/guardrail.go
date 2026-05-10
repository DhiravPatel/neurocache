package llmstack

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// GuardrailManager runs composable safety pipelines. Every team
// shipping LLM features writes the same glue: "first scan for prompt
// injection, then strip PII, then check the model's answer is
// grounded in the retrieved context, then refuse if any stage fails."
// They re-implement it in every project, get the short-circuiting
// wrong, and forget to add new safety stages when threats evolve.
//
// GUARDRAIL.* gives the cache a single command — GUARDRAIL.RUN — that
// executes a named pipeline of stages and returns a per-stage verdict
// plus the final mutated text (post-redaction, etc.) in one round
// trip. Definitions are durable (survive restart via AOF) so ops
// teams can tune pipelines from a console without bouncing app
// servers.
//
// Stage types:
//
//   - inject:THRESHOLD       — INJECT.SCAN; fails if severity ≥ threshold
//                              (default 0.8). Apps usually fast-fail here.
//   - redact                 — REDACT.SCRUB; never fails, mutates the
//                              text + emits a restore_token. Subsequent
//                              stages see the redacted text.
//   - ground                 — GROUND.CHECK against caller-provided
//                              SOURCE passages; fails on reject.
//   - length:MAX             — fails if text length > MAX bytes.
//                              Cheap pre-flight cost guard.
//   - regex_block:NAME:PATTERN — fails if text matches the pattern.
//                              Useful for tenant-specific blocklists.
//   - custom:NAME            — caller-supplied verdict (for cases the
//                              cache can't grade locally, e.g. model-
//                              based moderation).
//
// Default behavior: stop on first fail. With ALL_STAGES=1 the pipeline
// runs every stage even after a fail (useful for telemetry — you want
// to know ALL the safety violations, not just the first).
type GuardrailManager struct {
	mu        sync.RWMutex
	pipelines map[string]*pipeline

	// Hooks to other engine primitives. Set at engine init via
	// SetEngine; nil by default so the package stays test-friendly.
	inject   *InjectScanner
	redactor *Redactor
	ground   *GroundChecker

	totalRuns  atomic.Int64
	totalPass  atomic.Int64
	totalFail  atomic.Int64
}

type pipeline struct {
	id     string
	stages []pipelineStage
}

type pipelineStage struct {
	kind      string // inject|redact|ground|length|regex_block|custom
	name      string // user-visible stage name (defaults to kind)
	threshold float64
	maxLen    int
	regex     *regexp.Regexp
}

// NewGuardrailManager returns an empty manager. SetEngine wires it
// to the inject/redact/ground primitives at engine init.
func NewGuardrailManager() *GuardrailManager {
	return &GuardrailManager{pipelines: map[string]*pipeline{}}
}

// SetEngine binds the manager to the live primitives. Called once
// at engine init.
func (g *GuardrailManager) SetEngine(inject *InjectScanner, redactor *Redactor, ground *GroundChecker) {
	g.inject = inject
	g.redactor = redactor
	g.ground = ground
}

// Define registers (or replaces) a pipeline. `spec` is a comma-
// separated list of stage descriptors:
//   "inject:0.8,redact,length:8000"
//   "inject,length:4000,regex_block:no_emails:[A-Za-z0-9._]+@"
func (g *GuardrailManager) Define(id, spec string) error {
	if id == "" {
		return errors.New("pipeline id required")
	}
	stages, err := parsePipeline(spec)
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.pipelines[id] = &pipeline{id: id, stages: stages}
	g.mu.Unlock()
	return nil
}

// Forget drops a pipeline.
func (g *GuardrailManager) Forget(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.pipelines[id]
	delete(g.pipelines, id)
	return ok
}

// PipelineRow is one row of GUARDRAIL.LIST.
type PipelineRow struct {
	ID    string   `json:"id"`
	Spec  string   `json:"spec"`
	Stage []string `json:"stages"`
}

// Pipelines returns every defined pipeline, sorted by id.
func (g *GuardrailManager) Pipelines() []PipelineRow {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]PipelineRow, 0, len(g.pipelines))
	for _, p := range g.pipelines {
		stages := make([]string, len(p.stages))
		for i, s := range p.stages {
			stages[i] = stageDescriptor(s)
		}
		out = append(out, PipelineRow{
			ID:    p.id,
			Spec:  strings.Join(stages, ","),
			Stage: stages,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RunOpts configures GUARDRAIL.RUN.
type RunOpts struct {
	Output     string   // optional model output (for ground stage)
	Sources    []string // optional source passages (for ground stage)
	AllStages  bool     // run every stage even after a fail
	CustomPass map[string]bool
}

// StageResult is one stage's verdict + details.
type StageResult struct {
	Name    string  `json:"name"`
	Kind    string  `json:"kind"`
	Pass    bool    `json:"pass"`
	Details string  `json:"details,omitempty"`
	// Optional payload — present when the stage produces something
	// downstream stages care about (redact's restore token, etc.).
	Token string `json:"token,omitempty"`
}

// RunResult is GUARDRAIL.RUN's full reply.
type RunResult struct {
	Pass      bool          `json:"pass"`
	Stages    []StageResult `json:"stages"`
	FinalText string        `json:"final_text"`
}

// Run executes a pipeline against `text` (the user-input prompt).
// For ground stages, opts.Output is the model response and
// opts.Sources are the retrieved passages. The pipeline mutates the
// "current text" as it progresses (redact stages return the scrubbed
// version that subsequent stages see).
func (g *GuardrailManager) Run(id, text string, opts RunOpts) (RunResult, bool) {
	g.mu.RLock()
	p, ok := g.pipelines[id]
	g.mu.RUnlock()
	if !ok {
		return RunResult{}, false
	}
	g.totalRuns.Add(1)

	res := RunResult{Pass: true, FinalText: text}
	for _, st := range p.stages {
		sr := g.runStage(st, &res, opts)
		res.Stages = append(res.Stages, sr)
		if !sr.Pass {
			res.Pass = false
			if !opts.AllStages {
				break
			}
		}
	}
	if res.Pass {
		g.totalPass.Add(1)
	} else {
		g.totalFail.Add(1)
	}
	return res, true
}

func (g *GuardrailManager) runStage(st pipelineStage, res *RunResult, opts RunOpts) StageResult {
	out := StageResult{Name: st.name, Kind: st.kind, Pass: true}
	switch st.kind {
	case "inject":
		if g.inject == nil {
			out.Pass = true
			out.Details = "no inject scanner wired"
			return out
		}
		severity, name, hit := g.inject.Scan(res.FinalText)
		if hit && severity >= st.threshold {
			out.Pass = false
			out.Details = fmt.Sprintf("hit pattern=%s severity=%.2f (>= %.2f)",
				name, severity, st.threshold)
		} else {
			out.Details = fmt.Sprintf("severity=%.2f (< %.2f)", severity, st.threshold)
		}
	case "redact":
		if g.redactor == nil {
			out.Pass = true
			out.Details = "no redactor wired"
			return out
		}
		s := g.redactor.Scrub(res.FinalText)
		res.FinalText = s.Text
		out.Token = s.RestoreToken
		out.Details = fmt.Sprintf("replaced=%d", sumReplacements(s.Replacements))
	case "ground":
		if g.ground == nil {
			out.Pass = true
			out.Details = "no ground checker wired"
			return out
		}
		if opts.Output == "" {
			out.Pass = true
			out.Details = "no output provided; skipped"
			return out
		}
		gr := g.ground.Check(opts.Output, opts.Sources)
		if gr.Verdict == "reject" {
			out.Pass = false
			out.Details = fmt.Sprintf("verdict=reject doc_score=%.4f", gr.DocScore)
		} else {
			out.Details = fmt.Sprintf("verdict=%s doc_score=%.4f", gr.Verdict, gr.DocScore)
		}
	case "length":
		l := len(res.FinalText)
		if l > st.maxLen {
			out.Pass = false
			out.Details = fmt.Sprintf("len=%d > max=%d", l, st.maxLen)
		} else {
			out.Details = fmt.Sprintf("len=%d", l)
		}
	case "regex_block":
		if st.regex.MatchString(res.FinalText) {
			out.Pass = false
			out.Details = "blocked by " + st.name
		} else {
			out.Details = "no match"
		}
	case "custom":
		if v, ok := opts.CustomPass[st.name]; ok {
			out.Pass = v
			if v {
				out.Details = "caller passed"
			} else {
				out.Details = "caller failed"
			}
		} else {
			// No verdict supplied → skip silently. This is the safe
			// default: if you forgot to pass the verdict for a custom
			// stage, you don't accidentally block traffic.
			out.Pass = true
			out.Details = "no verdict supplied; skipped"
		}
	}
	return out
}

// GuardrailStats is the global counters snapshot.
type GuardrailStats struct {
	TotalRuns int64 `json:"total_runs"`
	TotalPass int64 `json:"total_pass"`
	TotalFail int64 `json:"total_fail"`
	Pipelines int   `json:"pipelines"`
}

func (g *GuardrailManager) Stats() GuardrailStats {
	g.mu.RLock()
	n := len(g.pipelines)
	g.mu.RUnlock()
	return GuardrailStats{
		TotalRuns: g.totalRuns.Load(),
		TotalPass: g.totalPass.Load(),
		TotalFail: g.totalFail.Load(),
		Pipelines: n,
	}
}

// ─── helpers ───────────────────────────────────────────────────

func parsePipeline(spec string) ([]pipelineStage, error) {
	parts := strings.Split(spec, ",")
	out := make([]pipelineStage, 0, len(parts))
	for _, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		st, err := parseStage(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil, errors.New("pipeline must have at least one stage")
	}
	return out, nil
}

func parseStage(raw string) (pipelineStage, error) {
	st := pipelineStage{}
	// kind[:arg1[:arg2]]
	parts := strings.SplitN(raw, ":", 3)
	st.kind = strings.ToLower(parts[0])
	st.name = st.kind
	switch st.kind {
	case "inject":
		st.threshold = 0.8
		if len(parts) >= 2 {
			f, err := strconv.ParseFloat(parts[1], 64)
			if err != nil {
				return st, fmt.Errorf("inject threshold not a float: %s", parts[1])
			}
			st.threshold = f
		}
	case "redact":
		// no args
	case "ground":
		// no args; thresholds owned by GroundChecker
	case "length":
		if len(parts) < 2 {
			return st, errors.New("length stage needs a max-bytes argument")
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n <= 0 {
			return st, errors.New("length max must be positive integer")
		}
		st.maxLen = n
	case "regex_block":
		if len(parts) < 3 {
			return st, errors.New("regex_block needs name:pattern")
		}
		st.name = parts[1]
		re, err := regexp.Compile(parts[2])
		if err != nil {
			return st, fmt.Errorf("bad regex_block pattern: %w", err)
		}
		st.regex = re
	case "custom":
		if len(parts) < 2 {
			return st, errors.New("custom stage needs a name")
		}
		st.name = parts[1]
	default:
		return st, fmt.Errorf("unknown stage kind: %s", st.kind)
	}
	return st, nil
}

func stageDescriptor(s pipelineStage) string {
	switch s.kind {
	case "inject":
		return fmt.Sprintf("inject:%.2f", s.threshold)
	case "length":
		return fmt.Sprintf("length:%d", s.maxLen)
	case "regex_block":
		return fmt.Sprintf("regex_block:%s:%s", s.name, s.regex.String())
	case "custom":
		return "custom:" + s.name
	default:
		return s.kind
	}
}

func sumReplacements(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
