package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// runHealthcheck faz uma requisição a /healthz no próprio container e sai com
// 0 (saudável) ou 1 (não saudável).
//
// A imagem final é `scratch`: não existe curl nem wget lá dentro. Em vez de
// inchar a imagem só para ter um HEALTHCHECK, o próprio binário faz o papel
// de cliente — `HEALTHCHECK CMD ["/http-server-projeto-korp", "-healthcheck"]`.
func runHealthcheck(addr string) int {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "endereço inválido %q: %v\n", addr, err)
		return 1
	}
	if host == "" {
		host = "127.0.0.1"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	url := fmt.Sprintf("http://%s/healthz", net.JoinHostPort(host, port))

	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck falhou: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
