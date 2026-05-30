package correlate

import (
	"errors"
	"testing"
	"time"

	"github.com/anshikapundeel/incident-pilot/internal/model"
)

type noopRule struct{}

func (noopRule) ID() string    { return "test.noop" }
func (noopRule) Title() string { return "noop" }
func (noopRule) Evaluate(*model.Alert, *model.Context) ([]model.Finding, error) {
	return nil, nil
}

type firingRule struct{}

func (firingRule) ID() string    { return "test.firing" }
func (firingRule) Title() string { return "firing" }
func (firingRule) Evaluate(*model.Alert, *model.Context) ([]model.Finding, error) {
	return []model.Finding{{
		RuleID:     "test.firing",
		Title:      "fired",
		Severity:   model.SeverityMedium,
		Confidence: model.ConfidenceHigh,
		Summary:    "x",
		Suggestion: "y",
		Evidence:   []model.Evidence{{Text: "z", Source: "test"}},
	}}, nil
}

type noEvidenceRule struct{}

func (noEvidenceRule) ID() string    { return "test.no_evidence" }
func (noEvidenceRule) Title() string { return "no evidence" }
func (noEvidenceRule) Evaluate(*model.Alert, *model.Context) ([]model.Finding, error) {
	return []model.Finding{{
		RuleID:   "test.no_evidence",
		Title:    "x",
		Severity: model.SeverityHigh,
		Evidence: nil, // empty — should be dropped
	}}, nil
}

type erroringRule struct{}

func (erroringRule) ID() string    { return "test.error" }
func (erroringRule) Title() string { return "error" }
func (erroringRule) Evaluate(*model.Alert, *model.Context) ([]model.Finding, error) {
	return nil, errors.New("broken")
}

type panickyRule struct{}

func (panickyRule) ID() string    { return "test.panic" }
func (panickyRule) Title() string { return "panic" }
func (panickyRule) Evaluate(*model.Alert, *model.Context) ([]model.Finding, error) {
	panic("oh no")
}

func mkAlert() *model.Alert {
	return &model.Alert{
		Fingerprint: "fp",
		Labels:      map[string]string{"alertname": "x", "service": "y"},
		StartsAt:    time.Now().UTC(),
	}
}

func TestEngine_DropsFindingsWithoutEvidence(t *testing.T) {
	e := &Engine{Rules: []Rule{firingRule{}, noEvidenceRule{}}}
	out := e.Run(mkAlert(), &model.Context{})
	ids := []string{}
	for _, f := range out {
		ids = append(ids, f.RuleID)
	}
	if len(ids) != 1 || ids[0] != "test.firing" {
		t.Errorf("expected only test.firing to survive, got %v", ids)
	}
}

func TestEngine_SurvivesErroringRule(t *testing.T) {
	e := &Engine{Rules: []Rule{firingRule{}, erroringRule{}}}
	out := e.Run(mkAlert(), &model.Context{})
	hasFiring := false
	hasError := false
	for _, f := range out {
		if f.RuleID == "test.firing" {
			hasFiring = true
		}
		if f.RuleID == "test.error" {
			hasError = true
			if f.Severity != model.SeverityLow {
				t.Errorf("error finding should be LOW, got %s", f.Severity)
			}
		}
	}
	if !hasFiring || !hasError {
		t.Errorf("want both firing+error in output, got %v", out)
	}
}

func TestEngine_SurvivesPanickyRule(t *testing.T) {
	e := &Engine{Rules: []Rule{firingRule{}, panickyRule{}}}
	out := e.Run(mkAlert(), &model.Context{})
	hasPanic := false
	for _, f := range out {
		if f.RuleID == "test.panic" {
			hasPanic = true
		}
	}
	if !hasPanic {
		t.Errorf("panic finding should be present, got %v", out)
	}
}

func TestEngine_SortsBySeverityThenConfidence(t *testing.T) {
	highRule := func(id string, sev model.Severity, conf model.Confidence) Rule {
		return &mockRule{id: id, sev: sev, conf: conf}
	}
	e := &Engine{Rules: []Rule{
		highRule("a", model.SeverityLow, model.ConfidenceHigh),
		highRule("b", model.SeverityHigh, model.ConfidenceLow),
		highRule("c", model.SeverityHigh, model.ConfidenceHigh),
		highRule("d", model.SeverityMedium, model.ConfidenceMedium),
	}}
	out := e.Run(mkAlert(), &model.Context{})
	if len(out) != 4 {
		t.Fatalf("want 4, got %d", len(out))
	}
	want := []string{"c", "b", "d", "a"}
	for i, expectedID := range want {
		if out[i].RuleID != expectedID {
			t.Errorf("pos %d: want %s, got %s", i, expectedID, out[i].RuleID)
		}
	}
}

type mockRule struct {
	id   string
	sev  model.Severity
	conf model.Confidence
}

func (m *mockRule) ID() string    { return m.id }
func (m *mockRule) Title() string { return m.id }
func (m *mockRule) Evaluate(*model.Alert, *model.Context) ([]model.Finding, error) {
	return []model.Finding{{
		RuleID:     m.id,
		Title:      m.id,
		Severity:   m.sev,
		Confidence: m.conf,
		Evidence:   []model.Evidence{{Text: "x", Source: "test"}},
	}}, nil
}
