package llmstack

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// MaskTemplates is a fill-in-the-middle (FIM) prompt builder.
// FIM prompts have three pieces — a prefix, a hole, and a suffix —
// but every model expects them in a different shape:
//
//   StarCoder / CodeLlama:
//     <fim_prefix>{prefix}<fim_suffix>{suffix}<fim_middle>
//   DeepSeek-Coder:
//     <｜fim▁begin｜>{prefix}<｜fim▁hole｜>{suffix}<｜fim▁end｜>
//   GPT-3.5/4 chat:
//     System: "Fill in <MASK> in: {prefix}<MASK>{suffix}"
//
// Apps shipping code-completion or image-inpainting flows
// hand-roll these formatters with subtle bugs (wrong sentinel,
// missing newline, wrong order). MASK.* registers each format
// once and BUILD assembles the prompt correctly every time.
//
// Commands:
//
//   MASK.REGISTER format-id template
//        Template uses placeholders {PREFIX} {SUFFIX} {MASK}.
//        E.g. "<fim_prefix>{PREFIX}<fim_suffix>{SUFFIX}<fim_middle>"
//   MASK.BUILD format-id prefix suffix [MASK_VAL m]
//        → assembled prompt with placeholders substituted.
//        Default MASK_VAL is empty (used by completion formats);
//        non-empty for "show this token where the model should
//        fill" formats.
//   MASK.UNREGISTER format-id
//   MASK.LIST
//   MASK.STATS
//
// Throughput: BUILD is a fixed-size strings.NewReplacer — sub-
// microsecond at typical prompt sizes.
type MaskTemplates struct {
	mu        sync.RWMutex
	templates map[string]string

	totalBuilds    atomic.Int64
	totalRegisters atomic.Int64
}

// NewMaskTemplates returns an empty registry. Pre-loads a few
// well-known FIM formats so apps can use them without setup.
func NewMaskTemplates() *MaskTemplates {
	m := &MaskTemplates{templates: map[string]string{}}
	// Pre-loaded formats (operators can override or remove these
	// with MASK.UNREGISTER + MASK.REGISTER).
	m.templates["starcoder"] = "<fim_prefix>{PREFIX}<fim_suffix>{SUFFIX}<fim_middle>"
	m.templates["codellama"] = "<PRE> {PREFIX} <SUF> {SUFFIX} <MID>"
	m.templates["deepseek"] = "<｜fim▁begin｜>{PREFIX}<｜fim▁hole｜>{SUFFIX}<｜fim▁end｜>"
	m.templates["mask_token"] = "{PREFIX}{MASK}{SUFFIX}"
	m.templates["chat_explain"] = "Fill in the {MASK} in this text:\n\n{PREFIX}{MASK}{SUFFIX}"
	return m
}

// Register stores a template. Replaces any existing format with
// the same id. Template MUST contain {PREFIX} and {SUFFIX};
// {MASK} is optional.
func (m *MaskTemplates) Register(formatID, template string) error {
	if formatID == "" {
		return errors.New("format_id required")
	}
	if !strings.Contains(template, "{PREFIX}") {
		return errors.New("template must contain {PREFIX} placeholder")
	}
	if !strings.Contains(template, "{SUFFIX}") {
		return errors.New("template must contain {SUFFIX} placeholder")
	}
	m.mu.Lock()
	m.templates[formatID] = template
	m.mu.Unlock()
	m.totalRegisters.Add(1)
	return nil
}

// Unregister drops a format. Returns true if it existed.
func (m *MaskTemplates) Unregister(formatID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.templates[formatID]
	delete(m.templates, formatID)
	return ok
}

// Build assembles the FIM prompt by substituting placeholders.
// mask is used for {MASK} substitutions (default empty if the
// template doesn't reference {MASK}).
func (m *MaskTemplates) Build(formatID, prefix, suffix, mask string) (string, bool) {
	m.mu.RLock()
	tmpl, ok := m.templates[formatID]
	m.mu.RUnlock()
	if !ok {
		return "", false
	}
	m.totalBuilds.Add(1)
	r := strings.NewReplacer(
		"{PREFIX}", prefix,
		"{SUFFIX}", suffix,
		"{MASK}", mask,
	)
	return r.Replace(tmpl), true
}

// MaskFormatRow is one row of LIST.
type MaskFormatRow struct {
	FormatID string `json:"format_id"`
	Template string `json:"template"`
}

// List returns every registered format, sorted by id.
func (m *MaskTemplates) List() []MaskFormatRow {
	m.mu.RLock()
	out := make([]MaskFormatRow, 0, len(m.templates))
	for id, tmpl := range m.templates {
		out = append(out, MaskFormatRow{FormatID: id, Template: tmpl})
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].FormatID < out[j].FormatID })
	return out
}

// MaskStats is the global snapshot.
type MaskStats struct {
	Formats        int   `json:"formats"`
	TotalBuilds    int64 `json:"total_builds"`
	TotalRegisters int64 `json:"total_registers"`
}

func (m *MaskTemplates) Stats() MaskStats {
	m.mu.RLock()
	n := len(m.templates)
	m.mu.RUnlock()
	return MaskStats{
		Formats:        n,
		TotalBuilds:    m.totalBuilds.Load(),
		TotalRegisters: m.totalRegisters.Load(),
	}
}
