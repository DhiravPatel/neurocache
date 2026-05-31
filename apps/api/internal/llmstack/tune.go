package llmstack

import (
	"errors"
	"math"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// TuneRegistry is the self-tuning bandit. NeuroCache has dozens of
// magic numbers (SEMANTIC_THRESHOLD, eviction weights, GUARD caps,
// every DELTA/THRESHOLD in canary / dedup / novelty). Every operator
// hand-tunes them once at deploy time and never touches them again,
// so they rot — the optimal value shifts as traffic shifts and
// nobody notices.
//
// TUNE treats NeuroCache's own configuration as an optimization
// problem against a declared objective:
//
//   1. KNOB declares a continuous range for a configuration value.
//      The knob name is opaque — the engine doesn't change settings
//      itself; APPLY returns the current best so the operator (or
//      AUTO rule) can wire it.
//
//   2. OBJECTIVE declares what we're optimising. Expressions of
//      observed metrics: "hit_rate - 0.3*stale_serve_rate". The
//      caller posts metric observations via OBSERVE; the engine
//      computes the objective per posting.
//
//   3. The optimiser is a discretised Thompson-sampling bandit over
//      the knob's range (10 buckets by default). Each bucket is a
//      Beta(α, β) over normalised reward. SUGGEST returns the next
//      candidate to try; OBSERVE updates the bucket with the
//      measured outcome.
//
//   4. APPLY returns the bucket center with the highest posterior
//      mean — the "winner so far." HISTORY shows every attempt.
//
// This is genuinely Bayesian, but kept dumb-on-purpose: bandits give
// you 95% of Gaussian-process-bayesopt's value for 1% of the
// complexity, and importantly stay interpretable to operators.
//
// Commands:
//
//   TUNE.KNOB tune-id knob RANGE low high [BUCKETS n]
//        BUCKETS defaults to 10. The knob is discretised into n
//        evenly-spaced bucket centers.
//   TUNE.OBJECTIVE tune-id MAXIMIZE|MINIMIZE "<expression>"
//        Expression is "metric op metric op ..." over post-time
//        metrics. Supported: +, -, *, /, numeric literals, metric
//        names. Examples:
//          "hit_rate"
//          "hit_rate - 0.3*stale_serve_rate"
//          "quality - 10*cost_usd"
//        We don't have a full parser; expression is parsed by a tiny
//        recursive-descent evaluator (see internals).
//   TUNE.SUGGEST tune-id
//        → bucket-center to try. Thompson sampling: draws one sample
//        per bucket's Beta posterior and picks the argmax.
//   TUNE.OBSERVE tune-id value METRIC k v [METRIC k v ...]
//        Update with the observed bucket value + metrics dict.
//        Reward is the objective expression evaluated against the
//        metrics. We normalize reward to [0,1] using a rolling
//        min/max so Beta posteriors stay well-defined.
//   TUNE.APPLY tune-id
//        → best bucket-center, projected_lift, trials, confidence
//   TUNE.STATUS tune-id
//        → current best + every bucket's posterior summary
//   TUNE.HISTORY tune-id [LIMIT n]
//   TUNE.FORGET tune-id|ALL
//   TUNE.LIST
//   TUNE.STATS
//
// Hot path: OBSERVE is one expression eval + Beta posterior update.
// SUGGEST is O(buckets) random samples from each Beta.
type TuneRegistry struct {
	mu     sync.RWMutex
	tuners map[string]*tuneJob
	rng    *rand.Rand

	totalSuggests atomic.Int64
	totalObserves atomic.Int64
}

type tuneJob struct {
	mu          sync.Mutex
	knob        string
	low, high   float64
	buckets     []tuneBucket
	objective   string // expression
	direction   string // "max" or "min"
	rewardLo    float64
	rewardHi    float64
	rewardInit  bool
	history     []tuneTry
	createdAt   time.Time
}

type tuneBucket struct {
	Center float64
	Alpha  float64
	Beta   float64
	N      int64
	Sum    float64 // sum of raw rewards (for mean reporting)
}

type tuneTry struct {
	BucketCenter float64
	Value        float64
	Reward       float64
	Metrics      map[string]float64
	At           time.Time
}

// NewTuneRegistry returns an empty registry.
func NewTuneRegistry() *TuneRegistry {
	return &TuneRegistry{
		tuners: map[string]*tuneJob{},
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Knob registers (or replaces) a tuner's knob range. Re-knob discards
// prior history (range change invalidates posteriors).
func (t *TuneRegistry) Knob(tuneID, knob string, low, high float64, buckets int) error {
	if tuneID == "" {
		return errors.New("tune_id required")
	}
	if knob == "" {
		return errors.New("knob required")
	}
	if low >= high {
		return errors.New("low must be < high")
	}
	if buckets <= 0 {
		buckets = 10
	}
	if buckets > 256 {
		return errors.New("buckets must be <= 256")
	}
	bs := make([]tuneBucket, buckets)
	for i := range bs {
		// Bucket centers evenly spaced across [low, high]
		bs[i].Center = low + (high-low)*(float64(i)+0.5)/float64(buckets)
		bs[i].Alpha = 1
		bs[i].Beta = 1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tuners[tuneID] = &tuneJob{
		knob: knob, low: low, high: high, buckets: bs,
		direction: "max", createdAt: time.Now(),
	}
	return nil
}

// Objective registers the objective expression. direction = "max" or "min".
func (t *TuneRegistry) Objective(tuneID, direction, expr string) error {
	if tuneID == "" {
		return errors.New("tune_id required")
	}
	direction = string(direction)
	if direction != "max" && direction != "min" {
		return errors.New("direction must be max or min")
	}
	if expr == "" {
		return errors.New("expression required")
	}
	// Validate by parsing once
	if _, err := evalExpr(expr, map[string]float64{}); err != nil {
		// Empty metrics → undefined var error is ok, parse error is not
		if _, perr := parseExpr(expr); perr != nil {
			return errors.New("expression parse error: " + perr.Error())
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	j, ok := t.tuners[tuneID]
	if !ok {
		return errors.New("unknown tune_id: " + tuneID)
	}
	j.mu.Lock()
	j.objective = expr
	j.direction = direction
	j.mu.Unlock()
	return nil
}

// Suggest returns a bucket center to try next using Thompson sampling.
// Beta posteriors → one sample per bucket → argmax pick.
func (t *TuneRegistry) Suggest(tuneID string) (float64, bool) {
	t.totalSuggests.Add(1)
	t.mu.RLock()
	j, ok := t.tuners[tuneID]
	rng := t.rng
	t.mu.RUnlock()
	if !ok {
		return 0, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.buckets) == 0 {
		return 0, false
	}
	bestIdx := 0
	bestSample := -1.0
	for i, b := range j.buckets {
		// Sample from Beta(α, β). Use a Gamma trick: sample = X / (X+Y),
		// where X ~ Gamma(α, 1), Y ~ Gamma(β, 1). For typical α/β ≤ 50,
		// the Marsaglia-Tsang method is overkill; just use NormalFloat64
		// approximation when both shape params are > 1, otherwise
		// exponential trick.
		sample := betaSample(b.Alpha, b.Beta, rng)
		// If we're minimizing, invert.
		score := sample
		if j.direction == "min" {
			score = 1 - sample
		}
		if score > bestSample {
			bestSample = score
			bestIdx = i
		}
	}
	return j.buckets[bestIdx].Center, true
}

// TuneObserveResult is OBSERVE's return.
type TuneObserveResult struct {
	BucketCenter float64 `json:"bucket_center"`
	RawReward    float64 `json:"raw_reward"`
	NormReward   float64 `json:"norm_reward"`
}

// Observe records the outcome of trying `value` with the supplied
// metrics dict. The reward = objective(metrics); we normalize across
// the rolling min/max seen so far so Beta posteriors stay well-defined.
func (t *TuneRegistry) Observe(tuneID string, value float64, metrics map[string]float64) (TuneObserveResult, error) {
	if tuneID == "" {
		return TuneObserveResult{}, errors.New("tune_id required")
	}
	t.totalObserves.Add(1)
	t.mu.RLock()
	j, ok := t.tuners[tuneID]
	t.mu.RUnlock()
	if !ok {
		return TuneObserveResult{}, errors.New("unknown tune_id: " + tuneID)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.objective == "" {
		return TuneObserveResult{}, errors.New("no objective set; call TUNE.OBJECTIVE first")
	}
	raw, err := evalExpr(j.objective, metrics)
	if err != nil {
		return TuneObserveResult{}, err
	}
	if !j.rewardInit {
		j.rewardLo = raw
		j.rewardHi = raw
		j.rewardInit = true
	} else {
		if raw < j.rewardLo {
			j.rewardLo = raw
		}
		if raw > j.rewardHi {
			j.rewardHi = raw
		}
	}
	// Normalize to [0,1]; pin to 0.5 if all observations equal so far
	norm := 0.5
	if j.rewardHi > j.rewardLo {
		norm = (raw - j.rewardLo) / (j.rewardHi - j.rewardLo)
	}
	if j.direction == "min" {
		norm = 1 - norm
	}
	// Find the bucket the value falls into
	idx := bucketFor(j, value)
	b := &j.buckets[idx]
	// Beta posterior update: treat norm reward as a "win" probability —
	// add norm to alpha, 1-norm to beta.
	b.Alpha += norm
	b.Beta += 1 - norm
	b.N++
	b.Sum += raw
	// Append to history (cap at 5000)
	cp := make(map[string]float64, len(metrics))
	for k, v := range metrics {
		cp[k] = v
	}
	j.history = append(j.history, tuneTry{
		BucketCenter: b.Center, Value: value,
		Reward: raw, Metrics: cp, At: time.Now(),
	})
	if len(j.history) > 5000 {
		j.history = j.history[len(j.history)-5000:]
	}
	return TuneObserveResult{
		BucketCenter: b.Center, RawReward: raw, NormReward: norm,
	}, nil
}

// TuneApplyResult is APPLY's return.
type TuneApplyResult struct {
	BestValue      float64 `json:"best_value"`
	ProjectedLift  float64 `json:"projected_lift_vs_random"` // best mean - average mean
	Trials         int64   `json:"trials"`
	Confidence     string  `json:"confidence"`
}

// Apply returns the winning bucket center. "Confidence" is bucketised
// from trial count: LOW (<20), MEDIUM (<100), HIGH (≥100).
func (t *TuneRegistry) Apply(tuneID string) (TuneApplyResult, bool) {
	t.mu.RLock()
	j, ok := t.tuners[tuneID]
	t.mu.RUnlock()
	if !ok {
		return TuneApplyResult{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.buckets) == 0 {
		return TuneApplyResult{}, true
	}
	var trials int64
	var sumMean float64
	bestIdx := 0
	bestMean := -1.0
	for i, b := range j.buckets {
		trials += b.N
		mean := b.Alpha / (b.Alpha + b.Beta)
		sumMean += mean
		if mean > bestMean {
			bestMean = mean
			bestIdx = i
		}
	}
	avgMean := sumMean / float64(len(j.buckets))
	out := TuneApplyResult{
		BestValue:     j.buckets[bestIdx].Center,
		ProjectedLift: bestMean - avgMean,
		Trials:        trials,
	}
	switch {
	case trials >= 100:
		out.Confidence = "HIGH"
	case trials >= 20:
		out.Confidence = "MEDIUM"
	default:
		out.Confidence = "LOW"
	}
	return out, true
}

// TuneBucketRow is one bucket in STATUS.
type TuneBucketRow struct {
	Center     float64 `json:"center"`
	Alpha      float64 `json:"alpha"`
	Beta       float64 `json:"beta"`
	N          int64   `json:"n"`
	Mean       float64 `json:"mean"`
	MeanReward float64 `json:"mean_reward"`
}

// TuneStatus is STATUS's return.
type TuneStatus struct {
	TuneID    string          `json:"tune_id"`
	Knob      string          `json:"knob"`
	Direction string          `json:"direction"`
	Objective string          `json:"objective"`
	Buckets   []TuneBucketRow `json:"buckets"`
}

// Status returns the full posterior snapshot.
func (t *TuneRegistry) Status(tuneID string) (TuneStatus, bool) {
	t.mu.RLock()
	j, ok := t.tuners[tuneID]
	t.mu.RUnlock()
	if !ok {
		return TuneStatus{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	rows := make([]TuneBucketRow, len(j.buckets))
	for i, b := range j.buckets {
		mean := 0.0
		if b.N > 0 {
			mean = b.Sum / float64(b.N)
		}
		rows[i] = TuneBucketRow{
			Center: b.Center, Alpha: b.Alpha, Beta: b.Beta, N: b.N,
			Mean: b.Alpha / (b.Alpha + b.Beta),
			MeanReward: mean,
		}
	}
	return TuneStatus{
		TuneID: tuneID, Knob: j.knob, Direction: j.direction,
		Objective: j.objective, Buckets: rows,
	}, true
}

// TuneHistoryRow is one row of HISTORY.
type TuneHistoryRow struct {
	BucketCenter float64 `json:"bucket_center"`
	Value        float64 `json:"value"`
	Reward       float64 `json:"reward"`
	AtUnix       int64   `json:"at_unix"`
}

// History returns the most-recent N attempts.
func (t *TuneRegistry) History(tuneID string, limit int) ([]TuneHistoryRow, bool) {
	if limit <= 0 {
		limit = 100
	}
	t.mu.RLock()
	j, ok := t.tuners[tuneID]
	t.mu.RUnlock()
	if !ok {
		return nil, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	n := len(j.history)
	start := n - limit
	if start < 0 {
		start = 0
	}
	out := make([]TuneHistoryRow, 0, n-start)
	for i := n - 1; i >= start; i-- {
		h := j.history[i]
		out = append(out, TuneHistoryRow{
			BucketCenter: h.BucketCenter, Value: h.Value,
			Reward: h.Reward, AtUnix: h.At.Unix(),
		})
	}
	return out, true
}

// Forget drops a tuner (or all).
func (t *TuneRegistry) Forget(tuneID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tuneID == "ALL" {
		n := len(t.tuners)
		t.tuners = map[string]*tuneJob{}
		return n
	}
	if _, ok := t.tuners[tuneID]; ok {
		delete(t.tuners, tuneID)
		return 1
	}
	return 0
}

// List returns every tuner id.
func (t *TuneRegistry) List() []string {
	t.mu.RLock()
	out := make([]string, 0, len(t.tuners))
	for k := range t.tuners {
		out = append(out, k)
	}
	t.mu.RUnlock()
	sort.Strings(out)
	return out
}

// TuneStats is the global snapshot.
type TuneStats struct {
	Tuners        int   `json:"tuners"`
	TotalSuggests int64 `json:"total_suggests"`
	TotalObserves int64 `json:"total_observes"`
}

func (t *TuneRegistry) Stats() TuneStats {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return TuneStats{
		Tuners:        len(t.tuners),
		TotalSuggests: t.totalSuggests.Load(),
		TotalObserves: t.totalObserves.Load(),
	}
}

// ─── internals: Beta sampler, bucket finder, expression evaluator ─

// betaSample draws from Beta(α, β) via the Gamma ratio (Marsaglia &
// Tsang). For our typical α/β values this is fast enough; if α,β
// drift huge we'd cache a Gaussian approximation. Kept simple.
func betaSample(alpha, beta float64, rng *rand.Rand) float64 {
	x := gammaSample(alpha, rng)
	y := gammaSample(beta, rng)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

func gammaSample(shape float64, rng *rand.Rand) float64 {
	if shape <= 0 {
		return 0
	}
	if shape < 1 {
		// Boost trick: Gamma(shape) = Gamma(shape+1) * U^(1/shape)
		u := rng.Float64()
		return gammaSample(shape+1, rng) * math.Pow(u, 1/shape)
	}
	// Marsaglia-Tsang for shape >= 1
	d := shape - 1.0/3.0
	c := 1.0 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x {
			return d * v
		}
		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

func bucketFor(j *tuneJob, value float64) int {
	if value <= j.low {
		return 0
	}
	if value >= j.high {
		return len(j.buckets) - 1
	}
	rng := j.high - j.low
	idx := int(float64(len(j.buckets)) * (value - j.low) / rng)
	if idx >= len(j.buckets) {
		idx = len(j.buckets) - 1
	}
	if idx < 0 {
		idx = 0
	}
	return idx
}

// ─── expression evaluator (tiny recursive-descent) ──────────────

type exprNode struct {
	op    string // "lit", "var", or an operator
	val   float64
	name  string
	left  *exprNode
	right *exprNode
}

func parseExpr(s string) (*exprNode, error) {
	p := &exprParser{src: s, pos: 0}
	node, err := p.expr()
	if err != nil {
		return nil, err
	}
	p.skipSpace()
	if p.pos != len(p.src) {
		return nil, errors.New("trailing input at pos " + itoaInline(p.pos))
	}
	return node, nil
}

func evalExpr(s string, vars map[string]float64) (float64, error) {
	n, err := parseExpr(s)
	if err != nil {
		return 0, err
	}
	return evalNode(n, vars)
}

func evalNode(n *exprNode, vars map[string]float64) (float64, error) {
	switch n.op {
	case "lit":
		return n.val, nil
	case "var":
		v, ok := vars[n.name]
		if !ok {
			return 0, errors.New("undefined metric: " + n.name)
		}
		return v, nil
	case "+", "-", "*", "/":
		l, err := evalNode(n.left, vars)
		if err != nil {
			return 0, err
		}
		r, err := evalNode(n.right, vars)
		if err != nil {
			return 0, err
		}
		switch n.op {
		case "+":
			return l + r, nil
		case "-":
			return l - r, nil
		case "*":
			return l * r, nil
		case "/":
			if r == 0 {
				return 0, errors.New("division by zero")
			}
			return l / r, nil
		}
	}
	return 0, errors.New("unknown node op: " + n.op)
}

type exprParser struct {
	src string
	pos int
}

func (p *exprParser) expr() (*exprNode, error) {
	left, err := p.term()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return left, nil
		}
		c := p.src[p.pos]
		if c != '+' && c != '-' {
			return left, nil
		}
		p.pos++
		right, err := p.term()
		if err != nil {
			return nil, err
		}
		left = &exprNode{op: string(c), left: left, right: right}
	}
}

func (p *exprParser) term() (*exprNode, error) {
	left, err := p.factor()
	if err != nil {
		return nil, err
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.src) {
			return left, nil
		}
		c := p.src[p.pos]
		if c != '*' && c != '/' {
			return left, nil
		}
		p.pos++
		right, err := p.factor()
		if err != nil {
			return nil, err
		}
		left = &exprNode{op: string(c), left: left, right: right}
	}
}

func (p *exprParser) factor() (*exprNode, error) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return nil, errors.New("unexpected end")
	}
	c := p.src[p.pos]
	if c == '(' {
		p.pos++
		node, err := p.expr()
		if err != nil {
			return nil, err
		}
		p.skipSpace()
		if p.pos >= len(p.src) || p.src[p.pos] != ')' {
			return nil, errors.New("expected )")
		}
		p.pos++
		return node, nil
	}
	if c == '-' {
		p.pos++
		// Unary minus: 0 - factor
		right, err := p.factor()
		if err != nil {
			return nil, err
		}
		return &exprNode{op: "-", left: &exprNode{op: "lit", val: 0}, right: right}, nil
	}
	if isDigit(c) || c == '.' {
		start := p.pos
		for p.pos < len(p.src) && (isDigit(p.src[p.pos]) || p.src[p.pos] == '.') {
			p.pos++
		}
		v, err := parseFloatInline(p.src[start:p.pos])
		if err != nil {
			return nil, err
		}
		return &exprNode{op: "lit", val: v}, nil
	}
	if isAlpha(c) {
		start := p.pos
		for p.pos < len(p.src) && (isAlpha(p.src[p.pos]) || isDigit(p.src[p.pos]) || p.src[p.pos] == '_') {
			p.pos++
		}
		return &exprNode{op: "var", name: p.src[start:p.pos]}, nil
	}
	return nil, errors.New("unexpected character: " + string(c))
}

func (p *exprParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' }

// parseFloatInline avoids dragging strconv into every call site. For
// our knob expressions it sees decimals and integers; nothing fancy.
func parseFloatInline(s string) (float64, error) {
	var f float64
	var frac float64 = 1
	seenDot := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if seenDot {
				return 0, errors.New("multiple dots in number")
			}
			seenDot = true
			continue
		}
		if !isDigit(c) {
			return 0, errors.New("non-digit in number")
		}
		d := float64(c - '0')
		if seenDot {
			frac *= 10
			f += d / frac
		} else {
			f = f*10 + d
		}
	}
	return f, nil
}

// itoaInline because importing strconv into this file would suck the
// fun out of the recursive-descent parser comment block above.
func itoaInline(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
