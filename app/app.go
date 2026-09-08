package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// application carrega as dependências dos handlers (config, logger, métricas
// e a função de relógio). Injetar em vez de usar variáveis globais é o que
// torna os handlers testáveis de forma determinística.
type application struct {
	cfg     config
	logger  *slog.Logger
	metrics *metrics
	now     func() time.Time
	started time.Time
}

func newApplication(cfg config, logger *slog.Logger) *application {
	return &application{
		cfg:     cfg,
		logger:  logger,
		metrics: newMetrics(cfg.ServiceName),
		now:     time.Now,
		started: time.Now(),
	}
}

// korpResponse é o contrato do endpoint principal.
type korpResponse struct {
	Nome    string `json:"nome"`
	Horario string `json:"horario"`
}

func (app *application) routes() http.Handler {
	mux := http.NewServeMux()

	// Padrões com método (Go 1.22+): um GET em rota registrada só como POST
	// devolve 405 automaticamente, sem `if r.Method != ...` em cada handler.
	mux.HandleFunc("GET /projeto-korp", app.handleProjetoKorp)
	mux.HandleFunc("GET /healthz", app.handleHealthz)
	mux.HandleFunc("GET /readyz", app.handleReadyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(
		app.metrics.registry,
		promhttp.HandlerOpts{
			ErrorHandling:     promhttp.ContinueOnError,
			EnableOpenMetrics: true,
		},
	))
	mux.HandleFunc("/", app.handleNotFound)

	// Ordem importa: recoverPanic é o mais externo, para que um pânico dentro
	// da instrumentação ou do handler vire 500 em vez de derrubar o processo.
	return app.recoverPanic(app.instrument(app.logRequests(mux)))
}

// handleProjetoKorp devolve o nome do projeto e o horário atual em UTC,
// resolvido a cada requisição (nada é memorizado entre chamadas).
func (app *application) handleProjetoKorp(w http.ResponseWriter, r *http.Request) {
	resp := korpResponse{
		Nome: "Projeto Korp",
		// RFC 3339 com precisão de segundos e sufixo "Z": formato sem
		// ambiguidade de fuso, o que qualquer cliente consegue parsear.
		Horario: app.now().UTC().Format(time.RFC3339),
	}
	app.writeJSON(w, r, http.StatusOK, resp)
}

// handleHealthz é a liveness probe: responde enquanto o processo consegue
// atender. Não toca em dependências externas de propósito — se um banco cair,
// reiniciar o container não resolveria nada.
func (app *application) handleHealthz(w http.ResponseWriter, r *http.Request) {
	app.writeJSON(w, r, http.StatusOK, map[string]any{
		"status":  "ok",
		"servico": app.cfg.ServiceName,
		"versao":  version,
		"uptime":  app.now().Sub(app.started).Round(time.Second).String(),
	})
}

// handleReadyz é a readiness probe. Aqui entrariam as checagens de
// dependências (banco, cache, fila) antes de aceitar tráfego.
func (app *application) handleReadyz(w http.ResponseWriter, r *http.Request) {
	app.writeJSON(w, r, http.StatusOK, map[string]any{"status": "ready"})
}

// handleNotFound atende tudo que não casou com uma rota registrada.
//
// O padrão "/" captura qualquer requisição, inclusive uma rota conhecida
// chamada com o método errado — e, por capturá-la, impede o ServeMux de
// devolver o 405 que ele geraria sozinho. Por isso a distinção é feita aqui.
func (app *application) handleNotFound(w http.ResponseWriter, r *http.Request) {
	if rotaConhecida(r.URL.Path) {
		// O cabeçalho Allow é obrigatório numa resposta 405 (RFC 9110).
		w.Header().Set("Allow", http.MethodGet)
		app.writeJSON(w, r, http.StatusMethodNotAllowed, map[string]any{
			"erro":            "método não permitido para este recurso",
			"metodo":          r.Method,
			"path":            r.URL.Path,
			"metodos_aceitos": []string{http.MethodGet},
		})
		return
	}

	app.writeJSON(w, r, http.StatusNotFound, map[string]any{
		"erro": "recurso não encontrado",
		"path": r.URL.Path,
	})
}

func (app *application) writeJSON(w http.ResponseWriter, r *http.Request, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// O status já foi escrito; só resta registrar.
		app.logger.Error("falha ao serializar resposta",
			slog.String("path", r.URL.Path),
			slog.Any("erro", err),
		)
	}
}
