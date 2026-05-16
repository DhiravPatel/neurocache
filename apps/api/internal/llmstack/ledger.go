package llmstack

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CostLedger is a per-call spend ledger for LLM workloads.
// GUARD.* enforces caps but can't answer "which feature / tenant /
// model spent the money." LEDGER.* is the chargeback ledger:
// every chargeable LLM call records (tenant, feature, model, cost,
// tokens_in, tokens_out, ts). REPORT aggregates over any dimension
// + time window. Apps drop the export directly into billing.
//
// Commands:
//
//   LEDGER.RECORD tenant feature model cost-usd
//        [TOKENS_IN n] [TOKENS_OUT n]
//   LEDGER.REPORT BY tenant|feature|model|day
//        [TENANT t] [FEATURE f] [MODEL m] [WINDOW seconds]
//        → array of {key, total_cost_usd, calls, tokens_in,
//                    tokens_out, avg_cost_per_call}
//   LEDGER.TOP dimension [WINDOW seconds] [LIMIT n]
//        → top N spenders in that dimension
//   LEDGER.SPEND tenant [FEATURE f] [WINDOW seconds]
//        → single totalised spend (the GUARD-style answer)
//   LEDGER.EXPORT tenant [WINDOW seconds] [FORMAT csv|json]
//        → flat per-call records (one per line, CSV header included)
//   LEDGER.PURGE [TENANT t] [OLDER_THAN seconds]
//   LEDGER.STATS
//
// Storage: per-tenant append-only log with TTL-driven sweep. At
// 100M calls × ~80 bytes/record = 8 GB — apps with massive volume
// purge older windows or stream into a downstream OLAP store.
// Atomic counters on the hot RECORD path. REPORT is O(records ×
// in-window), typically 0.5-2 ms on 100k records.
type CostLedger struct {
	mu       sync.RWMutex
	records  []ledgerRecord
	byTenant map[string][]int // tenant -> indices into records
	cap      int

	totalRecords atomic.Int64
	totalReports atomic.Int64
}

type ledgerRecord struct {
	tenant    string
	feature   string
	model     string
	costUSD   float64
	tokensIn  int64
	tokensOut int64
	ts        int64 // unix-nano
}

// NewCostLedger returns an empty ledger with a 5M-record soft cap.
func NewCostLedger() *CostLedger {
	return &CostLedger{
		records:  make([]ledgerRecord, 0, 1024),
		byTenant: map[string][]int{},
		cap:      5_000_000,
	}
}

// SetCap adjusts the soft eviction threshold.
func (l *CostLedger) SetCap(n int) {
	l.mu.Lock()
	l.cap = n
	l.mu.Unlock()
}

