// Command incident-pilot runs the incident-response server.
//
//	incident-pilot                              # listens on :9099
//	incident-pilot -addr :8080
//	INCIDENT_PILOT_SLACK_WEBHOOK=... incident-pilot
//	INCIDENT_PILOT_LLM_PROVIDER=ollama \
//	  INCIDENT_PILOT_LLM_BASE_URL=http://localhost:11434 \
//	  INCIDENT_PILOT_LLM_MODEL=llama3.1:8b \
//	  incident-pilot
//
// The default port (9099) is arbitrary; no standard exists.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anshikapundeel/incident-pilot/internal/correlate"
	"github.com/anshikapundeel/incident-pilot/internal/narrate"
	"github.com/anshikapundeel/incident-pilot/internal/route"
	"github.com/anshikapundeel/incident-pilot/internal/rules"
	"github.com/anshikapundeel/incident-pilot/internal/server"
	"github.com/anshikapundeel/incident-pilot/internal/store"
)

func main() {
	addr := flag.String("addr", ":9099", "address to listen on")
	quiet := flag.Bool("quiet", false, "suppress stdout sink (just log)")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	engine := &correlate.Engine{Rules: rules.Default()}
	st     := store.New()

	router := &route.Router{}
	if !*quiet {
		router.Sinks = append(router.Sinks, &route.StdoutSink{})
	}
	if slack := route.NewSlackSinkFromEnv(); slack != nil {
		router.Sinks = append(router.Sinks, slack)
		slog.Info("slack sink enabled")
	}

	llm := narrate.LLMConfigFromEnv()
	if llm.Complete() {
		slog.Info("LLM narration enabled",
			"provider", llm.Provider, "model", llm.Model)
	} else {
		slog.Info("LLM narration disabled (env vars not set); using templated narration")
	}

	srv := &server.Server{
		Engine: engine,
		Store:  st,
		Router: router,
		LLM:    llm,
	}

	httpsrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("incident-pilot listening", "addr", *addr,
			"endpoints", "POST /alerts · /context/{deploys,traces,logs} · GET /api/incidents")
		if err := httpsrv.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	slog.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpsrv.Shutdown(ctx)
}
