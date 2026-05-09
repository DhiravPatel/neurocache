package llmstack

import "time"

// timeNowUnixNano is wrapped so promptfingerprint.go can take a
// dep-free time read without importing time directly (keeps the
// hot-path file's import list minimal). Inlined by the compiler.
func timeNowUnixNano() int64 { return time.Now().UnixNano() }