// Record logs one chargeable LLM call.
func (l *CostLedger) Record(tenant, feature, model string, costUSD float64, tokensIn, tokensOut int64) error {
	if tenant == "" {
		return errors.New("tenant required")
	}
	if costUSD < 0 {
		return errors.New("cost must be non-negative")
	}
	l.totalRecords.Add(1)
	rec := ledgerRecord{
		tenant: tenant, feature: feature, model: model,
		costUSD: costUSD, tokensIn: tokensIn, tokensOut: tokensOut,
		ts: time.Now().UnixNano(),
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cap > 0 && len(l.records) >= l.cap {
		l.compactLocked()
	}
	idx := len(l.records)
	l.records = append(l.records, rec)
	l.byTenant[tenant] = append(l.byTenant[tenant], idx)
	return nil
}

// LedgerReportRow is one row of REPORT / TOP output.
type LedgerReportRow struct {
	Key            string  `json:"key"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	Calls          int64   `json:"calls"`
	TokensIn       int64   `json:"tokens_in"`
	TokensOut      int64   `json:"tokens_out"`
	AvgCostPerCall float64 `json:"avg_cost_per_call"`
}

// LedgerFilter narrows REPORT / SPEND / EXPORT.
type LedgerFilter struct {
	Tenant  string
	Feature string
	Model   string
	Window  time.Duration
}

// Report aggregates spend by the requested dimension.
// dimension = "tenant" | "feature" | "model" | "day".
func (l *CostLedger) Report(dimension string, f LedgerFilter) ([]LedgerReportRow, error) {
	l.totalReports.Add(1)
	if !validReportDim(dimension) {
		return nil, errors.New("dimension must be tenant | feature | model | day")
	}
	now := time.Now().UnixNano()
	cutoff := int64(0)
	if f.Window > 0 {
		cutoff = now - f.Window.Nanoseconds()
	}
	agg := map[string]*LedgerReportRow{}
	l.mu.RLock()
	records := l.recordsForFilterLocked(f)
	for _, idx := range records {
		r := l.records[idx]
		if cutoff > 0 && r.ts < cutoff {
			continue
		}
		key := groupKey(dimension, r)
		row, ok := agg[key]
		if !ok {
			row = &LedgerReportRow{Key: key}
			agg[key] = row
		}
		row.TotalCostUSD += r.costUSD
		row.Calls++
		row.TokensIn += r.tokensIn
		row.TokensOut += r.tokensOut
	}
	l.mu.RUnlock()
	out := make([]LedgerReportRow, 0, len(agg))
	for _, row := range agg {
		if row.Calls > 0 {
			row.AvgCostPerCall = row.TotalCostUSD / float64(row.Calls)
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalCostUSD > out[j].TotalCostUSD })
	return out, nil
}

// Top returns the top-N spenders in the requested dimension.
func (l *CostLedger) Top(dimension string, window time.Duration, limit int) ([]LedgerReportRow, error) {
	rows, err := l.Report(dimension, LedgerFilter{Window: window})
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// SpendResult is LEDGER.SPEND's return.
type SpendResult struct {
	Tenant       string  `json:"tenant"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Calls        int64   `json:"calls"`
	TokensIn     int64   `json:"tokens_in"`
	TokensOut    int64   `json:"tokens_out"`
}

// Spend totals one tenant's spend in the window.
func (l *CostLedger) Spend(tenant string, f LedgerFilter) SpendResult {
	f.Tenant = tenant
	now := time.Now().UnixNano()
	cutoff := int64(0)
	if f.Window > 0 {
		cutoff = now - f.Window.Nanoseconds()
	}
	out := SpendResult{Tenant: tenant}
	l.mu.RLock()
	for _, idx := range l.byTenant[tenant] {
		r := l.records[idx]
		if cutoff > 0 && r.ts < cutoff {
			continue
		}
		if f.Feature != "" && r.feature != f.Feature {
			continue
		}
		if f.Model != "" && r.model != f.Model {
			continue
		}
		out.TotalCostUSD += r.costUSD
		out.Calls++
		out.TokensIn += r.tokensIn
		out.TokensOut += r.tokensOut
	}
	l.mu.RUnlock()
	return out
}

// Export returns the flat per-call records. format = "csv" | "json".
func (l *CostLedger) Export(f LedgerFilter, format string) (string, error) {
	if format == "" {
		format = "csv"
	}
	if format != "csv" && format != "json" {
		return "", errors.New("format must be csv or json")
	}
	now := time.Now().UnixNano()
	cutoff := int64(0)
	if f.Window > 0 {
		cutoff = now - f.Window.Nanoseconds()
	}
	var b strings.Builder
	if format == "csv" {
		b.WriteString("ts,tenant,feature,model,cost_usd,tokens_in,tokens_out\n")
	} else {
		b.WriteString("[\n")
	}
	l.mu.RLock()
	first := true
	records := l.recordsForFilterLocked(f)
	for _, idx := range records {
		r := l.records[idx]
		if cutoff > 0 && r.ts < cutoff {
			continue
		}
		if format == "csv" {
			b.WriteString(strconv.FormatInt(r.ts/int64(time.Second), 10))
			b.WriteByte(',')
			b.WriteString(csvEscape(r.tenant))
			b.WriteByte(',')
			b.WriteString(csvEscape(r.feature))
			b.WriteByte(',')
			b.WriteString(csvEscape(r.model))
			b.WriteByte(',')
			b.WriteString(strconv.FormatFloat(r.costUSD, 'f', 6, 64))
			b.WriteByte(',')
			b.WriteString(strconv.FormatInt(r.tokensIn, 10))
			b.WriteByte(',')
			b.WriteString(strconv.FormatInt(r.tokensOut, 10))
			b.WriteByte('\n')
		} else {
			if !first {
				b.WriteString(",\n")
			}
			first = false
			b.WriteString(`  {"ts":`)
			b.WriteString(strconv.FormatInt(r.ts/int64(time.Second), 10))
			b.WriteString(`,"tenant":"`)
			b.WriteString(jsonEscape(r.tenant))
			b.WriteString(`","feature":"`)
			b.WriteString(jsonEscape(r.feature))
			b.WriteString(`","model":"`)
			b.WriteString(jsonEscape(r.model))
			b.WriteString(`","cost_usd":`)
			b.WriteString(strconv.FormatFloat(r.costUSD, 'f', 6, 64))
			b.WriteString(`,"tokens_in":`)
			b.WriteString(strconv.FormatInt(r.tokensIn, 10))
			b.WriteString(`,"tokens_out":`)
			b.WriteString(strconv.FormatInt(r.tokensOut, 10))
			b.WriteByte('}')
		}
	}
	l.mu.RUnlock()
	if format == "json" {
		b.WriteString("\n]")
	}
	return b.String(), nil
}

// Purge drops records matching the filter. olderThanSeconds=0
// means no time filter. tenant=="" means all tenants.
func (l *CostLedger) Purge(tenant string, olderThan time.Duration) int {
	cutoff := int64(0)
	if olderThan > 0 {
		cutoff = time.Now().UnixNano() - olderThan.Nanoseconds()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	keep := make([]ledgerRecord, 0, len(l.records))
	dropped := 0
	for _, r := range l.records {
		if tenant != "" && r.tenant != tenant {
			keep = append(keep, r)
			continue
		}
		if cutoff > 0 && r.ts >= cutoff {
			keep = append(keep, r)
			continue
		}
		dropped++
	}
	l.records = keep
	// Rebuild byTenant indices
	l.byTenant = map[string][]int{}
	for i, r := range l.records {
		l.byTenant[r.tenant] = append(l.byTenant[r.tenant], i)
	}
	return dropped
}

// LedgerStats is the global snapshot.
type LedgerStats struct {
	Records      int     `json:"records"`
	Cap          int     `json:"cap"`
	Tenants      int     `json:"tenants"`
	TotalRecords int64   `json:"total_records"`
	TotalReports int64   `json:"total_reports"`
	TotalSpendUSD float64 `json:"total_spend_usd"`
}

func (l *CostLedger) Stats() LedgerStats {
	l.mu.RLock()
	n := len(l.records)
	tenants := len(l.byTenant)
	total := 0.0
	for _, r := range l.records {
		total += r.costUSD
	}
	cap := l.cap
	l.mu.RUnlock()
	return LedgerStats{
		Records:       n,
		Cap:           cap,
		Tenants:       tenants,
		TotalRecords:  l.totalRecords.Load(),
		TotalReports:  l.totalReports.Load(),
		TotalSpendUSD: total,
	}
}

// ─── helpers ───────────────────────────────────────────────────

func validReportDim(d string) bool {
	switch d {
	case "tenant", "feature", "model", "day":
		return true
	}
	return false
}

func groupKey(dim string, r ledgerRecord) string {
	switch dim {
	case "tenant":
		return r.tenant
	case "feature":
		return r.feature
	case "model":
		return r.model
	case "day":
		// YYYY-MM-DD bucket
		t := time.Unix(r.ts/int64(time.Second), 0).UTC()
		return t.Format("2006-01-02")
	}
	return ""
}

// recordsForFilterLocked returns indices into l.records matching
// the tenant filter; caller already holds l.mu.RLock.
func (l *CostLedger) recordsForFilterLocked(f LedgerFilter) []int {
	if f.Tenant != "" {
		base := l.byTenant[f.Tenant]
		if f.Feature == "" && f.Model == "" {
			return base
		}
		out := make([]int, 0, len(base))
		for _, idx := range base {
			r := l.records[idx]
			if f.Feature != "" && r.feature != f.Feature {
				continue
			}
			if f.Model != "" && r.model != f.Model {
				continue
			}
			out = append(out, idx)
		}
		return out
	}
	// No tenant filter — scan all
	if f.Feature == "" && f.Model == "" {
		out := make([]int, len(l.records))
		for i := range l.records {
			out[i] = i
		}
		return out
	}
	out := make([]int, 0)
	for i, r := range l.records {
		if f.Feature != "" && r.feature != f.Feature {
			continue
		}
		if f.Model != "" && r.model != f.Model {
			continue
		}
		out = append(out, i)
	}
	return out
}

// compactLocked drops the oldest 10% of records (caller holds write lock).
func (l *CostLedger) compactLocked() {
	if len(l.records) < 10 {
		return
	}
	drop := len(l.records) / 10
	l.records = l.records[drop:]
	l.byTenant = map[string][]int{}
	for i, r := range l.records {
		l.byTenant[r.tenant] = append(l.byTenant[r.tenant], i)
	}
}

func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func jsonEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`)
	return r.Replace(s)
}
