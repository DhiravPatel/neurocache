package engine

import (
	_ "unsafe" // for go:linkname
)

// nowMono returns runtime monotonic time in nanoseconds. Linked to
// runtime.nanotime so the FastClock loop avoids the wall-clock vDSO
// cost on every tick. Standard runtime hook, used by time.Since and
// runtime/trace internally.
//
//go:noescape
//go:linkname nowMono runtime.nanotime
func nowMono() int64
