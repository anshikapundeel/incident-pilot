package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/anshikapundeel/incident-pilot/internal/correlate"
	"github.com/anshikapundeel/incident-pilot/internal/model"
	"github.com/anshikapundeel/incident-pilot/internal/route"
	"github.com/anshikapundeel/incident-pilot/internal/rules"
	"github.com/anshikapundeel/incident-pilot/internal/store"
)

func newTestServer() (*httptest.Server, *Server) {
	srv := &Server{
		Engine: &correlate.Engine{Rules: rules.Default()},
		Store:  store.New(),
		Router: &route.Router{}, // no sinks; tests don't want stdout spam
	}
	return httptest.NewServer(srv.Routes()), srv
}

// alertmgrEnv builds a minimal Alertmanager-shaped envelope.
func alertmgrEnv(svc, name string, startsAt time.Time) string {
	body := fmt.Sprintf(`{
		"version": "4",
		"status": "firing",
		"alerts": [{
			"fingerprint": "fp-%s-%s",
			"status": "firing",
			"labels": {
				"alertname": "%s",
				"service":   "%s",
				"severity":  "high",
				"team":      "team-platform"
			},
			"startsAt": "%s"
		}]
	}`, svc, name, name, svc, startsAt.UTC().Format(time.RFC3339))
	return body
}

func TestEndToEnd_AlertProducesIncident(t *testing.T) {
	hs, srv := newTestServer()
	defer hs.Close()

	now := time.Now().UTC()
	resp, err := http.Post(hs.URL+"/alerts", "application/json",
		strings.NewReader(alertmgrEnv("api", "HighLatency", now)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("alerts POST status: %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if n, _ := out["processed"].(float64); int(n) != 1 {
		t.Errorf("processed: %v", out["processed"])
	}
	if srv.Store.Count() != 1 {
		t.Errorf("store should have 1 incident, has %d", srv.Store.Count())
	}
}

func TestEndToEnd_DeployContextEnablesCorrelation(t *testing.T) {
	hs, srv := newTestServer()
	defer hs.Close()

	now := time.Now().UTC()

	// Record a deploy first.
	deployBody := fmt.Sprintf(`{
		"service": "api",
		"commit":  "abcdef1234567",
		"author":  "alice",
		"when":    "%s"
	}`, now.Add(-3*time.Minute).Format(time.RFC3339))
	resp, _ := http.Post(hs.URL+"/context/deploys", "application/json",
		strings.NewReader(deployBody))
	resp.Body.Close()

	// Fire an alert.
	resp, _ = http.Post(hs.URL+"/alerts", "application/json",
		strings.NewReader(alertmgrEnv("api", "HighLatency", now)))
	resp.Body.Close()

	// Check the resulting incident has the deploy.recent_correlated finding.
	all := srv.Store.All()
	if len(all) != 1 {
		t.Fatalf("want 1 incident, got %d", len(all))
	}
	inc := all[0]
	hasDeployFinding := false
	for _, f := range inc.Findings {
		if f.RuleID == "deploy.recent_correlated" {
			hasDeployFinding = true
		}
	}
	if !hasDeployFinding {
		t.Errorf("expected deploy.recent_correlated finding, got rules: %v",
			ruleIDs(inc.Findings))
	}
	// Severity should escalate to high because of the deploy correlation.
	if inc.Severity != "high" && inc.Severity != "critical" {
		t.Errorf("expected high+ severity, got %s", inc.Severity)
	}
}

func TestEndToEnd_ResolvedAlertClosesIncident(t *testing.T) {
	hs, srv := newTestServer()
	defer hs.Close()

	now := time.Now().UTC()
	// Fire
	resp, _ := http.Post(hs.URL+"/alerts", "application/json",
		strings.NewReader(alertmgrEnv("api", "HighLatency", now)))
	resp.Body.Close()

	openBefore := len(srv.Store.OpenIncidents())
	if openBefore != 1 {
		t.Fatalf("want 1 open before resolve, got %d", openBefore)
	}

	// Send a resolved status for the same fingerprint.
	resolved := strings.Replace(alertmgrEnv("api", "HighLatency", now),
		`"status": "firing",
			"labels"`, `"status": "resolved",
			"labels"`, 1)
	resp, _ = http.Post(hs.URL+"/alerts", "application/json", strings.NewReader(resolved))
	resp.Body.Close()

	openAfter := len(srv.Store.OpenIncidents())
	if openAfter != 0 {
		t.Errorf("want 0 open after resolve, got %d", openAfter)
	}
}

func TestEndToEnd_DuplicateAlertFlagged(t *testing.T) {
	hs, srv := newTestServer()
	defer hs.Close()

	now := time.Now().UTC()
	// Fire twice in a row.
	for i := 0; i < 2; i++ {
		// Use a 1-second offset so the second alert has a different
		// fingerprint-derived ID but matches by service+alertname.
		startsAt := now.Add(time.Duration(i) * time.Second)
		body := alertmgrEnv("api", "HighLatency", startsAt)
		// Modify the fingerprint of the second to make it distinct.
		if i == 1 {
			body = strings.Replace(body,
				`"fingerprint": "fp-api-HighLatency"`,
				`"fingerprint": "fp-api-HighLatency-second"`, 1)
		}
		resp, _ := http.Post(hs.URL+"/alerts", "application/json",
			strings.NewReader(body))
		resp.Body.Close()
	}

	// The second incident should have a duplicate.open finding.
	all := srv.Store.All()
	if len(all) != 2 {
		t.Fatalf("want 2 incidents, got %d", len(all))
	}
	// All() is newest-first, so [0] is the second alert.
	second := all[0]
	hasDup := false
	for _, f := range second.Findings {
		if f.RuleID == "incident.duplicate_open" {
			hasDup = true
		}
	}
	if !hasDup {
		t.Errorf("expected duplicate-open finding on second alert, got rules: %v",
			ruleIDs(second.Findings))
	}
}

func TestQueryAPI_GetIncidentByID(t *testing.T) {
	hs, srv := newTestServer()
	defer hs.Close()

	now := time.Now().UTC()
	resp, _ := http.Post(hs.URL+"/alerts", "application/json",
		strings.NewReader(alertmgrEnv("api", "HighLatency", now)))
	resp.Body.Close()

	all := srv.Store.All()
	if len(all) != 1 {
		t.Fatal("expected 1 incident")
	}
	id := all[0].ID

	resp, err := http.Get(hs.URL + "/api/incidents/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status: %d", resp.StatusCode)
	}
	var inc map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&inc); err != nil {
		t.Fatal(err)
	}
	if inc["id"] != id {
		t.Errorf("id mismatch: %v != %s", inc["id"], id)
	}
}

