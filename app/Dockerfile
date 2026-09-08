# syntax=docker/dockerfile:1

# ---------------------------------------------------------------------------
# Estágio 1 — build
# ---------------------------------------------------------------------------
# O toolchain do Go (~800 MB) fica só aqui. A imagem final leva apenas o
# binário estático, o que reduz tamanho e superfície de ataque.
FROM golang:1.23-alpine AS builder

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /src

# As dependências entram numa camada própria: enquanto go.mod/go.sum não
# mudarem, o Docker reaproveita o cache e não baixa nada de novo. Mudar um
# arquivo .go passa a custar segundos, não minutos.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download && go mod verify

COPY . .

# Os testes rodam dentro do build: uma imagem só é gerada se a suíte passar.
#
# Sem `-race` aqui de propósito — o detector de corrida exige CGO e um
# toolchain C, que a imagem alpine não traz, e habilitá-lo quebraria a
# compilação estática. O `-race` roda no CI, sobre glibc, onde é confiável.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go vet ./... && go test ./... -count=1

# CGO_ENABLED=0    -> binário estático, roda em `scratch` (sem libc).
# -mod=readonly    -> falha se go.mod/go.sum precisarem mudar (reprodutível).
# -trimpath        -> remove caminhos absolutos da máquina de build.
# -ldflags "-s -w" -> descarta tabela de símbolos e DWARF (binário menor).
# -X main.version  -> injeta versão/commit/data, que reaparecem na métrica
#                     korp_build_info e no painel do Grafana.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -mod=readonly \
      -trimpath \
      -ldflags="-s -w \
        -X main.version=${VERSION} \
        -X main.commit=${COMMIT} \
        -X main.buildDate=${BUILD_DATE}" \
      -o /out/http-server-projeto-korp .

# Um usuário não-root precisa existir em /etc/passwd, e `scratch` não traz
# o arquivo. Criado aqui e copiado para a imagem final.
RUN echo 'korp:x:65532:65532:korp:/nonexistent:/sbin/nologin' > /out/passwd

# ---------------------------------------------------------------------------
# Estágio 2 — runtime
# ---------------------------------------------------------------------------
# `scratch` é uma imagem vazia. Sem shell, sem gerenciador de pacotes, sem
# libc: não há o que um invasor executar mesmo obtendo execução de comando.
FROM scratch AS runtime

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /out/passwd /etc/passwd
COPY --from=builder /out/http-server-projeto-korp /http-server-projeto-korp

# Rodar como não-root é a defesa mais barata contra escape de container.
USER 65532:65532

ENV LISTEN_ADDR=":8080" \
    LOG_LEVEL="info" \
    SERVICE_NAME="http-server-projeto-korp" \
    TZ="UTC"

EXPOSE 8080

# O próprio binário faz o healthcheck: não existe curl nem wget em `scratch`,
# e a flag -healthcheck evita inchar a imagem só por causa disso.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/http-server-projeto-korp", "-healthcheck"]

# Forma exec: o binário vira o PID 1 de verdade e recebe o SIGTERM do
# `docker stop`, o que aciona o encerramento gracioso. Na forma shell, quem
# receberia o sinal seria um /bin/sh que não o repassa.
ENTRYPOINT ["/http-server-projeto-korp"]

LABEL org.opencontainers.image.title="http-server-projeto-korp" \
      org.opencontainers.image.description="Serviço HTTP em Go com métricas Prometheus" \
      org.opencontainers.image.licenses="MIT"
