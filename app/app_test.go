package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestApp cria a aplicação com relógio fixo e log silenciado, para que os
// testes sejam determinísticos.
func newTestApp(t *testing.T, fixed time.Time) *application {
	t.Helper()
	cfg := config{ServiceName: "http-server-projeto-korp"}
	return &application{
		cfg:     cfg,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics: newMetrics(cfg.ServiceName),
		now:     func() time.Time { return fixed },
		started: fixed.Add(-time.Minute),
	}
}

func TestProjetoKorpRetornaContratoEsperado(t *testing.T) {
	// Horário propositalmente em -03:00 para provar a conversão para UTC.
	fixed := time.Date(2026, 9, 5, 12, 30, 45, 0, time.FixedZone("BRT", -3*3600))
	app := newTestApp(t, fixed)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projeto-korp", nil)
	app.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, esperado application/json", ct)
	}

	var got korpResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("resposta não é JSON válido: %v (corpo: %s)", err, rec.Body.String())
	}
	if got.Nome != "Projeto Korp" {
		t.Errorf(`nome = %q, esperado "Projeto Korp"`, got.Nome)
	}
	if got.Horario != "2026-09-05T15:30:45Z" {
		t.Errorf("horario = %q, esperado 2026-09-05T15:30:45Z (UTC)", got.Horario)
	}
}

func TestHorarioResolvidoACadaRequisicao(t *testing.T) {
	app := newTestApp(t, time.Now())
	// Relógio que avança um segundo a cada leitura.
	tick := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	app.now = func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}
	handler := app.routes()

	horarios := make(map[string]bool)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projeto-korp", nil))
		var got korpResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("json inválido: %v", err)
		}
		horarios[got.Horario] = true
	}
	if len(horarios) != 3 {
		t.Errorf("esperava 3 horários distintos, obtive %d: %v", len(horarios), horarios)
	}
}

func TestMetodoNaoPermitido(t *testing.T) {
	app := newTestApp(t, time.Now())
	rec := httptest.NewRecorder()
	app.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/projeto-korp", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, esperado 405", rec.Code)
	}
	// A RFC 9110 exige o cabeçalho Allow numa resposta 405.
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Errorf("Allow = %q, esperado %q", allow, http.MethodGet)
	}
}

func TestRotaDesconhecidaRetorna404(t *testing.T) {
	app := newTestApp(t, time.Now())
	rec := httptest.NewRecorder()
	app.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nao-existe", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, esperado 404", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	app := newTestApp(t, time.Now())
	rec := httptest.NewRecorder()
	app.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json inválido: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, esperado ok", body["status"])
	}
}

func TestMetricsExpoeContadorEDisponibilidade(t *testing.T) {
	app := newTestApp(t, time.Now())
	handler := app.routes()

	for i := 0; i < 2; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/projeto-korp", nil))
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	esperados := []string{
		`korp_http_requests_total{method="GET",path="/projeto-korp",status="200"} 2`,
		"korp_service_up 1",
		"korp_http_request_duration_seconds_bucket",
		"korp_build_info",
	}
	for _, esperado := range esperados {
		if !strings.Contains(body, esperado) {
			t.Errorf("métrica ausente em /metrics: %s", esperado)
		}
	}
}

func TestNormalizeRouteLimitaCardinalidade(t *testing.T) {
	casos := map[string]string{
		"/projeto-korp":   "/projeto-korp",
		"/healthz":        "/healthz",
		"/metrics":        "/metrics",
		"/qualquer/coisa": "outros",
		"/../etc/passwd":  "outros",
		"":                "outros",
	}
	for entrada, esperado := range casos {
		if got := normalizeRoute(entrada); got != esperado {
			t.Errorf("normalizeRoute(%q) = %q, esperado %q", entrada, got, esperado)
		}
	}
}

func TestHealthcheckDoBinario(t *testing.T) {
	// O HEALTHCHECK do Docker chama o próprio binário com -healthcheck.
	// Este teste cobre os dois desfechos possíveis dessa chamada.
	app := newTestApp(t, time.Now())
	servidor := httptest.NewServer(app.routes())
	defer servidor.Close()

	endereco := strings.TrimPrefix(servidor.URL, "http://")

	if codigo := runHealthcheck(endereco); codigo != 0 {
		t.Errorf("healthcheck contra servidor saudável = %d, esperado 0", codigo)
	}

	servidor.Close()
	if codigo := runHealthcheck(endereco); codigo != 1 {
		t.Errorf("healthcheck contra servidor fora do ar = %d, esperado 1", codigo)
	}

	if codigo := runHealthcheck("endereco-invalido"); codigo != 1 {
		t.Errorf("healthcheck com endereço inválido = %d, esperado 1", codigo)
	}
}

func TestConfiguracaoPorAmbiente(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":9999")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SHUTDOWN_TIMEOUT", "30s")

	cfg := loadConfig()

	if cfg.ListenAddr != ":9999" {
		t.Errorf("ListenAddr = %q, esperado :9999", cfg.ListenAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, esperado debug", cfg.LogLevel)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout = %v, esperado 30s", cfg.ShutdownTimeout)
	}
}

func TestConfiguracaoUsaPadroesQuandoAusente(t *testing.T) {
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "valor-invalido")

	cfg := loadConfig()

	if cfg.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, esperado o padrão :8080", cfg.ListenAddr)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %v, esperado o padrão 10s", cfg.ShutdownTimeout)
	}
}

func TestPanicViraErro500(t *testing.T) {
	app := newTestApp(t, time.Now())
	explode := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("falha proposital")
	})
	handler := app.recoverPanic(explode)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/projeto-korp", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, esperado 500", rec.Code)
	}
}

func TestClientIPPrefereCabecalhosDoProxy(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/projeto-korp", nil)
	req.RemoteAddr = "172.28.0.5:54321"

	if got := clientIP(req); got != "172.28.0.5:54321" {
		t.Errorf("sem cabeçalhos, clientIP = %q", got)
	}

	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := clientIP(req); got != "203.0.113.9" {
		t.Errorf("com X-Forwarded-For, clientIP = %q", got)
	}

	req.Header.Set("X-Real-IP", "198.51.100.7")
	if got := clientIP(req); got != "198.51.100.7" {
		t.Errorf("X-Real-IP deveria ter precedência, clientIP = %q", got)
	}
}
