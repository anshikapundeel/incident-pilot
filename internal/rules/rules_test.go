package rules

import (
	"testing"
	"time"

	"github.com/anshikapundeel/incident-pilot/internal/model"
)

func mkAlert(svc, name string, startsAt time.Time) *model.Alert {
	return &model.Alert{
		Fingerprint: "fp-" + svc + "-" + name,
		Status:      "firing",
		Labels: map[string]string{
			"alertname": name,
			"service":   svc,
			"severity":  "high",
			"team":      "team-platform",
		},
		StartsAt: startsAt,
	}
}

// --- RecentDeploy -----------------------------------------------------------

func TestRecentDeploy_FiresOnRecentMatchingDeploy(t *testing.T) {
	now := time.Now().UTC()
	alert := mkAlert("api", "HighLatency", now)
	ctx := &model.Context{
		Now: now,
		RecentDeploys: []model.Deploy{
			{Service: "api", Commit: "abcdef123", When: now.Add(-3 * time.Minute), Author: "alice"},
		},
	}
	rule := NewRecentDeploy()
	findings, err := rule.Evaluate(alert, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	if findings[0].Severity != model.SeverityHigh {
		t.Errorf("want high severity at 3m gap, got %s", findings[0].Severity)
	}
	// Should cite both the deploy and the alert time.
	if len(findings[0].Evidence) < 2 {
		t.Errorf("want >=2 evidence, got %d", len(findings[0].Evidence))
	}
}

func TestRecentDeploy_QuietForUnrelatedService(t *testing.T) {
	now := time.Now().UTC()
	alert := mkAlert("api", "HighLatency", now)
	ctx := &model.Context{
		RecentDeploys: []model.Deploy{
			{Service: "billing", Commit: "abcdef123", When: now.Add(-3 * time.Minute)},
		},
	}
	findings, _ := NewRecentDeploy().Evaluate(alert, ctx)
	if len(findings) != 0 {
		t.Errorf("want 0 findings, got %d", len(findings))
	}
}

func TestRecentDeploy_QuietWhenDeployOutsideWindow(t *testing.T) {
	now := time.Now().UTC()
	alert := mkAlert("api", "HighLatency", now)
	ctx := &model.Context{
		RecentDeploys: []model.Deploy{
			{Service: "api", Commit: "abcdef123", When: now.Add(-3 * time.Hour)},
		},
	}
	findings, _ := NewRecentDeploy().Evaluate(alert, ctx)
	if len(findings) != 0 {
		t.Errorf("want 0 findings (deploy too old), got %d", len(findings))
	}
}

func TestRecentDeploy_PicksClosestMatch(t *testing.T) {
	now := time.Now().UTC()
	alert := mkAlert("api", "HighLatency", now)
	ctx := &model.Context{
		RecentDeploys: []model.Deploy{
			{Service: "api", Commit: "older1", When: now.Add(-45 * time.Minute)},
			{Service: "api", Commit: "closer", When: now.Add(-2 * time.Minute)},
		},
	}
	findings, _ := NewRecentDeploy().Evaluate(alert, ctx)
	if len(findings) != 1 {
		t.Fatalf("want 1, got %d", len(findings))
	}
	// HIGH severity because the closer deploy is within the strong window.
	if findings[0].Severity != model.SeverityHigh {
		t.Errorf("expected HIGH for 2m gap, got %s", findings[0].Severity)
	}
}

// --- ErrorSpike -------------------------------------------------------------

func TestErrorSpike_FiresOnVolume(t *testing.T) {
	now := time.Now().UTC()
	alert := mkAlert("api", "HighLatency", now)
	logs := make([]model.LogSample, 0, 15)
	for i := 0; i < 15; i++ {
		logs = append(logs, model.LogSample{
			Service: "api", Level: "ERROR",
			Message: "request failed: timeout connecting to db",
			When:    now.Add(-time.Duration(i) * time.Second),
		})
	}
	ctx := &model.Context{RecentLogErrors: logs}
	findings, _ := NewErrorSpike().Evaluate(alert, ctx)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	if findings[0].Severity != model.SeverityMedium {
		t.Errorf("expected MEDIUM at 15 errors, got %s", findings[0].Severity)
	}
}

func TestErrorSpike_QuietBelowThreshold(t *testing.T) {
	alert := mkAlert("api", "HighLatency", time.Now().UTC())
	ctx := &model.Context{
		RecentLogErrors: []model.LogSample{
			{Service: "api", Level: "ERROR", Message: "x"},
		},
	}
	findings, _ := NewErrorSpike().Evaluate(alert, ctx)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings below threshold, got %d", len(findings))
	}
}

