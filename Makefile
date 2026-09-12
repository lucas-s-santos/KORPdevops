# ===========================================================================
# Projeto Korp — atalhos de operação
#
#   make provisionar   -> tudo via Ansible (caminho oficial do desafio)
#   make up            -> sobe a stack manualmente com Docker Compose
#   make testar        -> executa o teste de aceitação
# ===========================================================================

SHELL          := /bin/bash
REDE           := korp-net
SUBNET         := 172.28.0.0/16
GATEWAY        := 172.28.0.1
COMPOSE        := docker compose
PLAYBOOK       := ansible/site.yml
INVENTARIO     := ansible/inventory.ini
URL            := http://localhost/projeto-korp

.DEFAULT_GOAL := ajuda

.PHONY: ajuda provisionar rede up down build logs testar carga metricas limpar lint fmt \n        alertas slo notificacoes incidente fim-incidente logs-loki

ajuda: ## Mostra esta ajuda
	@echo ""
	@echo "  Projeto Korp — comandos disponíveis"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
	@echo ""

# ---------------------------------------------------------------------------
# Caminho oficial: um comando provisiona tudo
# ---------------------------------------------------------------------------
provisionar: ## Provisiona o ambiente inteiro com Ansible
	ansible-playbook -i $(INVENTARIO) $(PLAYBOOK)

provisionar-verboso: ## Igual ao anterior, com saída detalhada
	ansible-playbook -i $(INVENTARIO) $(PLAYBOOK) -vv

simular: ## Simula a execução do playbook sem alterar nada (dry-run)
	ansible-playbook -i $(INVENTARIO) $(PLAYBOOK) --check --diff

# ---------------------------------------------------------------------------
# Caminho manual, para demonstrar cada etapa isoladamente
# ---------------------------------------------------------------------------
rede: ## Cria a rede Docker bridge (idempotente)
	@docker network inspect $(REDE) >/dev/null 2>&1 \
	  && echo "rede $(REDE) já existe" \
	  || docker network create \
	       --driver bridge \
	       --subnet $(SUBNET) \
	       --gateway $(GATEWAY) \
	       --opt com.docker.network.bridge.name=br-korp \
	       --label projeto=projeto-korp \
	       $(REDE)

build: ## Constrói a imagem da aplicação
	$(COMPOSE) build --pull http-server-projeto-korp

up: rede ## Sobe a stack completa
	$(COMPOSE) up -d --build --wait
	@$(MAKE) --no-print-directory estado

down: ## Derruba a stack (mantém os volumes)
	$(COMPOSE) down --remove-orphans

limpar: ## Derruba tudo, inclusive volumes e a rede
	$(COMPOSE) down --remove-orphans --volumes
	-@docker network rm $(REDE) 2>/dev/null || true

estado: ## Mostra o estado dos containers
	@$(COMPOSE) ps

logs: ## Acompanha os logs de todos os serviços
	$(COMPOSE) logs -f --tail=100

logs-app: ## Acompanha apenas os logs da aplicação
	$(COMPOSE) logs -f --tail=100 http-server-projeto-korp

carga: ## Liga o gerador de tráfego (para os gráficos terem dados)
	$(COMPOSE) --profile carga up -d gerador-de-carga

sem-carga: ## Desliga o gerador de tráfego
	$(COMPOSE) --profile carga stop gerador-de-carga

# ---------------------------------------------------------------------------
# Verificação
# ---------------------------------------------------------------------------
testar: ## Executa o teste de aceitação do ambiente
	@bash scripts/teste-aceitacao.sh

curl: ## Faz a requisição do enunciado
	@echo "$$ curl $(URL)"
	@curl -s $(URL) | (command -v jq >/dev/null && jq . || cat)
	@echo ""

metricas: ## Mostra as métricas do serviço
	@curl -s http://localhost/metrics | grep -E '^korp_' | grep -v '^#'

alvos: ## Lista os alvos do Prometheus e sua saúde
	@curl -s 'http://localhost:9090/api/v1/targets?state=active' \
	  | (command -v jq >/dev/null \
	     && jq -r '.data.activeTargets[] | "\(.labels.job)\t\(.health)\t\(.scrapeUrl)"' \
	     || cat)

