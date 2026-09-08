# Projeto Korp

Serviço HTTP em Go, containerizado, atrás de um proxy reverso NGINX, com
observabilidade completa (Prometheus + Grafana) e provisionamento inteiro
automatizado por um único comando Ansible.

```
                    ┌──────────────────────── host ────────────────────────┐
                    │                                                       │
   curl :80 ───────►│  :80    NGINX  ──────┐                                │
                    │                      │  rede bridge korp-net          │
   navegador :3000 ►│  :3000  Grafana      │  (172.28.0.0/16)               │
                    │            │         ▼                                │
   navegador :9090 ►│  :9090  Prometheus ──► http-server-projeto-korp:8080  │
                    │            │              (sem porta no host)         │
                    │            ├──────────► nginx-exporter:9113           │
                    │            └──────────► blackbox-exporter:9115 ──┐    │
                    │                                                  │    │
                    │                    sonda o caminho real ─────────┘    │
                    └───────────────────────────────────────────────────────┘
```

---

## Índice

- [O que este projeto entrega](#o-que-este-projeto-entrega)
- [Pré-requisitos](#pré-requisitos)
- [Execução em um comando](#execução-em-um-comando)
- [Execução manual (passo a passo)](#execução-manual-passo-a-passo)
- [Verificando o funcionamento](#verificando-o-funcionamento)
- [Estrutura do repositório](#estrutura-do-repositório)
- [O serviço HTTP](#o-serviço-http)
- [Observabilidade](#observabilidade)
- [O playbook Ansible](#o-playbook-ansible)
- [Decisões técnicas](#decisões-técnicas)
- [Solução de problemas](#solução-de-problemas)

---

## O que este projeto entrega

| Requisito do desafio | Onde está | Status |
|---|---|---|
| Servidor HTTP em Go na porta 8080 | [app/](app/) | ✅ |
| `GET /projeto-korp` com JSON e horário UTC dinâmico | [app/app.go](app/app.go) | ✅ |
| Dockerfile com build e execução | [app/Dockerfile](app/Dockerfile) | ✅ multi-stage, imagem `scratch`, não-root |
| Instalação e configuração do Docker | [ansible/roles/docker/](ansible/roles/docker/) | ✅ Debian/Ubuntu e RHEL |
| Rede Docker bridge | [ansible/roles/rede/](ansible/roles/rede/) | ✅ sub-rede fixa |
| Compose com app (sem portas) + NGINX (80:80, volume conf.d) | [docker-compose.yml](docker-compose.yml) | ✅ |
| Proxy reverso `http-server-projeto-korp.conf` | [nginx/conf.d/](nginx/conf.d/http-server-projeto-korp.conf) | ✅ |
| `curl http://localhost:80/projeto-korp` funciona | [scripts/teste-aceitacao.sh](scripts/teste-aceitacao.sh) | ✅ |
| Métrica de disponibilidade | `korp_service_up`, `up`, `probe_success` | ✅ três camadas |
| Métrica de volume de requisições | `korp_http_requests_total` | ✅ |
| Prometheus coletando as métricas | [prometheus/prometheus.yml](prometheus/prometheus.yml) | ✅ 5 jobs |
| Grafana visualizando | [grafana/](grafana/) | ✅ |
| Dashboard de análise do serviço | [dashboard JSON](grafana/dashboards/http-server-projeto-korp-dashboard.json) | ✅ 14 painéis |
| Playbook Ansible completo | [ansible/site.yml](ansible/site.yml) | ✅ 7 roles |
| Validação HTTP com resposta no console | [ansible/roles/validacao/](ansible/roles/validacao/tasks/main.yml) | ✅ |
| **Bônus:** Grafana provisionado por arquivo | `datasources.yml`, `dashboards.yml`, JSON | ✅ zero cliques |

Extras que foram além do mínimo pedido: testes automatizados em Go rodando
dentro do build da imagem, healthchecks em todos os containers, endurecimento
de segurança dos containers (não-root, `read_only`, `cap_drop: ALL`),
blackbox-exporter para disponibilidade fim-a-fim, exporter do NGINX, regras
de alerta, rotação de logs, encerramento gracioso, limites de recursos,
script de teste de aceitação e pipeline de CI.

---

## Pré-requisitos

Uma máquina **Linux** (VM, WSL2, EC2, o que preferir) com:

- Python 3.8+ e Ansible 2.14+
- `git`, `curl`, `sudo`

O Docker **não** precisa estar instalado — o playbook instala.

### Instalando o Ansible

```bash
# Debian / Ubuntu
sudo apt update && sudo apt install -y ansible git curl

# RHEL / Rocky / Alma
sudo dnf install -y ansible-core git curl
```

### Rodando no Windows

O Ansible não roda nativamente no Windows. Use o WSL2:

```powershell
wsl --install -d Ubuntu     # depois reinicie e crie seu usuário
```

Em seguida, dentro do Ubuntu do WSL, siga o fluxo normal do Linux.
Depois de subir a stack, `http://localhost/projeto-korp` funciona também
no navegador do Windows — o WSL2 encaminha as portas automaticamente.

---

## Execução em um comando

```bash
git clone https://github.com/<seu-usuario>/projeto-korp.git
cd projeto-korp/ansible
ansible-playbook -i inventory.ini site.yml
```

Isso instala o Docker, cria a rede, constrói a imagem, sobe os seis
containers, configura o proxy e o monitoramento, faz uma requisição HTTP real
e imprime a resposta no console:

```
TASK [validacao : RESPOSTA DO SERVIÇO] *****************************************
ok: [localhost] =>
  msg: |-

    ===========================================================
     curl http://localhost:80/projeto-korp
    ===========================================================
     HTTP 200
     Content-Type: application/json; charset=utf-8

     {"nome":"Projeto Korp","horario":"2026-09-05T14:22:31Z"}

     nome    -> Projeto Korp
     horario -> 2026-09-05T14:22:31Z (UTC)

     Segunda chamada, 2s depois:
     horario -> 2026-09-05T14:22:33Z (UTC)
    ===========================================================
```

Ao final, os endereços ficam disponíveis:

| Serviço | URL | Credenciais |
|---|---|---|
| Aplicação (via NGINX) | http://localhost/projeto-korp | — |
| Métricas | http://localhost/metrics | — |
| Prometheus | http://localhost:9090 | — |
| Grafana | http://localhost:3000/d/projeto-korp | `admin` / `admin` |

> Para gerar tráfego e ver os gráficos com vida durante a demonstração:
> `make carga` (sobe um container que dispara requisições a cada segundo).

---

## Execução manual (passo a passo)

Útil para demonstrar cada etapa isoladamente numa entrevista.

```bash
# 1. Rede bridge dedicada
docker network create --driver bridge \
  --subnet 172.28.0.0/16 --gateway 172.28.0.1 korp-net

# 2. Build da imagem
docker compose build

# 3. Subir tudo e esperar os healthchecks
docker compose up -d --wait

# 4. Testar
curl http://localhost:80/projeto-korp
```

Ou, com os atalhos do Makefile:

```bash
make          # lista os comandos
make up       # cria a rede + sobe a stack
make curl     # faz a requisição do enunciado
make testar   # roda o teste de aceitação completo
make carga    # liga o gerador de tráfego
make limpar   # derruba tudo, inclusive volumes e rede
```

---

## Verificando o funcionamento

### O teste do enunciado

```bash
$ curl http://localhost:80/projeto-korp
{"nome":"Projeto Korp","horario":"2026-09-05T14:22:31Z"}
```

### O teste de aceitação completo

```bash
$ make testar
```

São mais de 30 verificações independentes, agrupadas em seis blocos: contrato
do JSON, isolamento de rede, métricas, Prometheus, Grafana e saúde dos
containers. Sai com código 1 se qualquer uma falhar, então serve também em CI.

### Provando o isolamento de rede

```bash
# A aplicação não publica nenhuma porta:
$ docker port http-server-projeto-korp
(vazio)

# Direto na 8080 do host não há ninguém:
$ curl --max-time 3 http://localhost:8080/projeto-korp
curl: (7) Failed to connect

# Mas de dentro da rede ela responde:
$ docker run --rm --network korp-net curlimages/curl \
    -s http://http-server-projeto-korp:8080/projeto-korp
{"nome":"Projeto Korp","horario":"..."}
```

---

## Estrutura do repositório

```
projeto-korp/
├── app/                              # Serviço HTTP em Go
│   ├── main.go                       #   bootstrap, sinais, shutdown gracioso
│   ├── app.go                        #   rotas e handlers
│   ├── config.go                     #   configuração por variável de ambiente
│   ├── metrics.go                    #   coletores Prometheus
│   ├── middleware.go                 #   instrumentação, log, recover
│   ├── healthcheck.go                #   modo cliente para o HEALTHCHECK
│   ├── app_test.go                   #   testes de unidade
│   └── Dockerfile                    #   multi-stage -> imagem scratch
│
├── nginx/conf.d/
│   └── http-server-projeto-korp.conf # proxy reverso (volume montado)
│
├── prometheus/
│   ├── prometheus.yml                # jobs de coleta
│   └── rules/                        # regras de alerta
│
├── blackbox/blackbox.yml             # como sondar o serviço de fora
│
├── grafana/
│   ├── provisioning/datasources/     # datasources.yml
│   ├── provisioning/dashboards/      # dashboards.yml
│   └── dashboards/*.json             # dashboard versionado
│
├── ansible/
│   ├── site.yml                      # playbook principal
│   ├── inventory.ini                 # alvo (local por padrão)
│   ├── group_vars/all.yml            # variáveis centralizadas
│   └── roles/
│       ├── docker/                   # instalação do engine
│       ├── rede/                     # rede bridge
│       ├── aplicacao/                # código + build da imagem
│       ├── nginx/                    # proxy reverso
│       ├── monitoramento/            # Prometheus, Grafana, Blackbox
│       ├── stack/                    # docker compose up
│       └── validacao/                # requisição HTTP + saída no console
│
├── scripts/teste-aceitacao.sh        # verificação fim-a-fim
├── docker-compose.yml
├── Makefile
└── docs/
    ├── DECISOES-TECNICAS.md          # o porquê de cada escolha
    └── ROTEIRO-ENTREVISTA.md         # perguntas prováveis e respostas
```

---

## O serviço HTTP

### Endpoints

| Método | Rota | Resposta |
|---|---|---|
| GET | `/projeto-korp` | `{"nome":"Projeto Korp","horario":"2026-09-05T14:22:31Z"}` |
| GET | `/healthz` | Liveness — o processo está vivo |
| GET | `/readyz` | Readiness — pronto para receber tráfego |
| GET | `/metrics` | Métricas no formato Prometheus |
| — | qualquer outra | `404` em JSON |
| POST etc. | `/projeto-korp` | `405` (o roteador do Go 1.22 devolve sozinho) |

O campo `horario` é resolvido **a cada requisição** com
`time.Now().UTC().Format(time.RFC3339)`. O formato RFC 3339 terminado em `Z`
não deixa dúvida sobre o fuso — e o teste automatizado usa um relógio fixo em
`-03:00` justamente para provar que a conversão para UTC acontece.

### Configuração

Tudo por variável de ambiente, sem recompilar:

| Variável | Padrão | O que faz |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | Endereço e porta de escuta |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | `10s` | Prazo para drenar conexões no encerramento |
| `SERVICE_NAME` | `http-server-projeto-korp` | Label nas métricas |

### Rodando local, sem Docker

```bash
cd app
go mod tidy
go test ./... -race -cover
go run .
curl localhost:8080/projeto-korp
```

---

## Observabilidade

### Disponibilidade — em três camadas

O desafio deixa a critério do candidato como expor disponibilidade. A escolha
aqui foi medir em três níveis, porque cada um falha por um motivo diferente:

| Sinal | Origem | Responde a |
|---|---|---|
| `korp_service_up` | a própria aplicação | "o processo se considera saudável?" |
| `up{job="http-server-projeto-korp"}` | gerado pelo Prometheus na coleta | "o alvo respondeu à raspagem?" |
| `probe_success{instance="http://nginx:80/projeto-korp"}` | blackbox-exporter | "uma requisição real, pelo proxy, funcionou?" |

Se `up` está em 1 e `probe_success` em 0, o problema é o NGINX ou a rede, não
a aplicação. Um sinal único não daria esse diagnóstico.

O probe do blackbox ainda valida o **conteúdo** da resposta
(`fail_if_body_not_matches_regexp`): um HTTP 200 devolvendo o JSON errado é
contabilizado como indisponibilidade, que é o que o usuário sentiria.

### Volume de requisições

`korp_http_requests_total{method,path,status}` — um contador. A pergunta
"quantas requisições por segundo?" se responde com
`sum(rate(korp_http_requests_total[5m]))`.

O rótulo `path` passa por uma normalização
([`normalizeRoute`](app/middleware.go)) que mapeia qualquer caminho
desconhecido para `"outros"`. Sem isso, um scanner varrendo URLs aleatórias
criaria uma série temporal nova por URL e derrubaria o Prometheus por explosão
de cardinalidade.

### Todas as métricas expostas

| Métrica | Tipo | Para quê |
|---|---|---|
| `korp_http_requests_total` | counter | Volume, taxa de erro |
| `korp_http_request_duration_seconds` | histogram | Latência p50/p90/p99 |
| `korp_http_requests_in_flight` | gauge | Saturação |
| `korp_http_response_size_bytes` | histogram | Volume de dados |
| `korp_service_up` | gauge | Disponibilidade auto-reportada |
| `korp_build_info` | gauge | Versão/commit em execução |
| `go_*`, `process_*` | vários | Runtime Go e recursos do processo |
| `nginx_*` | vários | Conexões e requisições no proxy |
| `probe_*` | vários | Disponibilidade e latência fim-a-fim |

### O dashboard

14 painéis, agrupados por pergunta:

1. **Está no ar?** — cartões de `up`, `probe_success` e SLA da janela
2. **Quanto tráfego?** — req/s por rota, total acumulado, por status
3. **Está rápido?** — percentis p50/p90/p99 e tempo do probe
4. **Está com erro?** — proporção de 4xx e 5xx
5. **Está saturado?** — requisições em voo, goroutines, heap
6. **Qual versão?** — tabela com `korp_build_info`

Cada painel tem uma descrição (ícone de informação no canto) explicando a
consulta PromQL por trás — feito para ser lido por quem vai operar, não só por
quem construiu.

### Alertas

Cinco regras em [prometheus/rules/](prometheus/rules/), visíveis em
`http://localhost:9090/alerts`: serviço indisponível, caminho fim-a-fim
falhando, taxa de 5xx acima de 5%, latência p95 acima de 500 ms e sumiço da
métrica de saúde. Num ambiente real, um Alertmanager encaminharia para
Slack ou PagerDuty.

---

## O playbook Ansible

Sete roles, uma para cada requisito do enunciado:

| Role | O que faz |
|---|---|
| `docker` | Repositório oficial da Docker, engine, plugins, `daemon.json`, systemd, grupo do usuário |
| `rede` | Rede bridge `korp-net` com sub-rede fixa e nome de interface previsível |
| `aplicacao` | Copia o código, carimba versão/commit, constrói a imagem |
| `nginx` | Publica o `.conf` no volume, valida com `nginx -t` e recarrega sem downtime |
| `monitoramento` | Configs do Prometheus, Blackbox e provisionamento do Grafana |
| `stack` | `docker compose up` aguardando todos os healthchecks |
| `validacao` | Requisição HTTP real, valida o contrato e imprime tudo no console |

### Idempotência

Rodar duas vezes seguidas não muda nada na segunda:

```bash
$ ansible-playbook -i inventory.ini site.yml
PLAY RECAP ***
localhost : ok=38  changed=0  unreachable=0  failed=0
```

A imagem só é reconstruída quando o código muda; o NGINX só recarrega quando o
`.conf` muda; o Prometheus só recarrega quando a configuração muda.

### Comandos úteis

```bash
ansible-playbook -i inventory.ini site.yml                      # tudo
ansible-playbook -i inventory.ini site.yml --check --diff       # simulação
ansible-playbook -i inventory.ini site.yml --tags monitoramento # só uma parte
ansible-playbook -i inventory.ini site.yml --tags validacao     # só validar
ansible-playbook -i inventory.ini site.yml --syntax-check       # sintaxe
```

### Provisionando um servidor remoto

Edite [ansible/inventory.ini](ansible/inventory.ini), troque o bloco local
pelo remoto e rode o mesmo comando. Nenhuma role assume que o alvo é local.

---

## Decisões técnicas

O detalhamento completo, com as alternativas consideradas e por que foram
descartadas, está em **[docs/DECISOES-TECNICAS.md](docs/DECISOES-TECNICAS.md)**.
Em resumo:

- **Go com biblioteca padrão**, sem framework web. Para quatro rotas, o
  `net/http` do Go 1.22 já resolve roteamento por método; uma dependência
  externa aqui só adicionaria superfície de manutenção.
- **Imagem final `scratch`** (~8 MB). Sem shell, sem gerenciador de pacotes,
  sem libc: quase nada para um invasor executar. O healthcheck é feito pelo
  próprio binário, com a flag `-healthcheck`.
- **Rede definida pelo usuário**, não a `bridge` padrão. É o que dá resolução
  DNS por nome de container — sem ela, o NGINX não conseguiria usar
  `http-server-projeto-korp` como hostname.
- **`resolver 127.0.0.11` no NGINX.** Sem isso, o NGINX resolveria o nome do
  container uma vez na inicialização e continuaria apontando para um IP morto
  depois que a aplicação fosse recriada.
- **Rede `external: true` no Compose.** A criação da rede é um passo próprio
  do playbook, como o desafio pede — e não um efeito colateral do `up`.
- **Disponibilidade em três camadas** (aplicação, coleta, fim-a-fim), porque
  cada uma falha por um motivo distinto.
- **Grafana 100% provisionado por arquivo.** Datasource e dashboard nascem do
  repositório; a interface nunca é a fonte da verdade.

---

## Solução de problemas

| Sintoma | Causa provável | O que fazer |
|---|---|---|
| `permission denied` no socket do Docker | O grupo `docker` só vale em nova sessão | `newgrp docker` ou logout/login |
| `network korp-net not found` | Subiu o Compose sem criar a rede | `make rede` |
| NGINX devolve 502 | A aplicação ainda está subindo | `docker compose ps` e aguardar o healthcheck |
| Porta 80 ocupada | Outro servidor no host | `sudo ss -tlnp \| grep :80` |
| Grafana sem dados | Prometheus ainda não coletou | Aguardar ~30 s e conferir `/targets` |
| Dashboard não aparece | Volume de provisionamento não montou | `docker compose logs grafana` |
| Playbook falha em `community.docker` | Coleção ou SDK Python ausente | `ansible-galaxy collection install -r ansible/requirements.yml` |

Diagnóstico rápido:

```bash
docker compose ps                            # estado e healthchecks
docker compose logs -f http-server-projeto-korp
docker network inspect korp-net              # quem está na rede
curl -s localhost:9090/api/v1/targets | jq   # o que o Prometheus vê
```

---

## Licença

MIT — veja [LICENSE](LICENSE).