func TestErrorSpike_IgnoresOtherServices(t *testing.T) {
	alert := mkAlert("api", "HighLatency", time.Now().UTC())
	logs := []model.LogSample{}
	for i := 0; i < 20; i++ {
		logs = append(logs, model.LogSample{Service: "other-svc", Level: "ERROR", Message: "z"})
	}
	ctx := &model.Context{RecentLogErrors: logs}
	findings, _ := NewErrorSpike().Evaluate(alert, ctx)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for unrelated service errors, got %d", len(findings))
	}
}

// --- SlowTraces -------------------------------------------------------------

func TestSlowTraces_FiresOnHighP99(t *testing.T) {
	now := time.Now().UTC()
	alert := mkAlert("api", "HighLatency", now)
	// With 100 samples and p99 = index 98, we need at least 2 samples
	// in the slow tail to make p99 land on a slow value.
	traces := make([]model.TraceSample, 0, 100)
	for i := 0; i < 95; i++ {
		traces = append(traces, model.TraceSample{
			Service: "api", Operation: "GET /home",
			Duration: 50 * time.Millisecond,
			TraceID:  "t-fast",
		})
	}
	for i := 0; i < 5; i++ {
		traces = append(traces, model.TraceSample{
			Service: "api", Operation: "GET /slow",
			Duration: 12 * time.Second,
			TraceID:  "t-slow",
		})
	}
	ctx := &model.Context{RecentTraces: traces}
	findings, _ := NewSlowTraces().Evaluate(alert, ctx)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d", len(findings))
	}
	if findings[0].Severity != model.SeverityHigh {
		t.Errorf("expected HIGH for 12s p99, got %s", findings[0].Severity)
	}
}

func TestSlowTraces_QuietBelowMinSamples(t *testing.T) {
	alert := mkAlert("api", "HighLatency", time.Now().UTC())
	ctx := &model.Context{RecentTraces: []model.TraceSample{
		{Service: "api", Duration: 10 * time.Second},
	}}
	findings, _ := NewSlowTraces().Evaluate(alert, ctx)
	if len(findings) != 0 {
		t.Error("expected 0 findings with insufficient samples")
	}
}

// --- DuplicateOpen ----------------------------------------------------------

func TestDuplicateOpen_FiresOnSameFingerprint(t *testing.T) {
	now := time.Now().UTC()
	alert := mkAlert("api", "HighLatency", now)
	existing := &model.Incident{
		ID:    "INC-existing",
		Alert: *alert,
	}
	ctx := &model.Context{OpenIncidents: []*model.Incident{existing}}
	findings, _ := NewDuplicateOpen().Evaluate(alert, ctx)
	if len(findings) != 1 {
		t.Fatalf("want 1, got %d", len(findings))
	}
	if findings[0].Severity != model.SeverityInfo {
		t.Errorf("dup should be info, got %s", findings[0].Severity)
	}
}

func TestDuplicateOpen_QuietWhenResolved(t *testing.T) {
	now := time.Now().UTC()
	alert := mkAlert("api", "HighLatency", now)
	existing := &model.Incident{
		ID:       "INC-existing",
		Alert:    *alert,
		Resolved: true,
	}
	ctx := &model.Context{OpenIncidents: []*model.Incident{existing}}
	findings, _ := NewDuplicateOpen().Evaluate(alert, ctx)
	if len(findings) != 0 {
		t.Errorf("should not flag resolved incident as duplicate, got %d findings", len(findings))
	}
}

// --- TeamRouting ------------------------------------------------------------

func TestTeamRouting_FiresWithTeamLabel(t *testing.T) {
	alert := mkAlert("api", "HighLatency", time.Now().UTC())
	findings, _ := NewTeamRouting().Evaluate(alert, &model.Context{})
	if len(findings) != 1 {
		t.Fatalf("want 1, got %d", len(findings))
	}
	if findings[0].Severity != model.SeverityInfo {
		t.Errorf("routing finding should be info, got %s", findings[0].Severity)
	}
}

func TestTeamRouting_WarnsWhenNoTeam(t *testing.T) {
	alert := mkAlert("api", "HighLatency", time.Now().UTC())
	delete(alert.Labels, "team")
	findings, _ := NewTeamRouting().Evaluate(alert, &model.Context{})
	if len(findings) != 1 {
		t.Fatalf("want 1, got %d", len(findings))
	}
	if findings[0].Severity != model.SeverityLow {
		t.Errorf("no-team should be LOW, got %s", findings[0].Severity)
	}
}
