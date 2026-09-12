#!/usr/bin/env bash
# ===========================================================================
# Teste de aceitação do ambiente Projeto Korp
#
# Verifica, do lado de fora, tudo que o desafio pede. Sai com código 0 se
# todas as checagens passarem e 1 caso contrário — assim serve tanto para a
# demonstração quanto para um pipeline de CI.
#
#   bash scripts/teste-aceitacao.sh
# ===========================================================================
set -uo pipefail

HOST="${HOST:-localhost}"
PORTA_HTTP="${PORTA_HTTP:-80}"
PORTA_PROM="${PORTA_PROM:-9090}"
PORTA_GRAFANA="${PORTA_GRAFANA:-3000}"
PORTA_AM="${PORTA_AM:-9093}"
GRAFANA_USER="${GRAFANA_USER:-admin}"
GRAFANA_PASSWORD="${GRAFANA_PASSWORD:-admin}"

BASE="http://${HOST}:${PORTA_HTTP}"

if [[ -t 1 ]]; then
  VERDE=$'\033[32m'; VERMELHO=$'\033[31m'; AMARELO=$'\033[33m'
  AZUL=$'\033[36m'; NEGRITO=$'\033[1m'; RESET=$'\033[0m'
else
  VERDE=""; VERMELHO=""; AMARELO=""; AZUL=""; NEGRITO=""; RESET=""
fi

TOTAL=0
FALHAS=0

titulo() {
  printf '\n%s%s%s\n' "${NEGRITO}${AZUL}" "$1" "${RESET}"
  printf '%s\n' "------------------------------------------------------------"
}

verificar() {
  local descricao="$1"; shift
  TOTAL=$((TOTAL + 1))
  if "$@" >/dev/null 2>&1; then
    printf '  %s[ OK ]%s %s\n' "$VERDE" "$RESET" "$descricao"
    return 0
  fi
  printf '  %s[FALHA]%s %s\n' "$VERMELHO" "$RESET" "$descricao"
  FALHAS=$((FALHAS + 1))
  return 1
}

# --------------------------------------------------------------------------
titulo "1. Serviço HTTP através do proxy reverso"

RESPOSTA="$(curl -s --max-time 10 "${BASE}/projeto-korp" 2>/dev/null)"
CODIGO="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "${BASE}/projeto-korp" 2>/dev/null)"

printf '  %s$ curl %s/projeto-korp%s\n' "$AMARELO" "$BASE" "$RESET"
printf '  %s\n\n' "${RESPOSTA:-<sem resposta>}"

verificar "GET /projeto-korp devolve HTTP 200" test "$CODIGO" = "200"
verificar 'campo "nome" igual a "Projeto Korp"' \
  grep -qE '"nome"[[:space:]]*:[[:space:]]*"Projeto Korp"' <<<"$RESPOSTA"
verificar 'campo "horario" em UTC no formato RFC 3339' \
  grep -qE '"horario"[[:space:]]*:[[:space:]]*"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z"' <<<"$RESPOSTA"

HORA1="$(grep -oE '"horario"[^,}]*' <<<"$RESPOSTA")"
sleep 2
HORA2="$(curl -s --max-time 10 "${BASE}/projeto-korp" | grep -oE '"horario"[^,}]*')"
verificar "horário resolvido dinamicamente a cada requisição" test "$HORA1" != "$HORA2"

verificar "rota desconhecida devolve 404" \
  bash -c "[[ \$(curl -s -o /dev/null -w '%{http_code}' ${BASE}/rota-inexistente) == 404 ]]"

# --------------------------------------------------------------------------
titulo "2. Isolamento de rede"

verificar "a aplicação NÃO publica portas no host" \
  bash -c '! docker port http-server-projeto-korp 2>/dev/null | grep -q .'
verificar "porta 8080 do host não responde (só o NGINX é exposto)" \
  bash -c "! curl -s --max-time 3 http://${HOST}:8080/projeto-korp >/dev/null 2>&1"
verificar "rede korp-net existe e é do tipo bridge" \
  bash -c "docker network inspect korp-net --format '{{.Driver}}' | grep -qx bridge"
verificar "os containers estão conectados à korp-net" \
  bash -c "docker network inspect korp-net --format '{{range .Containers}}{{.Name}} {{end}}' | grep -q http-server-projeto-korp"

# --------------------------------------------------------------------------
titulo "3. Métricas no padrão Prometheus"

METRICAS="$(curl -s --max-time 10 "${BASE}/metrics" 2>/dev/null)"

verificar "endpoint /metrics responde" test -n "$METRICAS"
verificar "korp_http_requests_total (volume de requisições)" \
  grep -q 'korp_http_requests_total' <<<"$METRICAS"
verificar "korp_service_up (disponibilidade)" \
  grep -q 'korp_service_up 1' <<<"$METRICAS"
