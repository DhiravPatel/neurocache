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
	"time"
)

// JudgeSuite is an LLM-as-judge eval runner. Every team that ships
// LLM features eventually tries to write "tests for prompts" and
// fails — pytest doesn't know what to do with stochastic strings,
// LangSmith costs money, and rolling your own grader is yet another
// project. JUDGE.* gives the cache a single-server runner that:
//
//   - Stores test cases per prompt-id (input + expected + grader).
//   - Accepts actual outputs from the app's own LLM call.
//   - Scores them with one of five graders (exact, contains, regex,
//     numeric_within, llm — the last is provided by the app).
//   - Records pass/fail + per-prompt history with timestamps.
//   - Reports pass-rate per prompt-id over a sliding window for
//     regression alerts.
//
// Why this lives in the cache:
//
//   - The same prompts are tested across CI runs, dev runs, and
//     production canaries. Centralizing the case definitions + run
//     history avoids fork/merge of test-case YAML.
//   - Pass-rate tracking is exactly the kind of running counter the
//     cache is good at.
//   - Apps wire their own LLM caller; the cache is the grader, so
//     no API keys live here.
//
// The five graders cover ~90% of real prompt tests:
//   - exact:   actual == expected (after normalize)
//   - contains: actual contains expected as substring
//   - regex:   expected is a regex pattern
//   - numeric_within: parse both as floats, pass if within
//                     |actual - expected| <= tolerance
//   - llm:     short-circuit pass — the app already ran an LLM judge
//              and is just recording the verdict
//
// "llm" is the explicit hatch for the app to do its own grading
// (semantic similarity, custom rubric) and just submit pass/fail to
// the suite — JUDGE still aggregates the stats.
type JudgeSuite struct {
	mu     sync.RWMutex
	prompts map[string]*judgePrompt // prompt_id -> cases + runs

	totalRuns  atomic.Int64
	totalPass  atomic.Int64
	totalFail  atomic.Int64
}

type judgePrompt struct {
	cases      map[string]*judgeCase // case_id -> case
	runs       []judgeRun            // append-only; capped at 1000
	maxRuns    int
	createdAt  int64
}

type judgeCase struct {
	id       string
	input    string
	expected string
	grader   string  // exact|contains|regex|numeric_within|llm
	tol      float64 // for numeric_within
	regex    *regexp.Regexp
}

type judgeRun struct {
	caseID   string
	actual   string
	pass     bool
	score    float64 // 1.0 / 0.0 for binary graders, [0..1] for llm
	details  string
	tsUnix   int64
}

// NewJudgeSuite returns an empty suite.
func NewJudgeSuite() *JudgeSuite {
	return &JudgeSuite{prompts: map[string]*judgePrompt{}}
}

// CaseOpts configures a JUDGE.CASE.ADD call.
type CaseOpts struct {
	Grader    string  // default "exact"
	Tolerance float64 // for numeric_within
}

