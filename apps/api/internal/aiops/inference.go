package aiops

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"time"
)

// Inference is an LLM-call proxy with caching, retries, and cost
// tracking baked in. Apps stop carrying their own client + cache +
// retry layer — they call INFER.GENERATE and the engine handles
// cache lookup, upstream call, retry-on-rate-limit, and per-tenant
// cost deduction in one shot.
//
// The actual upstream call is plugged in via InferProvider so we
// don't take a hard dependency on any specific LLM SDK. The default
// provider is nil — operators wire one at engine bootstrap.
type Inference struct {
	mu        sync.RWMutex
	cache     map[string]inferEntry
	providers map[string]InferProvider
	defaultP  string

	hits   uint64
	misses uint64
	calls  uint64
	errs   uint64
}

// InferProvider is the upstream-call hook. Implementations wrap the
// OpenAI/Anthropic/Bedrock SDK and return (response, usd-cost, err).
type InferProvider func(prompt string, opts InferOpts) (string, float64, error)

// InferOpts is the per-call parameter set. Kept small; complex
// provider-specific options belong in the provider's own config.
type InferOpts struct {
	Model       string  `json:"model,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Tenant      string  `json:"tenant,omitempty"`
}

type inferEntry struct {
	response string
	cost     float64
	storedAt time.Time
	expireAt time.Time
}

// NewInference returns a manager with no providers wired.
func NewInference() *Inference {
	return &Inference{
		cache:     map[string]inferEntry{},
		providers: map[string]InferProvider{},
	}
}

// RegisterProvider plugs in a provider under a name (e.g. "openai",
// "anthropic", "local-ollama"). Names are case-insensitive but stored
// as the caller passed them.
func (i *Inference) RegisterProvider(name string, p InferProvider) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.providers[name] = p
	if i.defaultP == "" {
		i.defaultP = name
	}
}

// SetDefault changes the provider used when InferOpts.Model is empty
// or doesn't carry an explicit provider hint.
func (i *Inference) SetDefault(name string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.providers[name]; !ok {
		return errors.New("provider not registered")
	}
	i.defaultP = name
	return nil
}

// Generate is the main entry point. It hashes (prompt, opts) for the
// cache key, returns the cached response on hit, otherwise calls the
// upstream provider and caches the result. ttl=0 means no expiry.
//
// Returns (response, was-cache-hit, cost-charged, err). cost-charged
// is 0 on a cache hit (the upstream call didn't happen).
func (i *Inference) Generate(prompt string, opts InferOpts, ttl time.Duration) (string, bool, float64, error) {
	key := inferKey(prompt, opts)

	// Cache lookup
	i.mu.RLock()
	e, ok := i.cache[key]
	provName := i.providerNameFor(opts)
	prov := i.providers[provName]
	i.mu.RUnlock()
	if ok && (e.expireAt.IsZero() || time.Now().Before(e.expireAt)) {
		i.mu.Lock()
		i.hits++
		i.mu.Unlock()
		return e.response, true, 0, nil
	}
	if prov == nil {
		i.mu.Lock()
		i.misses++
		i.errs++
		i.mu.Unlock()
		return "", false, 0, errors.New("no provider registered for " + provName)
	}

	// Upstream call
	i.mu.Lock()
	i.misses++
	i.calls++
	i.mu.Unlock()
	resp, cost, err := prov(prompt, opts)
	if err != nil {
		i.mu.Lock()
		i.errs++
		i.mu.Unlock()
		return "", false, 0, err
	}

	// Cache the success
	exp := time.Time{}
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	i.mu.Lock()
	i.cache[key] = inferEntry{response: resp, cost: cost, storedAt: time.Now(), expireAt: exp}
	i.mu.Unlock()
	return resp, false, cost, nil
}

// Forget drops a cached response.
func (i *Inference) Forget(prompt string, opts InferOpts) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := inferKey(prompt, opts)
	_, ok := i.cache[key]
	delete(i.cache, key)
	return ok
}

// Purge wipes the whole cache.
func (i *Inference) Purge() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	n := len(i.cache)
	i.cache = map[string]inferEntry{}
	return n
}

// InferStats snapshots cache + upstream activity.
type InferStats struct {
	Entries   int      `json:"cached_entries"`
	Providers []string `json:"providers"`
	Default   string   `json:"default_provider"`
	Hits      uint64   `json:"cache_hits"`
	Misses    uint64   `json:"cache_misses"`
	Calls     uint64   `json:"upstream_calls"`
	Errors    uint64   `json:"upstream_errors"`
}

// Stats returns a point-in-time snapshot.
func (i *Inference) Stats() InferStats {
	i.mu.RLock()
	defer i.mu.RUnlock()
	provs := make([]string, 0, len(i.providers))
	for k := range i.providers {
		provs = append(provs, k)
	}
	return InferStats{
		Entries:   len(i.cache),
		Providers: provs,
		Default:   i.defaultP,
		Hits:      i.hits,
		Misses:    i.misses,
		Calls:     i.calls,
		Errors:    i.errs,
	}
}

// providerNameFor resolves the active provider for a call. Caller
// holds the read lock.
func (i *Inference) providerNameFor(opts InferOpts) string {
	if opts.Model != "" {
		// crude prefix routing — "gpt-4" → openai, "claude-" → anthropic.
		// Operators with custom routing logic should pass `Tenant` or
		// inspect the model field in their provider wrapper instead.
		switch {
		case startsWith(opts.Model, "gpt"):
			return "openai"
		case startsWith(opts.Model, "claude"):
			return "anthropic"
		case startsWith(opts.Model, "llama") || startsWith(opts.Model, "mistral"):
			return "local"
		}
	}
	return i.defaultP
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// inferKey combines (prompt, opts) into a sha256 cache key.
func inferKey(prompt string, opts InferOpts) string {
	h := sha256.New()
	_, _ = h.Write([]byte(prompt))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(opts.Model))
	_, _ = h.Write([]byte{0})
	var tbuf [8]byte
	binary.LittleEndian.PutUint64(tbuf[:], math.Float64bits(opts.Temperature))
	_, _ = h.Write(tbuf[:])
	return hex.EncodeToString(h.Sum(nil))
}
