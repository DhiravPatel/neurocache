package llmstack

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ProofRegistry produces verifiable computation receipts: commit to
// (model, inputs, params), then produce a small receipt that proves
// a given output came from that committed computation. ATTEST.* makes
// the audit *log* tamper-evident; PROOF.* makes the individual
// *inference* tamper-evident — caller commits before generating,
// the engine binds the output to the commitment.
//
// This is intentionally NOT zero-knowledge — we're not at zkML
// production-ready latency on commodity hardware in 2026 — but it
// gets you the property a regulated buyer typically needs: "I
// committed publicly to (model A, prompt P, params Q), and this
// output came from exactly that commitment; here is the cryptographic
// binding."
//
// The scheme:
//
//   1. COMMIT canonicalizes (model, prompt, params) and emits the
//      commitment hash H_commit = SHA256(canonical).
//
//   2. PRODUCE takes (commit-id, output) and emits a receipt:
//      receipt = (commit_id, H_commit, H_output, ts, optional sig).
//      The receipt is a small JSON blob — the buyer keeps it forever.
//
//   3. VERIFY takes (receipt, model, prompt, params, output) and
//      checks: re-canonicalising (model, prompt, params) reproduces
//      H_commit, AND SHA256(output) == H_output. Pure function, no
//      engine state needed.
//
// This binds output to the commitment with collision-resistance of
// SHA-256 — sufficient for procurement-grade audit. For full
// "model actually ran" zero-knowledge, the buyer pairs this with
// vendor TEE attestation.
//
// Commands:
//
//   PROOF.COMMIT commit-id model prompt params-json
//   PROOF.PRODUCE commit-id receipt-id output
//   PROOF.VERIFY receipt-id model prompt params-json output
//   PROOF.GET receipt-id
//   PROOF.LIST [LIMIT n]
//   PROOF.FORGET receipt-id|ALL  (commits live as long as receipts)
//   PROOF.STATS
type ProofRegistry struct {
	mu       sync.RWMutex
	commits  map[string]*proofCommit
	receipts map[string]*proofReceipt

	totalCommits  atomic.Int64
	totalProduces atomic.Int64
	totalVerifies atomic.Int64
}

type proofCommit struct {
	ID        string
	Hash      string
	Canon     string
	CreatedAt time.Time
}

type proofReceipt struct {
	ID         string
	CommitID   string
	CommitHash string
	OutputHash string
	IssuedAt   time.Time
}

// NewProofRegistry returns an empty registry.
func NewProofRegistry() *ProofRegistry {
	return &ProofRegistry{
		commits:  map[string]*proofCommit{},
		receipts: map[string]*proofReceipt{},
	}
}