func TestQueryAPI_ResolveEndpoint(t *testing.T) {
	hs, srv := newTestServer()
	defer hs.Close()

	now := time.Now().UTC()
	resp, _ := http.Post(hs.URL+"/alerts", "application/json",
		strings.NewReader(alertmgrEnv("api", "HighLatency", now)))
	resp.Body.Close()

	all := srv.Store.All()
	id := all[0].ID

	resp, err := http.Post(hs.URL+"/api/incidents/"+id+"/resolve",
		"application/json", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("resolve status: %d", resp.StatusCode)
	}
	if len(srv.Store.OpenIncidents()) != 0 {
		t.Errorf("expected 0 open after resolve")
	}
}

func TestQueryAPI_Healthz(t *testing.T) {
	hs, _ := newTestServer()
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("healthz: %d", resp.StatusCode)
	}
}

func TestQueryAPI_Stats(t *testing.T) {
	hs, _ := newTestServer()
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st map[string]any
	json.NewDecoder(resp.Body).Decode(&st)
	if _, ok := st["alerts_received"]; !ok {
		t.Errorf("missing alerts_received in stats: %v", st)
	}
}

func TestIngest_BareAlertAccepted(t *testing.T) {
	hs, srv := newTestServer()
	defer hs.Close()
	now := time.Now().UTC()
	body := fmt.Sprintf(`{
		"fingerprint": "bare-fp",
		"status": "firing",
		"labels": {"alertname": "X", "service": "y", "severity": "high"},
		"startsAt": "%s"
	}`, now.Format(time.RFC3339))
	resp, _ := http.Post(hs.URL+"/alerts", "application/json",
		strings.NewReader(body))
	resp.Body.Close()
	if srv.Store.Count() != 1 {
		t.Errorf("expected bare alert to be accepted; store count=%d", srv.Store.Count())
	}
}

// ruleIDs is a tiny helper for readable error messages.
func ruleIDs(in []model.Finding) []string {
	out := make([]string, 0, len(in))
	for _, f := range in {
		out = append(out, f.RuleID)
	}
	return out
}
