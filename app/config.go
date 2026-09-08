package main

import (
	"os"
	"strconv"
	"time"
)

// config concentra tudo que é ajustável por ambiente. Nada de valor mágico
// espalhado pelo código: a mesma imagem roda em dev e em produção mudando só
// variáveis de ambiente (12-factor app).
type config struct {
	ListenAddr      string
	LogLevel        string
	ShutdownTimeout time.Duration
	ServiceName     string
}

func loadConfig() config {
	return config{
		ListenAddr:      getenv("LISTEN_ADDR", ":8080"),
		LogLevel:        getenv("LOG_LEVEL", "info"),
		ShutdownTimeout: getenvDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
		ServiceName:     getenv("SERVICE_NAME", "http-server-projeto-korp"),
	}
}

func getenv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	// Aceita também um número puro interpretado como segundos.
	if s, err := strconv.Atoi(v); err == nil {
		return time.Duration(s) * time.Second
	}
	return fallback
}
