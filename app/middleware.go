package main

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"
)

// responseRecorder captura status e bytes escritos, que o http.ResponseWriter
// não expõe depois que a resposta sai.
type responseRecorder struct {
	http.ResponseWriter
	status  int
	written int
}

func (rec *responseRecorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
		rec.ResponseWriter.WriteHeader(status)
	}
}

func (rec *responseRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		// Escrever sem WriteHeader implica 200, igual à stdlib.
		rec.status = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.written += n
	return n, err
}

// instrument alimenta as métricas Prometheus a cada requisição.
func (app *application) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		route := normalizeRoute(r.URL.Path)

		app.metrics.requestsInFlight.Inc()
		defer app.metrics.requestsInFlight.Dec()

		rec := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		status := strconv.Itoa(rec.status)

		app.metrics.requestsTotal.WithLabelValues(r.Method, route, status).Inc()
		app.metrics.requestDuration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
		app.metrics.responseSize.WithLabelValues(r.Method, route).Observe(float64(rec.written))
	})
}

// logRequests grava uma linha estruturada por requisição (access log).
func (app *application) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		app.logger.Info("requisicao",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int("bytes", rec.written),
			slog.Duration("duracao", time.Since(start)),
			// Preenchido pelo NGINX; sem ele todo acesso pareceria vir
			// do IP do container do proxy.
			slog.String("client_ip", clientIP(r)),
			slog.String("user_agent", r.UserAgent()),
		)
	})
}

// recoverPanic transforma um pânico em 500 e mantém o processo de pé.
func (app *application) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				app.logger.Error("panic recuperado",
					slog.Any("erro", err),
					slog.String("stack", string(debug.Stack())),
				)
				w.Header().Set("Connection", "close")
				http.Error(w, `{"erro":"erro interno"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// rotasConhecidas é a lista fechada de caminhos que o serviço atende.
// Serve a dois propósitos: limitar a cardinalidade das métricas e distinguir
// "rota inexistente" de "rota existente chamada com o método errado".
var rotasConhecidas = map[string]struct{}{
	"/projeto-korp": {},
	"/healthz":      {},
	"/readyz":       {},
	"/metrics":      {},
}

func rotaConhecida(path string) bool {
	_, ok := rotasConhecidas[path]
	return ok
}

// normalizeRoute mapeia o caminho para um conjunto fechado de valores.
//
// Sem isso, uma varredura de URLs aleatórias criaria uma série temporal nova
// por caminho e explodiria a cardinalidade do Prometheus — o modo mais comum
// de derrubar um servidor de métricas.
func normalizeRoute(path string) string {
	if rotaConhecida(path) {
		return path
	}
	return "outros"
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return ip
	}
	return r.RemoteAddr
}
