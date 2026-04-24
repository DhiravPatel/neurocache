package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Host           string
	HTTPPort       string
	RESPPort       string
	MaxMemoryMB    int
	Eviction       string
	EmbeddingDim   int
	SemThreshold   float64
	DataDir        string
	AOFEnabled     bool
	AOFFsync       string // "always" | "everysec" | "no"
	RDBEnabled     bool
	RDBIntervalSec int
	LogLevel       string
	LogFormat      string
	CORSOrigins    []string

	// Access control
	ACLFile        string // path to ACL users file (empty => default users.acl in DataDir)
	RequirePass    string // legacy "requirepass" — default user password when no ACL file
	ProtectedMode  bool   // when true, refuse to run commands from unauthenticated clients

	// Introspection limits
	SlowLogMaxLen    int   // entries kept in the slowlog ring
	SlowLogThreshold int64 // microseconds; commands slower than this enter the slowlog
	LatencyMaxLen    int   // events kept per latency event name
	ClientIdleMax    int   // seconds; 0 disables the CLIENT NO-EVICT idle cap

	// Scripting
	ScriptTimeoutMs int // Lua script wall-clock ceiling (5000 = 5s)
}

func Load() Config {
	return Config{
		Host:         env("NEUROCACHE_HOST", "0.0.0.0"),
		HTTPPort:     env("NEUROCACHE_HTTP_PORT", envOr("PORT", "8080")),
		RESPPort:     env("NEUROCACHE_RESP_PORT", "6379"),
		MaxMemoryMB:  parseMemoryMB(env("NEUROCACHE_MAX_MEMORY", "512mb")),
		Eviction:     env("NEUROCACHE_EVICTION_POLICY", "ai-smart"),
		EmbeddingDim: envInt("NEUROCACHE_EMBEDDING_DIM", 384),
		SemThreshold: envFloat("NEUROCACHE_SEMANTIC_THRESHOLD", 0.75),
		DataDir:        env("NEUROCACHE_DATA_DIR", "./data"),
		AOFEnabled:     envBool("NEUROCACHE_AOF_ENABLED", false),
		AOFFsync:       strings.ToLower(env("NEUROCACHE_AOF_FSYNC", "everysec")),
		RDBEnabled:     envBool("NEUROCACHE_RDB_ENABLED", false),
		RDBIntervalSec: envInt("NEUROCACHE_RDB_INTERVAL_SEC", 300),
		LogLevel:       env("NEUROCACHE_LOG_LEVEL", "info"),
		LogFormat:      env("NEUROCACHE_LOG_FORMAT", "text"),
		CORSOrigins:    splitCSV(env("NEUROCACHE_CORS_ORIGINS", "*")),

		ACLFile:       env("NEUROCACHE_ACL_FILE", ""),
		RequirePass:   env("NEUROCACHE_REQUIREPASS", ""),
		ProtectedMode: envBool("NEUROCACHE_PROTECTED_MODE", false),

		SlowLogMaxLen:    envInt("NEUROCACHE_SLOWLOG_MAX_LEN", 128),
		SlowLogThreshold: int64(envInt("NEUROCACHE_SLOWLOG_THRESHOLD_US", 10000)),
		LatencyMaxLen:    envInt("NEUROCACHE_LATENCY_MAX_LEN", 160),
		ClientIdleMax:    envInt("NEUROCACHE_CLIENT_IDLE_MAX", 0),

		ScriptTimeoutMs: envInt("NEUROCACHE_SCRIPT_TIMEOUT_MS", 5000),
	}
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envOr(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, ok := os.LookupEnv(k); ok {
		switch strings.ToLower(v) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseMemoryMB(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := 1
	switch {
	case strings.HasSuffix(s, "gb"):
		mult = 1024
		s = strings.TrimSuffix(s, "gb")
	case strings.HasSuffix(s, "mb"):
		s = strings.TrimSuffix(s, "mb")
	case strings.HasSuffix(s, "kb"):
		s = strings.TrimSuffix(s, "kb")
		if n, err := strconv.Atoi(s); err == nil {
			return n / 1024
		}
		return 512
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 512
	}
	return n * mult
}
