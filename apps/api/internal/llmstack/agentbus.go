package llmstack

import (
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AgentBus is the agent-to-agent message bus with semantic routing.
// A traditional queue assumes the sender knows the recipient by name.
// In multi-agent systems the sender knows the *capability* needed
// ("write a SQL migration") and any agent with that capability should
// handle it — agents come and go and the senders don't track the
// roster.
//
// Each agent REGISTERs a capability description. SEND embeds the
// message and routes it to the agent whose capability has the highest
// cosine similarity. Recipients RECV their pending messages (FIFO
// per agent). If no agent has registered a sufficiently-matching
// capability, SEND returns routed_to="" and the caller can fall back.
//
// Commands:
//
//   AGENT.BUS.REGISTER agent-id "capability description"
//        Overwrites any prior registration for agent-id.
//   AGENT.BUS.UNREGISTER agent-id
//   AGENT.BUS.SEND run-id message [MIN_SIM f] [FROM agent-id]
//        → routed_to (agent-id), match (cosine), msg_id
//   AGENT.BUS.RECV agent-id [LIMIT n]
//        → list of pending messages (does not auto-ack)
//   AGENT.BUS.ACK agent-id msg-id
//        Removes a message from the agent's inbox.
//   AGENT.BUS.AGENTS              — every registered agent + capability
//   AGENT.BUS.PENDING agent-id    — count without consuming
//   AGENT.BUS.RESET ALL|agent-id  — drop registrations + inbox
//   AGENT.BUS.STATS
//
// Hot path: REGISTER stores one vector. SEND is a linear scan over the
// agent registry. RECV is a FIFO drain from one slice. The whole thing
// is per-instance; this is not a distributed bus.
type AgentBus struct {
	mu     sync.RWMutex
	agents map[string]*busAgent

	nextMsgID atomic.Int64
	totalSent atomic.Int64
	totalRecv atomic.Int64
	totalAcks atomic.Int64
	unrouted  atomic.Int64
}

type busAgent struct {
	mu         sync.Mutex
	id         string
	capability string
	vec        []float64
	registered time.Time
	inbox      []*busMsg
}

type busMsg struct {
	ID         int64
	From       string
	Text       string
	SentAt     time.Time
	MatchScore float64
}

// NewAgentBus returns an empty registry.
func NewAgentBus() *AgentBus {
	return &AgentBus{agents: map[string]*busAgent{}}
}

// Register declares (or overwrites) an agent's capability.
func (b *AgentBus) Register(agentID, capability string) error {
	if agentID == "" {
		return errors.New("agent_id required")
	}
	if capability == "" {
		return errors.New("capability required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.agents[agentID]
	if !ok {
		a = &busAgent{id: agentID}
		b.agents[agentID] = a
	}
	a.mu.Lock()
	a.capability = capability
	a.vec = embedRouting(capability)
	a.registered = time.Now()
	a.mu.Unlock()
	return nil
}

// embedRouting is a char-trigram hashed-BoW so capability matching
// catches morphological neighbours ("migration" ↔ "migrations") that
// pure-token embedFallback misses. 256-dim to keep collisions sparse.
func embedRouting(text string) []float64 {
	const dim = 256
	out := make([]float64, dim)
	text = strings.ToLower(text)
	// Tokens (with word boundaries) so token identity also counts
	tokens := tokenize(text)
	for _, t := range tokens {
		h := fnv1a32("tok|" + t)
		out[h%uint32(dim)] += 1
	}
	// Char trigrams over the whole string (padded so prefixes/suffixes show up)
	padded := "  " + text + "  "
	for i := 0; i+3 <= len(padded); i++ {
		tri := padded[i : i+3]
		h := fnv1a32("tri|" + tri)
		out[h%uint32(dim)] += 0.5
	}
	// L2-normalize
	var sum float64
	for _, v := range out {
		sum += v * v
	}
	if sum == 0 {
		return out
	}
	norm := math.Sqrt(sum)
	for i := range out {
		out[i] /= norm
	}
	return out
}

// Unregister removes the agent (and drops its inbox).
func (b *AgentBus) Unregister(agentID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.agents[agentID]; ok {
		delete(b.agents, agentID)
		return 1
	}
	return 0
}

// BusSendResult is SEND's return.
type BusSendResult struct {
	MsgID    int64   `json:"msg_id"`
	RoutedTo string  `json:"routed_to"`
	Match    float64 `json:"match"`
}

// Send routes a message to the best-matching agent. Default MIN_SIM
// is 0.10 so the hashed-BoW fallback isn't too restrictive; callers
// using a real sentence-transformer should pass a higher floor.
func (b *AgentBus) Send(message string, minSim float64, from string) (BusSendResult, error) {
	if message == "" {
		return BusSendResult{}, errors.New("message required")
	}
	if minSim < 0 {
		minSim = 0
	}
	// Default 0 (no floor): when an agent is registered, route to the
	// best match even if the cosine is tiny. The caller can pass a
	// higher floor to enforce a confidence threshold.
	b.totalSent.Add(1)
	qvec := embedRouting(message)
	b.mu.RLock()
	defer b.mu.RUnlock()
	var best *busAgent
	bestScore := -1.0
	for _, a := range b.agents {
		a.mu.Lock()
		s := dotProduct(a.vec, qvec)
		a.mu.Unlock()
		if s > bestScore {
			bestScore = s
			best = a
		}
	}
	if best == nil || bestScore < minSim {
		b.unrouted.Add(1)
		return BusSendResult{}, nil
	}
	id := b.nextMsgID.Add(1)
	best.mu.Lock()
	best.inbox = append(best.inbox, &busMsg{
		ID:         id,
		From:       from,
		Text:       message,
		SentAt:     time.Now(),
		MatchScore: bestScore,
	})
	best.mu.Unlock()
	return BusSendResult{MsgID: id, RoutedTo: best.id, Match: bestScore}, nil
}

// BusRecvRow is one row of RECV.
type BusRecvRow struct {
	MsgID      int64   `json:"msg_id"`
	From       string  `json:"from"`
	Text       string  `json:"text"`
	MatchScore float64 `json:"match"`
	AgeMS      int64   `json:"age_ms"`
}

// Recv returns the agent's pending messages (FIFO order) without
// acking. ACK is a separate call so the consumer can fail and have
// the message redelivered.
func (b *AgentBus) Recv(agentID string, limit int) ([]BusRecvRow, bool) {
	if agentID == "" {
		return nil, false
	}
	if limit <= 0 {
		limit = 32
	}
	b.totalRecv.Add(1)
	b.mu.RLock()
	a, ok := b.agents[agentID]
	b.mu.RUnlock()
	if !ok {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	out := make([]BusRecvRow, 0, len(a.inbox))
	for i, m := range a.inbox {
		if i >= limit {
			break
		}
		out = append(out, BusRecvRow{
			MsgID:      m.ID,
			From:       m.From,
			Text:       m.Text,
			MatchScore: m.MatchScore,
			AgeMS:      now.Sub(m.SentAt).Milliseconds(),
		})
	}
	return out, true
}

// Ack removes a delivered message from the agent's inbox. Idempotent:
// acking a missing msg returns 0.
func (b *AgentBus) Ack(agentID string, msgID int64) (int, error) {
	if agentID == "" {
		return 0, errors.New("agent_id required")
	}
	b.totalAcks.Add(1)
	b.mu.RLock()
	a, ok := b.agents[agentID]
	b.mu.RUnlock()
	if !ok {
		return 0, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, m := range a.inbox {
		if m.ID == msgID {
			a.inbox = append(a.inbox[:i], a.inbox[i+1:]...)
			return 1, nil
		}
	}
	return 0, nil
}

// BusAgentRow is one row of AGENTS.
type BusAgentRow struct {
	AgentID    string `json:"agent_id"`
	Capability string `json:"capability"`
	Pending    int    `json:"pending"`
	AgeMS      int64  `json:"age_ms"`
}

// Agents lists every registered agent.
func (b *AgentBus) Agents() []BusAgentRow {
	b.mu.RLock()
	defer b.mu.RUnlock()
	now := time.Now()
	out := make([]BusAgentRow, 0, len(b.agents))
	for _, a := range b.agents {
		a.mu.Lock()
		out = append(out, BusAgentRow{
			AgentID:    a.id,
			Capability: a.capability,
			Pending:    len(a.inbox),
			AgeMS:      now.Sub(a.registered).Milliseconds(),
		})
		a.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out
}

// Pending returns the inbox depth for one agent without consuming.
func (b *AgentBus) Pending(agentID string) (int, bool) {
	if agentID == "" {
		return 0, false
	}
	b.mu.RLock()
	a, ok := b.agents[agentID]
	b.mu.RUnlock()
	if !ok {
		return 0, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.inbox), true
}

// Reset drops registrations (and inboxes). agentID="ALL" wipes everything.
func (b *AgentBus) Reset(agentID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if agentID == "ALL" {
		n := len(b.agents)
		b.agents = map[string]*busAgent{}
		return n
	}
	if _, ok := b.agents[agentID]; ok {
		delete(b.agents, agentID)
		return 1
	}
	return 0
}

// BusStats is the global snapshot.
type BusStats struct {
	Agents      int   `json:"agents"`
	TotalSent   int64 `json:"total_sent"`
	TotalRecv   int64 `json:"total_recv"`
	TotalAcks   int64 `json:"total_acks"`
	Unrouted    int64 `json:"unrouted"`
	TotalPending int   `json:"total_pending"`
}

func (b *AgentBus) Stats() BusStats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	pending := 0
	for _, a := range b.agents {
		a.mu.Lock()
		pending += len(a.inbox)
		a.mu.Unlock()
	}
	return BusStats{
		Agents:       len(b.agents),
		TotalSent:    b.totalSent.Load(),
		TotalRecv:    b.totalRecv.Load(),
		TotalAcks:    b.totalAcks.Load(),
		Unrouted:     b.unrouted.Load(),
		TotalPending: pending,
	}
}
