package llmstack

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Jury aggregates votes from multiple LLM judges into a single
// verdict — the production answer to "the model is confident, but
// it's wrong sometimes; how do I gate the risky ones?"
//
// Patterns that ride on JURY.*:
//   - **Self-consistency**: run the same model N times, vote on the
//     majority answer. Cheap; works surprisingly well.
//   - **LLM-as-judge**: a stronger model scores N weaker-model
//     candidates, picks the winner.
//   - **Multi-model jury**: ensemble across providers (GPT-4o,
//     Claude, Gemini) and vote on the consensus answer.
//
// All three boil down to the same operations: SUBMIT candidate
// answers, VOTE per judge (with optional confidence weighting),
// VERDICT aggregates with weighted majority + reports inter-judge
// agreement so the orchestrator can route low-agreement questions
// to a human.
//
// Commands:
//
//   JURY.SUBMIT question-id candidate-id text
//   JURY.VOTE   question-id judge-id candidate-id [CONFIDENCE f]
//   JURY.VERDICT question-id
//        → {winner, winner_score, agreement, candidates_n, judges_n}
//        agreement ∈ [0,1] — fraction of judges that picked the winner.
//   JURY.STATUS question-id
//   JURY.LIST
//   JURY.RESET question-id|ALL
//   JURY.STATS
//
// Hot path: VOTE is one map lookup + accumulator update. VERDICT
// is O(candidates + votes) — typically a handful each. Lockless on
// the per-question scope (sync.RWMutex), serial only across
// question-id additions.
type Jury struct {
	mu        sync.RWMutex
	questions map[string]*juryQuestion

	totalSubmits  atomic.Int64
	totalVotes    atomic.Int64
	totalVerdicts atomic.Int64
}

type juryQuestion struct {
	mu         sync.RWMutex
	candidates map[string]string  // candidate-id → text
	candOrder  []string           // for deterministic iteration
	votes      map[string]map[string]float64 // judge → candidate → confidence
	createdAt  int64
}

// NewJury returns an empty jury store.
func NewJury() *Jury {
	return &Jury{questions: map[string]*juryQuestion{}}
}

// Submit registers a candidate answer for a question.
func (j *Jury) Submit(questionID, candidateID, text string) error {
	if questionID == "" {
		return errors.New("question_id required")
	}
	if candidateID == "" {
		return errors.New("candidate_id required")
	}
	j.totalSubmits.Add(1)
	q := j.questionOrCreate(questionID)
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.candidates[candidateID]; !exists {
		q.candOrder = append(q.candOrder, candidateID)
	}
	q.candidates[candidateID] = text
	return nil
}

