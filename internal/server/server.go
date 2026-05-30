// Package server implements the HTTP API. Endpoints:
//
//   POST /alerts                  — Alertmanager-compatible webhook ingest
//   POST /context/deploys         — record a deploy event
//   POST /context/traces          — record a trace sample (optional)
//   POST /context/logs            — record an error log line (optional)
//   GET  /api/incidents           — list recent incidents
//   GET  /api/incidents/{id}      — fetch one incident
//   POST /api/incidents/{id}/resolve  — mark resolved
//   GET  /api/stats               — counters
//   GET  /healthz                 — liveness
//
// Alertmanager fires batches. We unwrap the batch and process each
// alert independently.
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anshikapundeel/incident-pilot/internal/correlate"
	"github.com/anshikapundeel/incident-pilot/internal/model"
	"github.com/anshikapundeel/incident-pilot/internal/narrate"
	"github.com/anshikapundeel/incident-pilot/internal/route"
	"github.com/anshikapundeel/incident-pilot/internal/store"
)

// Server is the HTTP frontend; it owns the engine, store, router, and
// the in-memory context buffers.
type Server struct {
	Engine *correlate.Engine
	Store  *store.Store
	Router *route.Router
	LLM    narrate.LLMConfig

	mu              sync.RWMutex
	recentDeploys   []model.Deploy
	recentTraces    []model.TraceSample
	recentLogErrors []model.LogSample

	alertsIn   atomic.Uint64
	incidents  atomic.Uint64
	dropped    atomic.Uint64
}

// alertManagerEnvelope mirrors the relevant subset of the Alertmanager
// webhook JSON shape.
type alertManagerEnvelope struct {
	Version  string         `json:"version"`
	Status   string         `json:"status"`
	Receiver string         `json:"receiver"`
	Alerts   []model.Alert  `json:"alerts"`
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /alerts",                          s.handleAlerts)
	mux.HandleFunc("POST /context/deploys",                 s.handleDeploys)
	mux.HandleFunc("POST /context/traces",                  s.handleTraces)
	mux.HandleFunc("POST /context/logs",                    s.handleLogs)
	mux.HandleFunc("GET /api/incidents",                    s.handleIncidents)
	mux.HandleFunc("GET /api/incidents/{id}",               s.handleIncidentByID)
	mux.HandleFunc("POST /api/incidents/{id}/resolve",      s.handleResolve)
	mux.HandleFunc("GET /api/stats",                        s.handleStats)
	mux.HandleFunc("GET /healthz",                          s.handleHealthz)
	return mux
}

// handleAlerts unwraps the Alertmanager batch and processes each alert.
// Each alert becomes one Incident.
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var env alertManagerEnvelope
	envelopeErr := json.Unmarshal(body, &env)
	// If the envelope didn't parse OR it parsed but contained no alerts
	// AND no envelope-level fields (version), then the payload is probably
	// a bare single alert from a non-Alertmanager sender. Try that path.
	if envelopeErr != nil || (len(env.Alerts) == 0 && env.Version == "") {
		var single model.Alert
		if err := json.Unmarshal(body, &single); err != nil || single.Fingerprint == "" {
			writeError(w, http.StatusBadRequest,
				"could not parse as Alertmanager envelope or bare alert")
			return
		}
		env.Alerts = []model.Alert{single}
	}

	incs := make([]string, 0, len(env.Alerts))
	for i := range env.Alerts {
		alert := &env.Alerts[i]
		s.alertsIn.Add(1)

		// Only process firing alerts. Resolved-state messages mark
		// any matching open incident as resolved.
		if alert.Status == "resolved" {
			s.resolveByFingerprint(alert.Fingerprint)
			continue
		}

		inc := s.processOneAlert(alert)
		if inc != nil {
			s.incidents.Add(1)
			incs = append(incs, inc.ID)
		} else {
			s.dropped.Add(1)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"processed": len(env.Alerts),
		"incidents": incs,
	})
}

// processOneAlert runs the pipeline for a single alert and returns the
// stored incident. Returns nil if the alert was dropped (e.g. obvious
// duplicate the engine didn't even need to process).
func (s *Server) processOneAlert(alert *model.Alert) *model.Incident {
	ctx := s.buildContext()

	findings := s.Engine.Run(alert, ctx)
	severity := s.aggregateSeverity(alert, findings)

	id := store.NewID(alert)
	now := time.Now().UTC()
	inc := &model.Incident{
		ID:        id,
		Alert:     *alert,
		Findings:  findings,
		Severity:  severity,
		CreatedAt: now,
		UpdatedAt: now,
	}

	// Narrate. LLM if configured; templated fallback always.
	if s.LLM.Complete() {
		if narr := narrate.Explain(inc, s.LLM); narr != "" {
			inc.Narration = narr
		}
	}
	if inc.Narration == "" {
		inc.Narration = narrate.Templated(inc)
	}

	s.Store.Add(inc)
	s.Router.Deliver(inc)
	return inc
}

