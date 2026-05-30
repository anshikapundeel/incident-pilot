// Package model defines the core domain types that flow through the
// incident-pilot pipeline.
//
// The model intentionally mirrors the design captured in
// ai-ops-design (docs/02-incident-pipeline.md):
//
//   Alert  +  Context  ──> [ rule engine ] ──> []Finding ──> Incident
//                                                                │
//                                                                ▼
//                                       narrated + routed (Slack/etc.)
//
// Keeping these types in one place lets every layer (ingest, correlate,
// narrate, route) share the same vocabulary. Following the same
// pattern as perf-advisor: rule engine produces findings with cited
// evidence; LLM only rephrases.
package model

import "time"

// Severity mirrors common alert-manager conventions. Findings inherit
// severity from the rules that fire them; incident severity is the
// max of its findings'.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// rank gives a sortable score; higher = more severe. Used by the
// engine to order findings and to pick an incident's overall severity.
var sevRank = map[Severity]int{
	SeverityInfo:     0,
	SeverityLow:      1,
	SeverityMedium:   2,
	SeverityHigh:     3,
	SeverityCritical: 4,
}

func (s Severity) Rank() int { return sevRank[s] }

func MaxSeverity(a, b Severity) Severity {
	if a.Rank() >= b.Rank() {
		return a
	}
	return b
}

// Confidence is separate from severity, intentionally. A rule may be
// very sure something is true (high confidence) but the operational
// impact is minor (low severity). Both dimensions are surfaced in
// the report so an operator can prioritise correctly.
type Confidence string

const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// Alert is the incoming trigger. Shape mirrors the Prometheus
// Alertmanager webhook payload (subset). We keep only the fields the
// correlation engine actually uses; unknown fields are tolerated by
// the JSON decoder and dropped.
//
// We intentionally do NOT model the entire Alertmanager batch envelope
// here; the ingest layer unwraps a batch into individual Alerts.
type Alert struct {
	Fingerprint string            `json:"fingerprint"`
	Status      string            `json:"status"`     // "firing" or "resolved"
	Labels      map[string]string `json:"labels"`     // includes severity, service, etc.
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
	GeneratorURL string           `json:"generatorURL"`
}

// Service returns the alert's "service" label, or "unknown" if absent.
// Helpers like this exist so rules don't have to defensively
// inspect map keys; they get a sensible default.
func (a *Alert) Service() string {
	if v, ok := a.Labels["service"]; ok && v != "" {
		return v
	}
	return "unknown"
}

func (a *Alert) Team() string {
	if v, ok := a.Labels["team"]; ok && v != "" {
		return v
	}
	return ""
}

func (a *Alert) AlertSeverity() Severity {
	switch a.Labels["severity"] {
	case "critical":
		return SeverityCritical
	case "high", "warning":
		return SeverityHigh
	case "medium":
		return SeverityMedium
	case "low", "info":
		return SeverityLow
	}
	return SeverityMedium
}

func (a *Alert) Name() string {
	if v, ok := a.Labels["alertname"]; ok && v != "" {
		return v
	}
	return "(unnamed)"
}

// Deploy represents a deployment event the platform learned about
// (either via webhook from CI or by reading a `deploys` topic).
// The correlation engine uses these to spot deploy-correlated incidents.
type Deploy struct {
	Service  string    `json:"service"`
	Commit   string    `json:"commit"`
	Version  string    `json:"version"`
	When     time.Time `json:"when"`
	Author   string    `json:"author"`
	Summary  string    `json:"summary"`
}

// TraceSample is a tiny shape over a trace that's potentially relevant
// to the alert. The correlation engine looks at recent slow / errored
// traces for the service to corroborate. Full trace lookup happens via
// the trace store (e.g. flow-trace) at narration time.
type TraceSample struct {
	TraceID   string        `json:"trace_id"`
	Service   string        `json:"service"`
	Operation string        `json:"operation"`
	Duration  time.Duration `json:"duration_ns"`
	HasError  bool          `json:"has_error"`
	When      time.Time     `json:"when"`
}

// LogSample is similarly thin: one error-level log line, with no
// attempt at full-text or structure beyond service + message.
type LogSample struct {
	Service  string    `json:"service"`
	Level    string    `json:"level"`
	Message  string    `json:"message"`
	When     time.Time `json:"when"`
}

// Context is everything the correlation engine has on hand when
// evaluating rules against an alert. Each rule reads what it needs;
// missing data is normal (partial enrichment is the design — see
// ai-ops-design/docs/02-incident-pipeline.md stage 1).
type Context struct {
	Now             time.Time
	RecentDeploys   []Deploy
	RecentTraces    []TraceSample
	RecentLogErrors []LogSample
	OpenIncidents   []*Incident   // for de-duplication and "still-open" rules
}

// Evidence is a pointer from a finding back to the data that triggered
// it. Every finding cites at least one piece of evidence; findings
// without evidence are dropped by the engine (same contract as
// perf-advisor).
type Evidence struct {
	Text   string `json:"text"`
	Source string `json:"source"`         // "deploys", "logs", "traces", "alert", etc.
	Value  any    `json:"value,omitempty"`
}

// Finding is one observation produced by one rule about one alert.
// A rule may produce zero or many findings per alert.
type Finding struct {
	RuleID     string     `json:"rule_id"`
	Title      string     `json:"title"`
	Severity   Severity   `json:"severity"`
	Confidence Confidence `json:"confidence"`
	Summary    string     `json:"summary"`
	Suggestion string     `json:"suggestion"`
	Evidence   []Evidence `json:"evidence"`
	References []string   `json:"references,omitempty"`
}

// Incident is the aggregated, structured output for an alert.
// One alert -> one incident. Multiple findings live inside.
//
// The Narration field is filled by the narrator layer (LLM or
// templated fallback). The data plane never touches Narration; the
// intelligence plane never touches the other fields.
type Incident struct {
	ID         string    `json:"id"`
	Alert      Alert     `json:"alert"`
	Findings   []Finding `json:"findings"`
	Severity   Severity  `json:"severity"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Narration  string    `json:"narration,omitempty"`
	Resolved   bool      `json:"resolved"`
}
