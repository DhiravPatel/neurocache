package resp

import "github.com/dhiravpatel/neurocache/apps/api/internal/llmstack"

// llmstackFingerprint is a one-line indirection so commands_aiops_v3.go
// can call the pure Fingerprint() function without dragging the
// llmstack import into the dispatch handler file's deps. Splitting
// it into its own file keeps the imports in commands_aiops_v3.go
// minimal — the dispatch handler is the one file we want to keep
// scannable.
func llmstackFingerprint(s string) string { return llmstack.Fingerprint(s) }