verificar "korp_http_request_duration_seconds (latência)" \
  grep -q 'korp_http_request_duration_seconds_bucket' <<<"$METRICAS"
verificar "korp_build_info (versão em execução)" \
  grep -q 'korp_build_info' <<<"$METRICAS"

# --------------------------------------------------------------------------
titulo "4. Prometheus"

ALVOS="$(curl -s --max-time 10 "http://${HOST}:${PORTA_PROM}/api/v1/targets?state=active" 2>/dev/null)"

verificar "API do Prometheus responde" grep -q '"status":"success"' <<<"$ALVOS"

# Em vez de garimpar o JSON de /targets sem jq, pergunto ao proprio
# Prometheus: `up{job="X"}` vale 1 exatamente quando o alvo respondeu.
consultar() {
  curl -s --max-time 10 --get "http://${HOST}:${PORTA_PROM}/api/v1/query" \
    --data-urlencode "query=$1"
}

alvo_no_ar() {
  consultar "up{job=\"$1\"}" | grep -q '"1"]'
}

for job in http-server-projeto-korp nginx prometheus; do
  verificar "alvo '${job}' esta sendo coletado (up == 1)" alvo_no_ar "$job"
done

probe_ok() {
  consultar 'probe_success{instance="http://nginx:80/projeto-korp"}' | grep -q '"1"]'
}
verificar "probe fim-a-fim pelo NGINX esta com sucesso" probe_ok

tem_contador() {
  consultar 'sum(korp_http_requests_total)' | grep -q '"result":\[{'
}
verificar "contador de requisicoes ja tem dados no Prometheus" tem_contador


REGRAS="$(curl -s --max-time 10 "http://${HOST}:${PORTA_PROM}/api/v1/rules" 2>/dev/null)"
verificar "regras de alerta carregadas" grep -q 'ServicoIndisponivel' <<<"$REGRAS"

# --------------------------------------------------------------------------
titulo "5. Cadeia de alertas"

verificar "Alertmanager responde" \
  bash -c "curl -sf --max-time 10 http://${HOST}:${PORTA_AM}/-/healthy >/dev/null"

# Esta é a checagem que pega o erro mais comum de quem monta alerta: regra
# escrita, Alertmanager de pé, e os dois sem se falarem porque faltou o bloco
# `alerting:` no prometheus.yml. Sem ela, tudo "parece" funcionar.
AM="$(curl -s --max-time 10 "http://${HOST}:${PORTA_PROM}/api/v1/alertmanagers" 2>/dev/null)"
verificar "Prometheus está apontando para o Alertmanager" \
  grep -q 'alertmanager:9093' <<<"$AM"

verificar "regras de taxa de queima carregadas" \
  grep -q 'OrcamentoDeErroQueimandoRapido' <<<"$REGRAS"
verificar "registros de SLO carregados" \
  grep -q 'korp:queima_orcamento:rate1h' <<<"$REGRAS"

# O watchdog dispara sempre, de propósito. Se ele chegou até o notificador, a
# corrente inteira está provada — não apenas as duas pontas.
watchdog_entregue() {
  consultar 'korp_alertas_recebidos_total{alerta="PipelineDeAlertasViva"}' \
    | grep -qE '"[1-9][0-9]*"\]'
}
verificar "watchdog percorreu Prometheus -> Alertmanager -> notificador" \
  watchdog_entregue

ALERTAS_AM="$(curl -s --max-time 10 "http://${HOST}:${PORTA_AM}/api/v2/alerts" 2>/dev/null)"
verificar "Alertmanager registrou o alerta de watchdog" \
  grep -q 'PipelineDeAlertasViva' <<<"$ALERTAS_AM"

# As checagens internas passam pelo container do NGINX, que já está na
# korp-net e cuja imagem (alpine) garante o wget do busybox. Depender do
# cliente HTTP embutido em CADA imagem quebraria numa troca de base sem aviso.
interno() { docker exec nginx wget -q -O- "$1" 2>/dev/null; }

verificar "notificador responde no healthz" \
  bash -c "docker exec nginx wget -q -O- http://notificador:9094/healthz | grep -q ok"
verificar "notificador expõe korp_alertas_recebidos_total" \
  bash -c "docker exec nginx wget -q -O- http://notificador:9094/metrics | grep -q korp_alertas_recebidos_total"

# --------------------------------------------------------------------------
titulo "6. Logs (Loki + Promtail)"

loki_pronto() {
  for _ in 1 2 3 4 5 6 7 8 9 10; do
    if docker exec nginx wget -q -O- http://loki:3100/ready 2>/dev/null | grep -q ready; then
      return 0
    fi
    sleep 3
  done
  return 1
}
verificar "Loki está pronto" loki_pronto

