package governance

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ── AnomalyDetector ───────────────────────────────────────────
//
// Detects suspicious patterns on memory writes that may indicate:
//
//   1. Memory flooding  — an agent writing at an unusually high rate
//   2. Prompt injection — content that contains injection markers
//   3. Scope escalation — an agent trying to write to a scope above its grant
//   4. Confidence manipulation — suspiciously high confidence on untrusted writes
//   5. Large payload anomaly — single writes that are unexpectedly large
//
// Detections are lightweight and synchronous; they add minimal latency to
// the memory write path. Quarantine decisions are made by the caller
// (memory.Store) based on the returned AnomalyReport.

// Severity of an anomaly finding.
type AnomalySeverity string

const (
	AnomalySeverityLow    AnomalySeverity = "low"
	AnomalySeverityMedium AnomalySeverity = "medium"
	AnomalySeverityHigh   AnomalySeverity = "high"
)

// AnomalyFinding describes a single detected anomaly.
type AnomalyFinding struct {
	Kind     string          // e.g. "rate_limit", "injection", "scope_escalation"
	Severity AnomalySeverity
	Detail   string
}

// AnomalyReport is the result of Inspect.
type AnomalyReport struct {
	Findings         []AnomalyFinding
	ShouldQuarantine bool
	Reason           string // human-readable summary for quarantine_reason
}

func (r *AnomalyReport) HasFindings() bool { return len(r.Findings) > 0 }

// WriteRequest is the input to Inspect.
type WriteRequest struct {
	ProjectID       uuid.UUID
	AgentInstanceID *uuid.UUID
	Scope           string // memory_scope value
	AllowedScopes   []string
	Content         string
	Confidence      float64
	TrustTier       string // "verified" | "observed" | "untrusted"
}

// AnomalyDetector inspects memory write requests for suspicious patterns.
type AnomalyDetector struct {
	db       *sql.DB
	redactor *Redactor

	// Per-(project, agent) sliding window for rate limiting
	mu      sync.Mutex
	windows map[rateKey]*rateWindow
}

type rateKey struct {
	projectID uuid.UUID
	agentID   uuid.UUID
}

type rateWindow struct {
	counts []time.Time
}

// Rate limits (configurable via constants for now)
const (
	rateWindowDuration = 1 * time.Minute
	maxWritesPerWindow = 30  // writes/min per agent per project
	maxContentBytes    = 64 * 1024 // 64 KB
)

// NewAnomalyDetector creates an AnomalyDetector.
func NewAnomalyDetector(db *sql.DB, redactor *Redactor) *AnomalyDetector {
	return &AnomalyDetector{
		db:       db,
		redactor: redactor,
		windows:  make(map[rateKey]*rateWindow),
	}
}

// Inspect evaluates a memory write request and returns any anomaly findings.
// It is safe to call concurrently.
func (d *AnomalyDetector) Inspect(ctx context.Context, req WriteRequest) *AnomalyReport {
	report := &AnomalyReport{}

	// 1. Rate limiting
	if req.AgentInstanceID != nil {
		if exceeded, count := d.checkRateLimit(*req.AgentInstanceID, req.ProjectID); exceeded {
			report.Findings = append(report.Findings, AnomalyFinding{
				Kind:     "rate_limit",
				Severity: AnomalySeverityHigh,
				Detail:   fmt.Sprintf("agent wrote %d times in last minute (limit %d)", count, maxWritesPerWindow),
			})
		}
	}

	// 2. Prompt injection detection
	if LooksLikeInjection(req.Content) {
		labels := d.redactor.MatchedLabels(req.Content)
		detail := "injection markers detected in content"
		if len(labels) > 0 {
			detail += fmt.Sprintf("; also matched PII/secret patterns: %v", labels)
		}
		report.Findings = append(report.Findings, AnomalyFinding{
			Kind:     "prompt_injection",
			Severity: AnomalySeverityHigh,
			Detail:   detail,
		})
	}

	// 3. PII / secret leakage
	if d.redactor.ContainsSensitive(req.Content) {
		labels := d.redactor.MatchedLabels(req.Content)
		report.Findings = append(report.Findings, AnomalyFinding{
			Kind:     "sensitive_data",
			Severity: AnomalySeverityMedium,
			Detail:   fmt.Sprintf("content matches PII/secret patterns: %v", labels),
		})
	}

	// 4. Scope escalation — agent writing to a scope outside its grants
	if req.AgentInstanceID != nil && len(req.AllowedScopes) > 0 {
		if !scopeAllowed(req.Scope, req.AllowedScopes) {
			report.Findings = append(report.Findings, AnomalyFinding{
				Kind:     "scope_escalation",
				Severity: AnomalySeverityHigh,
				Detail:   fmt.Sprintf("agent attempted write to scope %q; allowed scopes: %v", req.Scope, req.AllowedScopes),
			})
		}
	}

	// 5. Confidence manipulation — untrusted content claiming high confidence
	if req.TrustTier == "untrusted" && req.Confidence > 0.75 {
		report.Findings = append(report.Findings, AnomalyFinding{
			Kind:     "confidence_manipulation",
			Severity: AnomalySeverityMedium,
			Detail:   fmt.Sprintf("untrusted write with confidence %.2f (>0.75)", req.Confidence),
		})
	}

	// 6. Oversized payload
	if len(req.Content) > maxContentBytes {
		report.Findings = append(report.Findings, AnomalyFinding{
			Kind:     "large_payload",
			Severity: AnomalySeverityLow,
			Detail:   fmt.Sprintf("content is %d bytes (limit %d)", len(req.Content), maxContentBytes),
		})
	}

	// Decide quarantine: any HIGH finding or two or more findings of any severity
	for _, f := range report.Findings {
		if f.Severity == AnomalySeverityHigh {
			report.ShouldQuarantine = true
		}
	}
	if len(report.Findings) >= 2 {
		report.ShouldQuarantine = true
	}

	if report.ShouldQuarantine {
		report.Reason = summariseFindings(report.Findings)
	}

	return report
}

// ── Rate-limit sliding window ─────────────────────────────────

func (d *AnomalyDetector) checkRateLimit(agentID, projectID uuid.UUID) (exceeded bool, count int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	key := rateKey{projectID, agentID}
	now := time.Now()
	cutoff := now.Add(-rateWindowDuration)

	w, ok := d.windows[key]
	if !ok {
		w = &rateWindow{}
		d.windows[key] = w
	}

	// Evict old entries
	fresh := w.counts[:0]
	for _, t := range w.counts {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	w.counts = append(fresh, now)

	count = len(w.counts)
	exceeded = count > maxWritesPerWindow
	return
}

// PruneWindows removes stale rate-limit windows. Call periodically from
// a maintenance goroutine to prevent unbounded memory growth.
func (d *AnomalyDetector) PruneWindows() {
	d.mu.Lock()
	defer d.mu.Unlock()

	cutoff := time.Now().Add(-rateWindowDuration)
	for k, w := range d.windows {
		if len(w.counts) == 0 || w.counts[len(w.counts)-1].Before(cutoff) {
			delete(d.windows, k)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────

func scopeAllowed(scope string, allowed []string) bool {
	for _, s := range allowed {
		if s == scope {
			return true
		}
	}
	return false
}

func summariseFindings(findings []AnomalyFinding) string {
	if len(findings) == 0 {
		return ""
	}
	kinds := make([]string, 0, len(findings))
	for _, f := range findings {
		kinds = append(kinds, f.Kind)
	}
	result := "anomaly detected: "
	for i, k := range kinds {
		if i > 0 {
			result += ", "
		}
		result += k
	}
	return result
}