// aggregateSeverity picks the incident severity as the max of:
//   - the alert's own labeled severity
//   - every finding's severity
// Floor is the alert's label, so a "high" alert never gets demoted by
// only "info" findings firing.
func (s *Server) aggregateSeverity(a *model.Alert, findings []model.Finding) model.Severity {
	sev := a.AlertSeverity()
	for _, f := range findings {
		sev = model.MaxSeverity(sev, f.Severity)
	}
	return sev
}

// buildContext snapshots the in-memory enrichment buffers + open
// incidents. We deliberately limit RecentDeploys/Traces/Logs to the
// last hour to keep the rule input bounded.
func (s *Server) buildContext() *model.Context {
	cutoff := time.Now().Add(-1 * time.Hour)

	s.mu.RLock()
	deploys := filterDeploys(s.recentDeploys, cutoff)
	traces  := filterTraces(s.recentTraces, cutoff)
	logs    := filterLogs(s.recentLogErrors, cutoff)
	s.mu.RUnlock()

	return &model.Context{
		Now:             time.Now().UTC(),
		RecentDeploys:   deploys,
		RecentTraces:    traces,
		RecentLogErrors: logs,
		OpenIncidents:   s.Store.OpenIncidents(),
	}
}

func filterDeploys(in []model.Deploy, cutoff time.Time) []model.Deploy {
	out := in[:0:0]
	for _, d := range in {
		if d.When.After(cutoff) {
			out = append(out, d)
		}
	}
	return out
}
func filterTraces(in []model.TraceSample, cutoff time.Time) []model.TraceSample {
	out := in[:0:0]
	for _, t := range in {
		if t.When.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}
func filterLogs(in []model.LogSample, cutoff time.Time) []model.LogSample {
	out := in[:0:0]
	for _, l := range in {
		if l.When.After(cutoff) {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) resolveByFingerprint(fp string) {
	if fp == "" {
		return
	}
	for _, inc := range s.Store.OpenIncidents() {
		if inc.Alert.Fingerprint == fp {
			s.Store.Resolve(inc.ID)
		}
	}
}

// ---- context-enrichment endpoints --------------------------------------

func (s *Server) handleDeploys(w http.ResponseWriter, r *http.Request) {
	var d model.Deploy
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if d.When.IsZero() {
		d.When = time.Now().UTC()
	}
	s.mu.Lock()
	s.recentDeploys = trimAndAppendDeploy(s.recentDeploys, d, 200)
	s.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]string{"recorded": d.Service})
}

func (s *Server) handleTraces(w http.ResponseWriter, r *http.Request) {
	var t model.TraceSample
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if t.When.IsZero() {
		t.When = time.Now().UTC()
	}
	s.mu.Lock()
	s.recentTraces = trimAndAppendTrace(s.recentTraces, t, 500)
	s.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]string{"recorded": t.TraceID})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	var l model.LogSample
	if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if l.When.IsZero() {
		l.When = time.Now().UTC()
	}
	s.mu.Lock()
	s.recentLogErrors = trimAndAppendLog(s.recentLogErrors, l, 1000)
	s.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]string{"recorded": l.Service})
}

// trimAndAppend* keeps the buffers bounded — the platform's enrichment
// state never exceeds a few-MB worst case regardless of traffic.
func trimAndAppendDeploy(in []model.Deploy, x model.Deploy, max int) []model.Deploy {
	if len(in) >= max {
		in = in[len(in)-max+1:]
	}
	return append(in, x)
}
func trimAndAppendTrace(in []model.TraceSample, x model.TraceSample, max int) []model.TraceSample {
	if len(in) >= max {
		in = in[len(in)-max+1:]
	}
	return append(in, x)
}
func trimAndAppendLog(in []model.LogSample, x model.LogSample, max int) []model.LogSample {
	if len(in) >= max {
		in = in[len(in)-max+1:]
	}
	return append(in, x)
}

// ---- query endpoints -------------------------------------------------------

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
		if limit < 1 || limit > 500 {
			limit = 50
		}
	}
	all := s.Store.All()
	if len(all) > limit {
		all = all[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"count":     len(all),
		"incidents": all,
	})
}

func (s *Server) handleIncidentByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inc, ok := s.Store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "incident not found")
		return
	}
	writeJSON(w, http.StatusOK, inc)
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.Store.Resolve(id) {
		writeError(w, http.StatusNotFound, "incident not found")
		return
	}
	slog.Info("incident resolved", "id", id)
	writeJSON(w, http.StatusOK, map[string]string{"resolved": id})
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"alerts_received":   s.alertsIn.Load(),
		"incidents_opened":  s.incidents.Load(),
		"alerts_dropped":    s.dropped.Load(),
		"incidents_in_store": s.Store.Count(),
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Write([]byte("ok"))
}

// ---- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": strings.TrimSpace(msg)})
}