// AddCase registers a new test case under prompt_id. Returns an error
// for unknown grader / bad regex.
func (j *JudgeSuite) AddCase(promptID, caseID, input, expected string, opts CaseOpts) error {
	if promptID == "" || caseID == "" {
		return errors.New("prompt_id and case_id required")
	}
	grader := strings.ToLower(opts.Grader)
	if grader == "" {
		grader = "exact"
	}
	switch grader {
	case "exact", "contains", "numeric_within", "llm":
	case "regex":
	default:
		return fmt.Errorf("unknown grader: %s", grader)
	}
	c := &judgeCase{
		id:       caseID,
		input:    input,
		expected: expected,
		grader:   grader,
		tol:      opts.Tolerance,
	}
	if grader == "regex" {
		re, err := regexp.Compile(expected)
		if err != nil {
			return fmt.Errorf("bad regex: %w", err)
		}
		c.regex = re
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	p, ok := j.prompts[promptID]
	if !ok {
		p = &judgePrompt{
			cases:     map[string]*judgeCase{},
			maxRuns:   1000,
			createdAt: time.Now().Unix(),
		}
		j.prompts[promptID] = p
	}
	p.cases[caseID] = c
	return nil
}

// RemoveCase drops a case. Returns true if it existed.
func (j *JudgeSuite) RemoveCase(promptID, caseID string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	p, ok := j.prompts[promptID]
	if !ok {
		return false
	}
	_, was := p.cases[caseID]
	delete(p.cases, caseID)
	return was
}

// ScoreResult is the JUDGE.SCORE return.
type ScoreResult struct {
	CaseID   string  `json:"case_id"`
	Pass     bool    `json:"pass"`
	Score    float64 `json:"score"`
	Details  string  `json:"details,omitempty"`
	Grader   string  `json:"grader"`
}

// Score grades `actual` against the case's expected, records the
// run, and returns the verdict. For "llm" grader the caller passes
// the precomputed pass/score via ScoreOpts.
type ScoreOpts struct {
	LLMPass  bool    // for llm grader
	LLMScore float64 // for llm grader; 0..1
}

func (j *JudgeSuite) Score(promptID, caseID, actual string, opts ScoreOpts) (ScoreResult, bool) {
	j.mu.RLock()
	p, ok := j.prompts[promptID]
	if !ok {
		j.mu.RUnlock()
		return ScoreResult{}, false
	}
	c, ok := p.cases[caseID]
	j.mu.RUnlock()
	if !ok {
		return ScoreResult{}, false
	}

	r := ScoreResult{CaseID: caseID, Grader: c.grader}
	switch c.grader {
	case "exact":
		r.Pass = strings.TrimSpace(actual) == strings.TrimSpace(c.expected)
		r.Score = passScore(r.Pass)
	case "contains":
		r.Pass = strings.Contains(actual, c.expected)
		r.Score = passScore(r.Pass)
	case "regex":
		r.Pass = c.regex.MatchString(actual)
		r.Score = passScore(r.Pass)
	case "numeric_within":
		gotF, err := strconv.ParseFloat(strings.TrimSpace(actual), 64)
		if err != nil {
			r.Pass = false
			r.Score = 0
			r.Details = "actual not parseable as float"
			break
		}
		expF, _ := strconv.ParseFloat(strings.TrimSpace(c.expected), 64)
		diff := gotF - expF
		if diff < 0 {
			diff = -diff
		}
		r.Pass = diff <= c.tol
		r.Score = passScore(r.Pass)
		r.Details = fmt.Sprintf("|%.6g - %.6g| = %.6g (tol=%.6g)", gotF, expF, diff, c.tol)
	case "llm":
		r.Pass = opts.LLMPass
		r.Score = opts.LLMScore
		r.Details = "llm-grader (caller-supplied verdict)"
	}

	// Record run
	j.mu.Lock()
	p.runs = append(p.runs, judgeRun{
		caseID:  caseID,
		actual:  actual,
		pass:    r.Pass,
		score:   r.Score,
		details: r.Details,
		tsUnix:  time.Now().Unix(),
	})
	if len(p.runs) > p.maxRuns {
		p.runs = p.runs[len(p.runs)-p.maxRuns:]
	}
	j.mu.Unlock()

	j.totalRuns.Add(1)
	if r.Pass {
		j.totalPass.Add(1)
	} else {
		j.totalFail.Add(1)
	}
	return r, true
}

// CaseRow is one row of JUDGE.CASE.LIST.
type CaseRow struct {
	CaseID   string  `json:"case_id"`
	Input    string  `json:"input"`
	Expected string  `json:"expected"`
	Grader   string  `json:"grader"`
	Tol      float64 `json:"tol,omitempty"`
}

// Cases returns every case for a prompt, ordered by case_id.
func (j *JudgeSuite) Cases(promptID string) []CaseRow {
	j.mu.RLock()
	defer j.mu.RUnlock()
	p, ok := j.prompts[promptID]
	if !ok {
		return nil
	}
	out := make([]CaseRow, 0, len(p.cases))
	for _, c := range p.cases {
		out = append(out, CaseRow{
			CaseID:   c.id,
			Input:    c.input,
			Expected: c.expected,
			Grader:   c.grader,
			Tol:      c.tol,
		})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CaseID < out[k].CaseID })
	return out
}