// Commit registers a (model, prompt, params) tuple and returns the
// commitment hash. params is opaque JSON; we canonicalise so the
// same logical commitment always produces the same hash.
func (p *ProofRegistry) Commit(id, model, prompt, paramsJSON string) (string, error) {
	if id == "" {
		return "", errors.New("commit_id required")
	}
	if model == "" {
		return "", errors.New("model required")
	}
	if prompt == "" {
		return "", errors.New("prompt required")
	}
	if paramsJSON == "" {
		paramsJSON = "{}"
	}
	p.totalCommits.Add(1)
	// Canonical: combine model + prompt + canonical(params)
	canonParams, err := canonicalJSON(paramsJSON)
	if err != nil {
		return "", err
	}
	canon := "model:" + model + "\nprompt:" + prompt + "\nparams:" + string(canonParams)
	h := sha256.Sum256([]byte(canon))
	hash := hex.EncodeToString(h[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.commits[id]; exists {
		return "", errors.New("commit already exists: " + id)
	}
	p.commits[id] = &proofCommit{ID: id, Hash: hash, Canon: canon, CreatedAt: time.Now()}
	return hash, nil
}

// ProofProduceResult is PRODUCE's return.
type ProofProduceResult struct {
	ReceiptID  string `json:"receipt_id"`
	CommitHash string `json:"commit_hash"`
	OutputHash string `json:"output_hash"`
	IssuedUnix int64  `json:"issued_unix"`
}

// Produce takes (commit-id, output) and emits a receipt.
func (p *ProofRegistry) Produce(commitID, receiptID, output string) (ProofProduceResult, error) {
	if commitID == "" || receiptID == "" {
		return ProofProduceResult{}, errors.New("commit_id and receipt_id required")
	}
	if output == "" {
		return ProofProduceResult{}, errors.New("output required")
	}
	p.totalProduces.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	c, ok := p.commits[commitID]
	if !ok {
		return ProofProduceResult{}, errors.New("unknown commit: " + commitID)
	}
	if _, exists := p.receipts[receiptID]; exists {
		return ProofProduceResult{}, errors.New("receipt already exists: " + receiptID)
	}
	h := sha256.Sum256([]byte(output))
	outputHash := hex.EncodeToString(h[:])
	r := &proofReceipt{
		ID: receiptID, CommitID: commitID,
		CommitHash: c.Hash, OutputHash: outputHash, IssuedAt: time.Now(),
	}
	p.receipts[receiptID] = r
	return ProofProduceResult{
		ReceiptID: r.ID, CommitHash: r.CommitHash,
		OutputHash: r.OutputHash, IssuedUnix: r.IssuedAt.Unix(),
	}, nil
}

// ProofVerifyResult is VERIFY's return.
type ProofVerifyResult struct {
	Valid          bool   `json:"valid"`
	Reason         string `json:"reason"`
	ExpectedCommit string `json:"expected_commit_hash"`
	ExpectedOutput string `json:"expected_output_hash"`
}

// Verify is stateless-ish: it consults the stored receipt for the
// committed hash, then recomputes against the supplied (model,
// prompt, params, output) and reports the binding.
//
// True zero-engine VERIFY: the caller can re-implement this function
// in their own language using only the receipt's JSON (which carries
// commit_hash and output_hash) — the engine is not load-bearing for
// verification. The convenience version stored here also confirms
// the receipt was issued by us.
func (p *ProofRegistry) Verify(receiptID, model, prompt, paramsJSON, output string) (ProofVerifyResult, error) {
	if receiptID == "" {
		return ProofVerifyResult{}, errors.New("receipt_id required")
	}
	p.totalVerifies.Add(1)
	p.mu.RLock()
	r, ok := p.receipts[receiptID]
	p.mu.RUnlock()
	if !ok {
		return ProofVerifyResult{}, errors.New("unknown receipt: " + receiptID)
	}
	if paramsJSON == "" {
		paramsJSON = "{}"
	}
	canonParams, err := canonicalJSON(paramsJSON)
	if err != nil {
		return ProofVerifyResult{Reason: "params invalid: " + err.Error()}, nil
	}
	canon := "model:" + model + "\nprompt:" + prompt + "\nparams:" + string(canonParams)
	h := sha256.Sum256([]byte(canon))
	expectedCommit := hex.EncodeToString(h[:])
	h2 := sha256.Sum256([]byte(output))
	expectedOutput := hex.EncodeToString(h2[:])
	out := ProofVerifyResult{
		ExpectedCommit: expectedCommit, ExpectedOutput: expectedOutput,
	}
	switch {
	case expectedCommit != r.CommitHash:
		out.Reason = "commitment mismatch — (model, prompt, params) differs from what was committed"
	case expectedOutput != r.OutputHash:
		out.Reason = "output mismatch — supplied output is not what we issued the receipt for"
	default:
		out.Valid = true
		out.Reason = "ok"
	}
	return out, nil
}

// ProofReceiptView is GET's return.
type ProofReceiptView struct {
	ReceiptID  string `json:"receipt_id"`
	CommitID   string `json:"commit_id"`
	CommitHash string `json:"commit_hash"`
	OutputHash string `json:"output_hash"`
	IssuedUnix int64  `json:"issued_unix"`
}

// Get returns a receipt body.
func (p *ProofRegistry) Get(receiptID string) (ProofReceiptView, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.receipts[receiptID]
	if !ok {
		return ProofReceiptView{}, false
	}
	return ProofReceiptView{
		ReceiptID: r.ID, CommitID: r.CommitID,
		CommitHash: r.CommitHash, OutputHash: r.OutputHash,
		IssuedUnix: r.IssuedAt.Unix(),
	}, true
}

// List returns recent receipts.
func (p *ProofRegistry) List(limit int) []ProofReceiptView {
	if limit <= 0 {
		limit = 100
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]ProofReceiptView, 0, len(p.receipts))
	for _, r := range p.receipts {
		out = append(out, ProofReceiptView{
			ReceiptID: r.ID, CommitID: r.CommitID,
			CommitHash: r.CommitHash, OutputHash: r.OutputHash,
			IssuedUnix: r.IssuedAt.Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssuedUnix > out[j].IssuedUnix })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Forget drops a receipt (or all).
func (p *ProofRegistry) Forget(receiptID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if receiptID == "ALL" {
		n := len(p.receipts)
		p.receipts = map[string]*proofReceipt{}
		p.commits = map[string]*proofCommit{}
		return n
	}
	if _, ok := p.receipts[receiptID]; ok {
		delete(p.receipts, receiptID)
		return 1
	}
	return 0
}

// ProofStats is the global snapshot.
type ProofStats struct {
	Commits       int   `json:"commits"`
	Receipts      int   `json:"receipts"`
	TotalCommits  int64 `json:"total_commits"`
	TotalProduces int64 `json:"total_produces"`
	TotalVerifies int64 `json:"total_verifies"`
}

func (p *ProofRegistry) Stats() ProofStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return ProofStats{
		Commits: len(p.commits), Receipts: len(p.receipts),
		TotalCommits: p.totalCommits.Load(),
		TotalProduces: p.totalProduces.Load(),
		TotalVerifies: p.totalVerifies.Load(),
	}
}
