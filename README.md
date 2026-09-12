# Projeto Korp

Serviço HTTP em Go, containerizado, atrás de um proxy reverso NGINX, com
observabilidade completa — métricas, alertas que **chegam a alguém**, SLO com
orçamento de erro e logs no mesmo Grafana — e o ambiente inteiro provisionado
por um único comando Ansible.

```
            ┌──────────────────────────── host ────────────────────────────┐
            │                                                              │
 curl :80 ─►│  :80   NGINX ────────┐                                       │
            │                      │   rede bridge korp-net                │
  nav :3000►│  :3000 Grafana       │   (172.28.0.0/16)                     │
            │          │           ▼                                       │
            │          │      http-server-projeto-korp:8080                │
            │          │           (sem porta publicada no host)           │
  nav :9090►│  :9090 Prometheus ──► nginx-exporter:9113                    │
            │          │       └──► blackbox-exporter:9115 ──┐             │
            │          │                                     │             │
            │          │              sonda o caminho real ──┘             │
            │          │                                                   │
            │          ▼  regra dispara                                    │
  nav :9093►│  :9093 Alertmanager ──► notificador:9094 ──► Discord          │
            │          agrupa, inibe        │  vira log + métrica          │
            │                               ▼                              │
            │  todos os containers ──► promtail ──► loki:3100 ──► Grafana  │
            │       (logs)              via docker-socket-proxy            │
            └──────────────────────────────────────────────────────────────┘
```

O caminho de um alerta, do sintoma ao celular, é uma corrente de seis elos —
e o projeto valida a corrente inteira, não só as pontas:

**métrica → regra avaliada → Alertmanager roteia → webhook entregue →
notificação no Discord → o próprio alerta vira log e métrica**

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
- [Alertas e SLO](#alertas-e-slo)
- [Logs](#logs)
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
| Prometheus coletando as métricas | [prometheus/prometheus.yml](prometheus/prometheus.yml) | ✅ 9 jobs |
| Grafana visualizando | [grafana/](grafana/) | ✅ |
| Dashboard de análise do serviço | [dashboard JSON](grafana/dashboards/http-server-projeto-korp-dashboard.json) | ✅ 14 painéis |
| Playbook Ansible completo | [ansible/site.yml](ansible/site.yml) | ✅ 7 roles |
| Validação HTTP com resposta no console | [ansible/roles/validacao/](ansible/roles/validacao/tasks/main.yml) | ✅ |
| **Bônus:** Grafana provisionado por arquivo | `datasources.yml`, `dashboards.yml`, JSON | ✅ zero cliques |

Depois de entregue o que o desafio pedia, o projeto continuou. O que veio
em seguida trata a parte que faltava: o alerta que dispara e não avisa
ninguém.

| Além do desafio | Onde está |
|---|---|
| Alertmanager agrupando, inibindo e roteando por severidade | [alertmanager/](alertmanager/alertmanager.yml) |
| Notificação no Discord, com o segredo fora do Git | [notificador/](notificador/notificador.py) |
| Alerta vira log estruturado **e** métrica (`korp_alertas_recebidos_total`) | [notificador/](notificador/notificador.py) |
| *Dead man's switch*: um alerta que monitora o monitoramento | [slo-projeto-korp.yml](prometheus/rules/slo-projeto-korp.yml) |
| SLO de 99,9% com orçamento de erro e taxa de queima multi-janela | [slo-projeto-korp.yml](prometheus/rules/slo-projeto-korp.yml) |
| Logs no Grafana, ao lado das métricas (Loki + Promtail) | [loki/](loki/loki-config.yml), [promtail/](promtail/promtail-config.yml) |
| Socket do Docker atrás de um proxy só-leitura | [docker-compose.yml](docker-compose.yml) |
| Segundo dashboard: SLO, alertas e logs | [dashboard de SLO](grafana/dashboards/slo-projeto-korp-dashboard.json) |
| O playbook **prova** que o alerta chega ao destino | [roles/validacao/](ansible/roles/validacao/tasks/main.yml) |

Extras que foram além do mínimo pedido: testes automatizados em Go rodando
dentro do build da imagem, healthchecks em todos os containers, endurecimento
de segurança dos containers (não-root, `read_only`, `cap_drop: ALL`),
blackbox-exporter para disponibilidade fim-a-fim, exporter do NGINX, regras
de alerta, rotação de logs, encerramento gracioso, limites de recursos,
script de teste de aceitação (55 verificações) e pipeline de CI.

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

O Ansible não roda nativamente no Windows como nó de controle. Use o WSL2:

```powershell
wsl --install -d Ubuntu-24.04
```

Se o comando responder que é preciso reiniciar, **reinicie e rode de novo** —
a primeira execução apenas habilita o recurso do Windows, sem instalar a
distribuição.

Dentro do Ubuntu, habilite o **systemd** antes de qualquer coisa. O playbook
gerencia o Docker por `ansible.builtin.systemd`, e sem isso ele falha:

```bash
sudo tee /etc/wsl.conf >/dev/null <<'EOF'
[boot]
systemd=true
EOF
```

Depois, no PowerShell, `wsl --shutdown` e abra o Ubuntu de novo. Confirme com
`ps -p 1 -o comm=`, que deve responder `systemd`. A partir daí, siga o fluxo
normal do Linux.

Clone o repositório **dentro do filesystem do Linux** (`~/projeto-korp`), não
em `/mnt/c`: o acesso ao disco do Windows via 9p deixa o build da imagem
lento e ignora os finais de linha LF que o `.gitattributes` exige.

Com a stack no ar, `http://localhost/projeto-korp` funciona também no
navegador do Windows — o WSL2 encaminha as portas automaticamente.

> **Atenção:** o WSL desliga a VM pouco depois que a última sessão se
> desconecta, e os containers caem junto. Deixe um terminal do Ubuntu aberto
> enquanto estiver usando o ambiente. Ao religar, a stack volta sozinha:
> o `docker.service` fica habilitado no boot e os containers usam
> `restart: unless-stopped`.

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
│   ├── prometheus.yml                # jobs de coleta + alvo do Alertmanager
│   └── rules/
│       ├── alertas-projeto-korp.yml  #   alertas por limiar
│       └── slo-projeto-korp.yml      #   SLO, taxa de queima e watchdog
│
├── alertmanager/alertmanager.yml     # agrupamento, inibição e roteamento
├── notificador/notificador.py        # webhook -> log + métrica + Discord
│
├── loki/loki-config.yml              # armazenamento dos logs
├── promtail/promtail-config.yml      # coleta dos logs dos containers
│
├── blackbox/blackbox.yml             # como sondar o serviço de fora
│
├── grafana/
│   ├── provisioning/datasources/     # datasources.yml (Prometheus + Loki)
│   ├── provisioning/dashboards/      # dashboards.yml
│   └── dashboards/*.json             # dashboards versionados (operação, SLO)
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
│       ├── monitoramento/            # Prometheus, Grafana, alertas, logs
│       ├── stack/                    # docker compose up
│       └── validacao/                # requisição HTTP + cadeia de alertas
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

### Os dashboards

São dois. O de **operação** (`/d/projeto-korp`) responde "como o serviço está
agora"; o de **SLO** (`/d/projeto-korp-slo`) responde "estamos cumprindo o
que prometemos, e o alerta chegaria se não estivéssemos".

O de operação tem 14 painéis, agrupados por pergunta:

1. **Está no ar?** — cartões de `up`, `probe_success` e SLA da janela
2. **Quanto tráfego?** — req/s por rota, total acumulado, por status
3. **Está rápido?** — percentis p50/p90/p99 e tempo do probe
4. **Está com erro?** — proporção de 4xx e 5xx
5. **Está saturado?** — requisições em voo, goroutines, heap
6. **Qual versão?** — tabela com `korp_build_info`

O de SLO tem 12, descritos em [Alertas e SLO](#alertas-e-slo).

Cada painel tem uma descrição (ícone de informação no canto) explicando a
consulta PromQL por trás — feito para ser lido por quem vai operar, não só por
quem construiu.

---

## Alertas e SLO

O desafio pedia métrica e dashboard, e isso ficou pronto. Mas um dashboard só
funciona enquanto alguém está olhando para ele, e às 3h da manhã ninguém está.
Esta parte fecha esse buraco.

### O que estava faltando

O projeto já tinha cinco regras de alerta escritas. Elas **disparavam e
morriam na tela do Prometheus** — não havia bloco `alerting:` no
`prometheus.yml` e não havia Alertmanager na stack. É a falha mais silenciosa
possível: tudo parece configurado, o alerta fica vermelho na interface, e
ninguém é avisado.

### A cadeia completa

```
korp_http_requests_total          a aplicação expõe
        │
        ▼
Prometheus avalia prometheus/rules/*.yml        a cada 15s
        │
        ▼
Alertmanager   agrupa por (alertname, severidade)
               inibe o sintoma quando a causa já foi notificada
               roteia: critico -> 10s | alto/aviso -> 30s | watchdog -> 0s
        │
        ▼
notificador    log estruturado + korp_alertas_recebidos_total
        │
        ├──► Discord (se DISCORD_WEBHOOK_URL estiver definida)
        └──► Promtail -> Loki -> Grafana
```

### Três decisões que valem explicar

**Inibição.** Quando a aplicação cai, quatro alertas disparam juntos:
`ServicoIndisponivel`, `CaminhoFimAFimFalhando`, `TaxaDeErro5xxAlta` e
`LatenciaP95Alta`. São quatro mensagens sobre um único incidente. As regras de
`inhibit_rules` entregam só a primeira — a causa — e seguram os sintomas.

**Taxa de queima, não só limiar.** Alerta por limiar ("p95 passou de 500 ms")
dispara em pico que se resolve sozinho e não diz nada sobre quanto da
confiabilidade prometida já foi gasta. Os alertas de
[slo-projeto-korp.yml](prometheus/rules/slo-projeto-korp.yml) olham a
velocidade com que o orçamento de erro está sendo consumido, com duas janelas
simultâneas: a longa dá confiança de que o problema é real, a curta faz o
alerta limpar sozinho quando passa.

| SLO | Orçamento de erro | Queima 14,4x | Queima 1x |
|---|---|---|---|
| 99,9% em 30 dias | ~43 min por mês | acaba em 2 dias | acaba no prazo |

**Dead man's switch.** `PipelineDeAlertasViva` é `vector(1)` — dispara sempre,
de propósito. Ele não monitora o serviço: monitora o monitoramento. No dia em
que o Prometheus cair, todos os outros alertas param de chegar, e um canal
silencioso é indistinguível de um canal saudável. O silêncio *deste* alerta é
o sinal.

Ele **não** vai para o Discord — repetir a cada 5 minutos encheria o canal de
ruído e treinaria a equipe a ignorar as notificações, matando justamente o
alerta de verdade que aparecesse no meio. Fica no log e na métrica, que é onde
o silêncio dele é detectável (`make notificacoes`).

### Por que um notificador próprio e não `discord_configs`

O Alertmanager não expande variáveis de ambiente no arquivo de configuração.
Usar o receiver nativo do Discord obrigaria a commitar a URL do webhook em
texto puro no YAML, ou a gerar esse YAML por template — no primeiro caso o
segredo vaza para o Git, no segundo o arquivo versionado deixa de ser o que
roda. Com um notificador próprio, o segredo vive só no `.env`, e o alerta
ainda ganha log estruturado e métrica de quebra.

Sem webhook configurado, nada quebra: o alerta continua virando log e métrica.
É assim que o CI roda, sem precisar de credencial nenhuma.

### Vendo funcionar

A única prova honesta é um incidente de verdade:

```bash
make incidente        # para a aplicação de propósito
# ~1 min depois:
make alertas          # ServicoIndisponivel em "firing"
make notificacoes     # o que chegou ao notificador
make slo              # disponibilidade, orçamento e taxa de queima
make fim-incidente    # religa; o alerta resolve em ~1 min
```

Repare no que **não** acontece: `LatenciaP95Alta` e `TaxaDeErro5xxAlta` não
viram notificação. A inibição funcionou.

Para receber no Discord: crie um webhook em *Configurações do canal >
Integrações > Webhooks*, e coloque a URL em `DISCORD_WEBHOOK_URL` no `.env`
(que está no `.gitignore`). Pelo Ansible:

```bash
ansible-playbook -i ansible/inventory.ini ansible/site.yml \
  -e discord_webhook_url='https://discord.com/api/webhooks/...'
```

### O dashboard de SLO

Em `http://localhost:3000/d/projeto-korp-slo`: disponibilidade contra o SLO,
orçamento de erro restante, taxa de queima nas quatro janelas, histórico dos
alertas entregues, a tabela dos alertas ativos e os logs — tudo na mesma tela.

---

## Logs

Métrica responde *o que* quebrou. Log responde *por quê*. Estando os dois no
mesmo Grafana, o caminho entre as duas perguntas é de dois cliques, sem trocar
de ferramenta no meio de um incidente.

- **Loki** guarda 7 dias de log (métrica é barata e fica 31 dias; log é caro).
- **Promtail** descobre os containers pela API do Docker, o que dá labels pelo
  **nome** do serviço — por glob no disco só existiria o ID, um hash que muda
  a cada recriação.
- O JSON do `slog` da aplicação é parseado, e `level` vira label. Campos de
  alta cardinalidade (ID de requisição, IP, path com parâmetro) **não** viram
  label: no Loki cada combinação de labels cria um fluxo, e é assim que se
  derruba um Loki.

### O socket do Docker

Para descobrir containers pela API, o Promtail precisaria de
`/var/run/docker.sock`. Montar o socket num container equivale a dar root no
host para ele — quem fala com o socket cria container privilegiado e monta o
filesystem inteiro.

Por isso existe o `docker-socket-proxy` no meio, liberando só leitura do que o
Promtail usa — `CONTAINERS=1` para listar e ler logs, `NETWORKS=1` porque a
descoberta monta as labels `__meta_docker_network_*` — e bloqueando o resto
(`POST=0`: não cria, não para, não executa). O teste de aceitação verifica que
o Promtail **não** tem o socket montado.

```bash
make logs-loki        # últimas linhas da aplicação, lidas do Loki
```

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
