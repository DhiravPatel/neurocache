package llmstack

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// Turn is one role/content message in a conversation.
type Turn struct {
	Role      string    `json:"role"`     // "system" | "user" | "assistant" | "tool"
	Content   string    `json:"content"`
	Tokens    int       `json:"tokens"`
	CreatedAt time.Time `json:"created_at"`
}

// Conversation is the per-session ordered turn log. We bundle a running
// `Summary` (filled by CONV.SUMMARIZE) ahead of the windowed turns so
// "compress old context, keep recent" is a one-call operation.
type Conversation struct {
	mu      sync.RWMutex
	turns   []Turn
	summary string
	updated time.Time
}

// Conversations is the keyspace of named sessions.
type Conversations struct {
	mu sync.RWMutex
	by map[string]*Conversation
}

// NewConversations returns an empty session store.
func NewConversations() *Conversations {
	return &Conversations{by: map[string]*Conversation{}}
}

// Append records a turn. role must be non-empty; content may be empty
// (some callers use zero-content turns as deliberate markers). Returns
// the new total turn count.
func (c *Conversations) Append(key, role, content string) (int, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		return 0, errors.New("ERR role is required")
	}
	c.mu.Lock()
	conv, ok := c.by[key]
	if !ok {
		conv = &Conversation{}
		c.by[key] = conv
	}
	c.mu.Unlock()
	conv.mu.Lock()
	conv.turns = append(conv.turns, Turn{
		Role:      role,
		Content:   content,
		Tokens:    approxTokens(content),
		CreatedAt: time.Now(),
	})
	conv.updated = time.Now()
	n := len(conv.turns)
	conv.mu.Unlock()
	return n, nil
}

// Window returns the turns whose cumulative token count fits in
// `maxTokens`, walking from the most recent backwards. The summary (if
// any) is prepended as a synthetic system turn so callers can splice
// the result straight into a model's `messages` array.
//
// When maxTokens <= 0, every turn is returned.
func (c *Conversations) Window(key string, maxTokens int) []Turn {
	c.mu.RLock()
	conv, ok := c.by[key]
	c.mu.RUnlock()
	if !ok {
		return nil
	}
	conv.mu.RLock()
	defer conv.mu.RUnlock()
	out := []Turn{}
	if conv.summary != "" {
		out = append(out, Turn{
			Role:      "system",
			Content:   conv.summary,
			Tokens:    approxTokens(conv.summary),
			CreatedAt: conv.updated,
		})
	}
	if maxTokens <= 0 {
		out = append(out, conv.turns...)
		return out
	}
	used := 0
	if len(out) > 0 {
		used = out[0].Tokens
	}
	// walk recent → old, then reverse the slice we accumulate so the
	// caller sees chronological order
	picked := []Turn{}
	for i := len(conv.turns) - 1; i >= 0; i-- {
		t := conv.turns[i]
		if used+t.Tokens > maxTokens && len(picked) > 0 {
			break
		}
		used += t.Tokens
		picked = append(picked, t)
	}
	// reverse picked
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}
	out = append(out, picked...)
	return out
}

// Summarize replaces the older portion of the log with a synthetic
// summary message. The caller passes the summary text (typically
// produced by an LLM call); we drop the original turns it represents
// and keep the most recent `keepTokens` worth verbatim.
//
// Returns (turnsDropped, totalTokensRemaining, nil).
func (c *Conversations) Summarize(key, summary string, keepTokens int) (int, int, error) {
	c.mu.RLock()
	conv, ok := c.by[key]
	c.mu.RUnlock()
	if !ok {
		return 0, 0, errors.New("ERR no such conversation")
	}
	conv.mu.Lock()
	defer conv.mu.Unlock()
	conv.summary = summary
	if keepTokens <= 0 {
		dropped := len(conv.turns)
		conv.turns = nil
		conv.updated = time.Now()
		return dropped, approxTokens(summary), nil
	}
	used := 0
	keepFrom := len(conv.turns)
	for i := len(conv.turns) - 1; i >= 0; i-- {
		t := conv.turns[i]
		if used+t.Tokens > keepTokens && keepFrom < len(conv.turns) {
			break
		}
		used += t.Tokens
		keepFrom = i
	}
	dropped := keepFrom
	conv.turns = append([]Turn{}, conv.turns[keepFrom:]...)
	conv.updated = time.Now()
	return dropped, approxTokens(summary) + used, nil
}

// Reset wipes a conversation. Returns true if it existed.
func (c *Conversations) Reset(key string) bool {
	c.mu.Lock()
	_, ok := c.by[key]
	delete(c.by, key)
	c.mu.Unlock()
	return ok
}

// ConvStats is the CONV.LEN reply shape: count + token estimate.
type ConvStats struct {
	Turns        int `json:"turns"`
	Tokens       int `json:"tokens"`
	HasSummary   bool `json:"has_summary"`
	SummaryToks  int `json:"summary_tokens"`
}

// Stats returns turn count + token estimate for a conversation. Missing
// keys return a zero-valued struct (no error) so dashboards can render
// even before the first turn is appended.
func (c *Conversations) Stats(key string) ConvStats {
	c.mu.RLock()
	conv, ok := c.by[key]
	c.mu.RUnlock()
	if !ok {
		return ConvStats{}
	}
	conv.mu.RLock()
	defer conv.mu.RUnlock()
	tok := 0
	for _, t := range conv.turns {
		tok += t.Tokens
	}
	st := ConvStats{Turns: len(conv.turns), Tokens: tok}
	if conv.summary != "" {
		st.HasSummary = true
		st.SummaryToks = approxTokens(conv.summary)
	}
	return st
}

// Keys lists every active conversation key (sorted is the caller's job).
func (c *Conversations) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.by))
	for k := range c.by {
		out = append(out, k)
	}
	return out
}

// Size returns the total number of active conversations.
func (c *Conversations) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.by)
}
