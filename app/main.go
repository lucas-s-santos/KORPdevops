// Command http-server-projeto-korp expõe uma API HTTP simples com métricas
// no padrão Prometheus.
//
// Endpoints:
//
//	GET /projeto-korp  -> {"nome":"Projeto Korp","horario":"<UTC atual>"}
//	GET /healthz       -> liveness probe
//	GET /readyz        -> readiness probe
//	GET /metrics       -> métricas no formato Prometheus
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
)

// Preenchidos em tempo de build via -ldflags (ver Dockerfile).
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "faz uma requisição a /healthz e sai com 0 ou 1 (usado pelo HEALTHCHECK do Docker)")
	flag.Parse()

	cfg := loadConfig()

	if *healthcheck {
		os.Exit(runHealthcheck(cfg.ListenAddr))
	}

	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	app := newApplication(cfg, logger)

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: app.routes(),

		// Timeouts explícitos: um servidor sem eles mantém conexões lentas
		// abertas para sempre (Slowloris) e vaza file descriptors.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}

	// Encerramento gracioso: o container recebe SIGTERM no `docker stop`.
	// Sem isso, requisições em andamento são cortadas no meio.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("servidor iniciado",
			slog.String("addr", cfg.ListenAddr),
			slog.String("version", version),
			slog.String("commit", commit),
		)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		logger.Error("falha ao iniciar o servidor", slog.Any("erro", err))
		os.Exit(1)
	case <-ctx.Done():
		logger.Info("sinal de término recebido, drenando conexões")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown forçado", slog.Any("erro", err))
		os.Exit(1)
	}
	logger.Info("servidor encerrado com sucesso")
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	// Log estruturado em JSON: pronto para ser coletado por Loki/ELK
	// sem precisar de regex de parsing.
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
