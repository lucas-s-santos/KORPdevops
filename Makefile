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

.PHONY: ajuda provisionar rede up down build logs testar carga metricas limpar lint fmt

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