# Loki e Promtail não têm healthcheck de container (a imagem não garante um
# cliente HTTP embutido). A prontidão é provada aqui, por quem tem como
# provar: o Prometheus coletando os dois com sucesso.
for job in loki promtail; do
  verificar "alvo '${job}' está sendo coletado (up == 1)" alvo_no_ar "$job"
done

promtail_entregou() {
  consultar 'sum(promtail_sent_entries_total)' | grep -qE '"[1-9][0-9]*"\]'
}
verificar "Promtail já entregou linhas ao Loki" promtail_entregou

# `values` só aparece no JSON quando há entradas de verdade; procurar por
# `result` passaria também com uma resposta vazia.
LOGS_APP="$(interno 'http://loki:3100/loki/api/v1/query_range?query=%7Bservico%3D%22http-server-projeto-korp%22%7D&limit=5')"
verificar "Loki devolve logs da aplicação" grep -q '"values"' <<<"$LOGS_APP"
verificar "os logs chegam rotulados pelo nome do serviço" \
  grep -q 'http-server-projeto-korp' <<<"$LOGS_APP"

# --------------------------------------------------------------------------
titulo "7. Grafana"

SAUDE="$(curl -s --max-time 10 "http://${HOST}:${PORTA_GRAFANA}/api/health" 2>/dev/null)"
verificar "Grafana está saudável" grep -q '"database": *"ok"' <<<"$SAUDE"

DASH="$(curl -s --max-time 10 -u "${GRAFANA_USER}:${GRAFANA_PASSWORD}" \
  "http://${HOST}:${PORTA_GRAFANA}/api/dashboards/uid/projeto-korp" 2>/dev/null)"
verificar "dashboard 'projeto-korp' provisionado" grep -q '"uid": *"projeto-korp"' <<<"$DASH"

DS="$(curl -s --max-time 10 -u "${GRAFANA_USER}:${GRAFANA_PASSWORD}" \
  "http://${HOST}:${PORTA_GRAFANA}/api/datasources/uid/prometheus" 2>/dev/null)"
verificar "datasource Prometheus provisionado" grep -q '"type": *"prometheus"' <<<"$DS"

DASH_SLO="$(curl -s --max-time 10 -u "${GRAFANA_USER}:${GRAFANA_PASSWORD}" \
  "http://${HOST}:${PORTA_GRAFANA}/api/dashboards/uid/projeto-korp-slo" 2>/dev/null)"
verificar "dashboard 'projeto-korp-slo' provisionado" \
  grep -q '"uid": *"projeto-korp-slo"' <<<"$DASH_SLO"

DS_LOKI="$(curl -s --max-time 10 -u "${GRAFANA_USER}:${GRAFANA_PASSWORD}" \
  "http://${HOST}:${PORTA_GRAFANA}/api/datasources/uid/loki" 2>/dev/null)"
verificar "datasource Loki provisionado" grep -q '"type": *"loki"' <<<"$DS_LOKI"

# --------------------------------------------------------------------------
titulo "8. Saúde dos containers"

CONTAINERS="http-server-projeto-korp nginx nginx-exporter blackbox-exporter prometheus grafana alertmanager notificador loki promtail docker-socket-proxy"
for c in $CONTAINERS; do
  verificar "container ${c} em execução" \
    bash -c "docker inspect -f '{{.State.Running}}' ${c} 2>/dev/null | grep -qx true"
done

verificar "healthcheck da aplicação está 'healthy'" \
  bash -c "docker inspect -f '{{.State.Health.Status}}' http-server-projeto-korp 2>/dev/null | grep -qx healthy"
verificar "aplicação roda como usuário não-root" \
  bash -c "docker inspect -f '{{.Config.User}}' http-server-projeto-korp | grep -qx '65532:65532'"

verificar "notificador roda como usuário não-root" \
  bash -c "docker inspect -f '{{.Config.User}}' notificador | grep -qx '65534:65534'"

# O Promtail precisa da API do Docker, mas NÃO deve ter o socket montado:
# quem fala direto com o socket tem, na prática, root no host. Ele conversa
# com o docker-socket-proxy, que só libera leitura de containers.
verificar "Promtail NÃO tem o socket do Docker montado" \
  bash -c "! docker inspect -f '{{range .Mounts}}{{.Source}} {{end}}' promtail | grep -q docker.sock"

# --------------------------------------------------------------------------
printf '\n%s\n' "============================================================"
if [[ $FALHAS -eq 0 ]]; then
  printf '%s TODAS AS %d VERIFICAÇÕES PASSARAM%s\n' "${NEGRITO}${VERDE}" "$TOTAL" "$RESET"
  printf '%s\n' "============================================================"
  exit 0
fi
printf '%s %d de %d verificações FALHARAM%s\n' "${NEGRITO}${VERMELHO}" "$FALHAS" "$TOTAL" "$RESET"
printf '%s\n' "============================================================"
exit 1
