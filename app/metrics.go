package main

import (
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// metrics agrupa os coletores da aplicação.
//
// Uso um registry próprio em vez do prometheus.DefaultRegisterer para ter
// controle explícito do que é exposto (e para os testes poderem criar um
// registry limpo, sem colisão de métricas já registradas globalmente).
type metrics struct {
	registry *prometheus.Registry

	// Volume de requisições (requisito obrigatório).
	requestsTotal *prometheus.CounterVec

	// Latência: permite calcular p95/p99 e alimentar SLOs.
	requestDuration *prometheus.HistogramVec

	// Requisições simultâneas em processamento (saturação).
	requestsInFlight prometheus.Gauge

	// Bytes devolvidos por resposta.
	responseSize *prometheus.HistogramVec

	// Disponibilidade auto-reportada pelo serviço (requisito obrigatório).
	// Vale 1 enquanto o processo está saudável. Combinada com o `up` do
	// Prometheus e com o probe do Blackbox, fecha as três camadas:
	// processo vivo -> alvo raspável -> caminho fim-a-fim pelo NGINX.
	serviceUp prometheus.Gauge

	// Metadado de build no padrão *_build_info (valor sempre 1, informação
	// nas labels). Serve para correlacionar comportamento com versão.
	buildInfo *prometheus.GaugeVec
}

func newMetrics(serviceName string) *metrics {
	reg := prometheus.NewRegistry()

	m := &metrics{
		registry: reg,
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "korp_http_requests_total",
				Help: "Total de requisições HTTP processadas, por método, rota e status.",
			},
			[]string{"method", "path", "status"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name: "korp_http_request_duration_seconds",
				Help: "Duração das requisições HTTP em segundos.",
				// Buckets ajustados a um serviço rápido (ordem de ms).
				// O default do client_golang começa em 5ms e vai até 10s,
				// o que desperdiça resolução justamente onde ficam as
				// respostas deste serviço.
				Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
			},
			[]string{"method", "path"},
		),
		requestsInFlight: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "korp_http_requests_in_flight",
				Help: "Requisições HTTP sendo processadas neste instante.",
			},
		),
		responseSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "korp_http_response_size_bytes",
				Help:    "Tamanho das respostas HTTP em bytes.",
				Buckets: prometheus.ExponentialBuckets(32, 4, 6),
			},
			[]string{"method", "path"},
		),
		serviceUp: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "korp_service_up",
				Help: "1 enquanto o serviço está saudável e pronto para atender.",
			},
		),
		buildInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "korp_build_info",
				Help: "Metadados de build do serviço (valor constante 1).",
			},
			[]string{"service", "version", "commit", "build_date", "go_version"},
		),
	}

	m.serviceUp.Set(1)
	m.buildInfo.WithLabelValues(serviceName, version, commit, buildDate, runtime.Version()).Set(1)

	reg.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.requestsInFlight,
		m.responseSize,
		m.serviceUp,
		m.buildInfo,
		// Coletores padrão: memória/GC/goroutines do runtime Go e
		// CPU/FDs do processo. Baratos e muito úteis em diagnóstico.
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return m
}