# ---------------------------------------------------------------------------
# Alertas, SLO e logs
# ---------------------------------------------------------------------------
alertas: ## Lista os alertas ativos no Alertmanager
	@curl -s http://localhost:9093/api/v2/alerts \
	  | (command -v jq >/dev/null \
	     && jq -r '.[] | "\(.labels.severidade)\t\(.labels.alertname)\t\(.status.state)"' \
	     || cat)

notificacoes: ## Mostra os alertas que chegaram ao notificador
	@docker exec nginx wget -q -O- http://notificador:9094/metrics \
	  | grep -E '^korp_(alertas|notificacoes)' \
	  || echo "  nenhum alerta recebido ainda"

slo: ## Mostra a disponibilidade, o orçamento de erro e a taxa de queima
	@for consulta in \
	    "disponibilidade 30d|korp:disponibilidade:ratio_rate30d" \
	    "orçamento restante|korp:orcamento_erro_restante:ratio" \
	    "taxa de queima 1h|korp:queima_orcamento:rate1h" \
	    "taxa de queima 6h|korp:queima_orcamento:rate6h"; do \
	  rotulo="$${consulta%%|*}"; expressao="$${consulta##*|}"; \
	  valor=$$(curl -s -G --data-urlencode "query=$$expressao" \
	            http://localhost:9090/api/v1/query \
	          | grep -oE '"value":\[[0-9.]+,"[^"]*"' \
	          | grep -oE '"[^"]*"$$' | tr -d '"'); \
	  printf '  %-22s %s\n' "$$rotulo" "$${valor:-sem dados}"; \
	done

logs-loki: ## Últimas linhas de log da aplicação, lidas do Loki
	@docker exec nginx wget -q -O- \
	  'http://loki:3100/loki/api/v1/query_range?query=%7Bservico%3D%22http-server-projeto-korp%22%7D&limit=20' \
	  | (command -v jq >/dev/null && jq -r '.data.result[].values[][1]' || cat)

# Um incidente de verdade é a única forma honesta de provar que a cadeia de
# alertas funciona. Parar o container dispara ServicoIndisponivel (for: 1m) e
# CaminhoFimAFimFalhando — e a regra de inibição do Alertmanager deve fazer
# com que apenas o PRIMEIRO vire notificação.
incidente: ## Derruba a aplicação de propósito, para ver os alertas dispararem
	@echo "parando a aplicação..."
	@docker stop http-server-projeto-korp >/dev/null
	@echo ""
	@echo "  ~1min  -> ServicoIndisponivel fica 'firing'   (make alertas)"
	@echo "  ~10s   -> depois disso sai a notificação       (make notificacoes)"
	@echo "  então  -> make fim-incidente para restaurar"

fim-incidente: ## Religa a aplicação depois do incidente simulado
	@docker start http-server-projeto-korp >/dev/null
	@echo "aplicação de volta — o alerta deve resolver em ~1min"

# ---------------------------------------------------------------------------
# Desenvolvimento
# ---------------------------------------------------------------------------
teste-go: ## Roda a suíte de testes Go
	cd app && go test ./... -race -count=1 -cover

fmt: ## Formata o código Go
	cd app && gofmt -w . && go vet ./...

lint: ## Valida os arquivos de configuração
	@echo "== docker compose =="
	@$(COMPOSE) config --quiet && echo "  compose ok"
	@echo "== ansible =="
	@ansible-playbook -i $(INVENTARIO) $(PLAYBOOK) --syntax-check
	@command -v ansible-lint >/dev/null && ansible-lint ansible/ || echo "  (ansible-lint não instalado)"
	@echo "== prometheus =="
	@docker run --rm -v "$$PWD/prometheus:/p" --entrypoint promtool prom/prometheus:v2.55.1 \
	   check config /p/prometheus.yml
	@docker run --rm -v "$$PWD/prometheus:/p" --entrypoint promtool prom/prometheus:v2.55.1 \
	   check rules /p/rules/alertas-projeto-korp.yml /p/rules/slo-projeto-korp.yml
	@echo "== alertmanager =="
	@docker run --rm -v "$$PWD/alertmanager:/a" --entrypoint amtool prom/alertmanager:v0.27.0 \
	   check-config /a/alertmanager.yml
	@echo "== notificador =="
	@docker run --rm -v "$$PWD/notificador:/n:ro" python:3.12-alpine \
	   python -m py_compile /n/notificador.py && echo "  notificador.py ok"