// Vote casts (or replaces) a judge's vote for a candidate.
// confidence = 1.0 if omitted (unweighted majority).
func (j *Jury) Vote(questionID, judgeID, candidateID string, confidence float64) error {
	if questionID == "" {
		return errors.New("question_id required")
	}
	if judgeID == "" {
		return errors.New("judge_id required")
	}
	if candidateID == "" {
		return errors.New("candidate_id required")
	}
	if confidence < 0 || confidence > 1 {
		return errors.New("confidence must be in [0,1]")
	}
	if confidence == 0 {
		confidence = 1.0
	}
	j.totalVotes.Add(1)
	j.mu.RLock()
	q, ok := j.questions[questionID]
	j.mu.RUnlock()
	if !ok {
		return errors.New("unknown question_id (call JURY.SUBMIT first): " + questionID)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.candidates[candidateID]; !exists {
		return errors.New("unknown candidate_id: " + candidateID)
	}
	if q.votes[judgeID] == nil {
		q.votes[judgeID] = map[string]float64{}
	}
	// Each judge votes for ONE candidate; replace any prior vote
	q.votes[judgeID] = map[string]float64{candidateID: confidence}
	return nil
}

// JuryVerdict is VERDICT's return.
type JuryVerdict struct {
	QuestionID    string  `json:"question_id"`
	Winner        string  `json:"winner"`        // candidate id; "" if no votes
	WinnerText    string  `json:"winner_text,omitempty"`
	WinnerScore   float64 `json:"winner_score"`
	Agreement     float64 `json:"agreement"`     // fraction of judges that picked the winner
	CandidatesN   int     `json:"candidates_n"`
	JudgesN       int     `json:"judges_n"`
	TieBroken     bool    `json:"tie_broken"`    // true if winner came from tiebreaker (alphabetical)
}

// Verdict returns the aggregated jury decision.
func (j *Jury) Verdict(questionID string) (JuryVerdict, error) {
	j.totalVerdicts.Add(1)
	j.mu.RLock()
	q, ok := j.questions[questionID]
	j.mu.RUnlock()
	if !ok {
		return JuryVerdict{}, errors.New("unknown question_id: " + questionID)
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	out := JuryVerdict{
		QuestionID:  questionID,
		CandidatesN: len(q.candidates),
		JudgesN:     len(q.votes),
	}
	if len(q.votes) == 0 {
		return out, nil
	}
	// Sum confidence per candidate
	scores := map[string]float64{}
	picks := map[string]int{}
	for _, choice := range q.votes {
		for cand, conf := range choice {
			scores[cand] += conf
			picks[cand]++
		}
	}
	// Find winner — highest score, tiebreak by candidate-id alphabetic
	var bestCand string
	var bestScore float64
	tied := []string{}
	for cand, sc := range scores {
		if sc > bestScore {
			bestCand = cand
			bestScore = sc
			tied = []string{cand}
		} else if sc == bestScore {
			tied = append(tied, cand)
		}
	}
	if len(tied) > 1 {
		sort.Strings(tied)
		// A tie was broken — set the flag regardless of which candidate
		// happened to land in bestCand first during the (unordered)
		// map iteration above.
		out.TieBroken = true
		bestCand = tied[0]
		bestScore = scores[bestCand]
	}
	out.Winner = bestCand
	out.WinnerScore = bestScore
	out.WinnerText = q.candidates[bestCand]
	if len(q.votes) > 0 {
		out.Agreement = float64(picks[bestCand]) / float64(len(q.votes))
	}
	return out, nil
}

// JuryStatusRow is one candidate row in STATUS.
type JuryStatusRow struct {
	CandidateID string  `json:"candidate_id"`
	Score       float64 `json:"score"`
	Picks       int     `json:"picks"`
}

// JuryStatus is the per-question snapshot.
type JuryStatus struct {
	QuestionID  string          `json:"question_id"`
	CandidatesN int             `json:"candidates_n"`
	JudgesN     int             `json:"judges_n"`
	CreatedAt   int64           `json:"created_at"`
	Rows        []JuryStatusRow `json:"candidates"`
}

// Status returns the per-question snapshot with per-candidate scores.
func (j *Jury) Status(questionID string) (JuryStatus, bool) {
	j.mu.RLock()
	q, ok := j.questions[questionID]
	j.mu.RUnlock()
	if !ok {
		return JuryStatus{}, false
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	scores := map[string]float64{}
	picks := map[string]int{}
	for _, choice := range q.votes {
		for cand, conf := range choice {
			scores[cand] += conf
			picks[cand]++
		}
	}
	out := JuryStatus{
		QuestionID:  questionID,
		CandidatesN: len(q.candidates),
		JudgesN:     len(q.votes),
		CreatedAt:   q.createdAt / int64(time.Second),
	}
	for _, cand := range q.candOrder {
		out.Rows = append(out.Rows, JuryStatusRow{
			CandidateID: cand,
			Score:       scores[cand],
			Picks:       picks[cand],
		})
	}
	sort.Slice(out.Rows, func(i, k int) bool { return out.Rows[i].Score > out.Rows[k].Score })
	return out, true
}

// List returns every question id, sorted.
func (j *Jury) List() []string {
	j.mu.RLock()
	out := make([]string, 0, len(j.questions))
	for k := range j.questions {
		out = append(out, k)
	}
	j.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Reset drops a question. questionID="ALL" wipes all.
func (j *Jury) Reset(questionID string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	if questionID == "ALL" {
		n := len(j.questions)
		j.questions = map[string]*juryQuestion{}
		return n
	}
	if _, ok := j.questions[questionID]; ok {
		delete(j.questions, questionID)
		return 1
	}
	return 0
}

// JuryStats is the global snapshot.
type JuryStats struct {
	Questions     int   `json:"questions"`
	TotalSubmits  int64 `json:"total_submits"`
	TotalVotes    int64 `json:"total_votes"`
	TotalVerdicts int64 `json:"total_verdicts"`
}

func (j *Jury) Stats() JuryStats {
	j.mu.RLock()
	n := len(j.questions)
	j.mu.RUnlock()
	return JuryStats{
		Questions:     n,
		TotalSubmits:  j.totalSubmits.Load(),
		TotalVotes:    j.totalVotes.Load(),
		TotalVerdicts: j.totalVerdicts.Load(),
	}
}

// ─── internals ──────────────────────────────────────────────────

func (j *Jury) questionOrCreate(id string) *juryQuestion {
	j.mu.RLock()
	q, ok := j.questions[id]
	j.mu.RUnlock()
	if ok {
		return q
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if q, ok := j.questions[id]; ok {
		return q
	}
	q = &juryQuestion{
		candidates: map[string]string{},
		votes:      map[string]map[string]float64{},
		createdAt:  time.Now().UnixNano(),
	}
	j.questions[id] = q
	return q
}