// RunRow is one row of JUDGE.HISTORY.
type RunRow struct {
	CaseID   string  `json:"case_id"`
	Pass     bool    `json:"pass"`
	Score    float64 `json:"score"`
	Actual   string  `json:"actual,omitempty"`
	Details  string  `json:"details,omitempty"`
	TSUnix   int64   `json:"ts_unix"`
}

// History returns the most-recent N runs for a prompt, newest first.
// Pass 0 to get them all (subject to max_runs cap).
func (j *JudgeSuite) History(promptID string, limit int) []RunRow {
	j.mu.RLock()
	defer j.mu.RUnlock()
	p, ok := j.prompts[promptID]
	if !ok {
		return nil
	}
	all := make([]RunRow, 0, len(p.runs))
	for i := len(p.runs) - 1; i >= 0; i-- {
		r := p.runs[i]
		all = append(all, RunRow{
			CaseID: r.caseID, Pass: r.pass, Score: r.score,
			Actual: r.actual, Details: r.details, TSUnix: r.tsUnix,
		})
		if limit > 0 && len(all) >= limit {
			break
		}
	}
	return all
}

// PassRateResult is JUDGE.PASSRATE return.
type PassRateResult struct {
	PromptID  string  `json:"prompt_id"`
	WindowN   int     `json:"window_n"`
	Pass      int     `json:"pass"`
	Fail      int     `json:"fail"`
	PassRate  float64 `json:"pass_rate"`
	Cases     int     `json:"cases"`
}

// PassRate returns the windowed pass-rate for a prompt. If windowN
// is 0, uses the entire run history.
func (j *JudgeSuite) PassRate(promptID string, windowN int) (PassRateResult, bool) {
	j.mu.RLock()
	defer j.mu.RUnlock()
	p, ok := j.prompts[promptID]
	if !ok {
		return PassRateResult{}, false
	}
	runs := p.runs
	if windowN > 0 && windowN < len(runs) {
		runs = runs[len(runs)-windowN:]
	}
	pass, fail := 0, 0
	for _, r := range runs {
		if r.pass {
			pass++
		} else {
			fail++
		}
	}
	rate := 0.0
	if pass+fail > 0 {
		rate = float64(pass) / float64(pass+fail)
	}
	return PassRateResult{
		PromptID: promptID,
		WindowN:  len(runs),
		Pass:     pass,
		Fail:     fail,
		PassRate: rate,
		Cases:    len(p.cases),
	}, true
}

// Forget drops a prompt entirely (cases + runs).
func (j *JudgeSuite) Forget(promptID string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, ok := j.prompts[promptID]
	delete(j.prompts, promptID)
	return ok
}

// PromptIDs returns every registered prompt id, sorted.
func (j *JudgeSuite) PromptIDs() []string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	out := make([]string, 0, len(j.prompts))
	for id := range j.prompts {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// JudgeStats is the global counters snapshot.
type JudgeStats struct {
	TotalRuns  int64 `json:"total_runs"`
	TotalPass  int64 `json:"total_pass"`
	TotalFail  int64 `json:"total_fail"`
	Prompts    int   `json:"prompts"`
	Cases      int   `json:"cases"`
}

func (j *JudgeSuite) Stats() JudgeStats {
	j.mu.RLock()
	prompts := len(j.prompts)
	cases := 0
	for _, p := range j.prompts {
		cases += len(p.cases)
	}
	j.mu.RUnlock()
	return JudgeStats{
		TotalRuns: j.totalRuns.Load(),
		TotalPass: j.totalPass.Load(),
		TotalFail: j.totalFail.Load(),
		Prompts:   prompts,
		Cases:     cases,
	}
}

// ─── helpers ───────────────────────────────────────────────────

func passScore(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}
