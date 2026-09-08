# Decisões técnicas

Cada seção segue o mesmo formato: **o que foi feito**, **por quê**, e **o que
foi descartado**. É o material de apoio para defender o projeto numa conversa
técnica.

---

## 1. Aplicação

### 1.1 Biblioteca padrão do Go, sem framework

**Feito:** `net/http` puro, com o roteador `ServeMux` do Go 1.22.

**Por quê:** o serviço tem quatro rotas. O `ServeMux` do Go 1.22 já aceita
padrões com método (`"GET /projeto-korp"`) e devolve `405 Method Not Allowed`
sozinho quando o método não bate. Gin, Echo ou Chi trariam middlewares
prontos, mas também uma árvore de dependências para manter atualizada,
auditar por CVE e explicar. Menos dependência é menos superfície.

**Descartado:** Gin (o mais comum em desafios), Chi (bom meio-termo). Se o
projeto crescesse para dezenas de rotas com parâmetros, grupos e validação de
corpo, o Chi passaria a valer a pena.

### 1.2 Injeção de dependências na struct `application`

**Feito:** config, logger, métricas e a **função de relógio** vivem numa
struct; os handlers são métodos dela.

**Por quê:** o campo `now func() time.Time` é o que torna o teste do horário
determinístico. O teste injeta um relógio fixo em `-03:00` e verifica que a
saída é exatamente `2026-09-05T15:30:45Z` — provando a conversão para UTC.
Com `time.Now()` chamado direto dentro do handler, esse teste seria impossível
de escrever sem gambiarra.

**Descartado:** variáveis globais e o registry global do Prometheus. Ambos
criam acoplamento entre testes (uma métrica registrada num teste vaza para o
seguinte e causa `duplicate metrics collector registration attempted`).

### 1.3 Formato do horário: RFC 3339 com `Z`

**Feito:** `time.Now().UTC().Format(time.RFC3339)` → `2026-09-05T14:22:31Z`.

**Por quê:** é o formato de data/hora da internet (RFC 3339, um perfil do
ISO 8601). Ordenável como string, sem ambiguidade de fuso, parseável por
qualquer linguagem sem biblioteca extra. O `Z` deixa explícito que é UTC.

**Descartado:** `time.RFC1123` (verboso e dependente de locale),
`Unix timestamp` (ilegível para humanos), formato brasileiro (ambíguo entre
dia/mês e sem fuso).

### 1.4 Encerramento gracioso

**Feito:** `signal.NotifyContext` captura SIGTERM/SIGINT e chama
`srv.Shutdown(ctx)` com prazo de 10 s.

**Por quê:** `docker stop` manda SIGTERM e espera 10 s antes do SIGKILL. Sem
tratar o sinal, toda requisição em andamento é cortada no meio a cada deploy.
Com o shutdown gracioso, o servidor para de aceitar conexões novas e termina
as que já estão em curso.

**Detalhe que sustenta isso:** o `ENTRYPOINT` do Dockerfile está na forma
*exec* (`["/http-server-projeto-korp"]`). Na forma *shell*, o processo viraria
filho de um `/bin/sh` que é o PID 1 — e o sinal nunca chegaria à aplicação.

### 1.5 Timeouts explícitos no servidor

**Feito:** `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`.

**Por quê:** o `http.Server` do Go vem **sem timeout algum** por padrão. Um
cliente que abre a conexão e envia um cabeçalho por minuto (ataque Slowloris)
segura um descritor de arquivo indefinidamente. Com poucas centenas de
conexões assim, o processo esgota os FDs e para de aceitar qualquer coisa.

### 1.6 Testes rodando dentro do build da imagem

**Feito:** `RUN go vet ./... && go test ./... -race` como camada do Dockerfile.

**Por quê:** torna impossível existir uma imagem publicada com a suíte
quebrada — o build simplesmente falha. O `-race` liga o detector de corrida,
que é onde bugs de concorrência aparecem.

**Contrapartida honesta:** isso deixa o build um pouco mais lento e mistura
duas responsabilidades. Num pipeline maduro, os testes rodariam num estágio
separado do CI e o Dockerfile só construiria. Mantive junto aqui porque o
desafio precisa subir com um comando só, sem CI externo.

---

## 2. Container

### 2.1 Build multi-stage terminando em `scratch`

**Feito:** estágio `golang:1.23-alpine` compila; estágio final é `scratch`
com apenas o binário, os certificados de CA e o `zoneinfo`.

**Por quê:**

| | Tamanho | Superfície |
|---|---|---|
| `golang:1.23` (imagem única) | ~850 MB | compilador, shell, apt, git |
| `alpine` + binário | ~15 MB | shell, apk, busybox |
| `scratch` + binário | ~8 MB | nada além do binário |

Sem shell e sem gerenciador de pacotes, um invasor que consiga execução de
comando dentro do container não tem `sh`, `curl` ou `wget` para dar o próximo
passo. É defesa em profundidade barata.

**O que isso exige:** `CGO_ENABLED=0`, para gerar um binário estaticamente
ligado que não precisa de libc.

**Descartado:** `distroless` (excelente, mas depende do `gcr.io` e ainda
carrega um sistema de arquivos mínimo), `alpine` (prático pelo shell, mas
mantém superfície sem necessidade real aqui).

### 2.2 Healthcheck feito pelo próprio binário

**Feito:** `HEALTHCHECK CMD ["/http-server-projeto-korp", "-healthcheck"]`.

**Por quê:** em `scratch` não existe `curl` nem `wget`. As alternativas seriam
inchar a imagem só para o healthcheck ou abrir mão dele. A flag
`-healthcheck` faz o binário agir como cliente: chama `/healthz` em si mesmo e
sai com 0 ou 1. Custo: zero bytes a mais na imagem.

### 2.3 Usuário não-root, `read_only`, `cap_drop: ALL`

**Feito:** `USER 65532:65532` no Dockerfile; `read_only: true`,
`cap_drop: [ALL]` e `no-new-privileges` no Compose.

**Por quê:** por padrão o processo do container roda como root. Se houver
falha de configuração no runtime ou no kernel, esse root é o root do host.
A aplicação não escreve em disco nem precisa de nenhuma capability, então
tirar tudo não custa funcionalidade.

**Detalhe:** `scratch` não tem `/etc/passwd`, e um `USER korp` por nome
falharia. Por isso o estágio de build cria a linha do usuário e ela é copiada
para a imagem final — e o `USER` usa o UID numérico.

### 2.4 Flags de build: `-trimpath` e `-ldflags "-s -w"`

- `-trimpath` remove caminhos absolutos da máquina de build do binário (não
  vaza `/home/lucas/...` num stack trace).
- `-s -w` descartam a tabela de símbolos e o DWARF: binário menor.
- `-X main.version=...` injeta versão, commit e data em tempo de build; eles
  reaparecem na métrica `korp_build_info` e no painel do Grafana.

### 2.5 Ordem das camadas e cache

`COPY go.mod` → `go mod download` → `COPY . .` → `build`. Como as dependências
entram numa camada anterior ao código, mudar um `.go` não invalida o download
dos módulos. Junto com `--mount=type=cache`, um rebuild de código leva
segundos em vez de minutos.

---

## 3. Rede Docker

### 3.1 Rede definida pelo usuário, não a `bridge` padrão

**Feito:** `docker network create --driver bridge korp-net`.

**Por quê:** a diferença decisiva é o **DNS**. Numa rede definida pelo
usuário, o Docker sobe um resolvedor interno em `127.0.0.11` que traduz nomes
de container para IP. É o que permite o NGINX escrever
`proxy_pass http://http-server-projeto-korp:8080`. Na rede `bridge` padrão
isso não funciona — lá só haveria o antigo `--link`, obsoleto há anos.

Ganhos adicionais: isolamento em relação a containers de outros projetos e
controle da faixa de IPs.

### 3.2 Sub-rede fixa `172.28.0.0/16`

**Por quê:** com faixa fixa, regras de firewall e de `allow` no NGINX ficam
previsíveis, e não há surpresa de colisão com uma VPN corporativa que já use
a faixa que o Docker escolheria sozinho.

### 3.3 Rede `external: true` no Compose

**Por quê:** o desafio pede a criação da rede como passo próprio. Declarando
como externa, o `docker compose up` **consome** uma rede que já existe em vez
de criar uma com o nome do projeto no prefixo. Isso deixa a etapa "criar a
rede" visível no playbook, em vez de escondida como efeito colateral.

**Contrapartida:** é preciso criar a rede antes. O playbook faz isso na role
`rede`, e o `make up` depende do alvo `rede`. Está documentado no README e no
guia de problemas.

---

## 4. NGINX

### 4.1 `resolver 127.0.0.11` + hostname em variável

**Feito:**

```nginx
resolver 127.0.0.11 valid=10s ipv6=off;
set $korp_upstream "http-server-projeto-korp";
proxy_pass http://$korp_upstream:8080$request_uri;
```

**Por quê:** este é o detalhe que mais separa uma configuração de demonstração
de uma que aguenta o dia a dia. O NGINX resolve nomes em blocos `upstream`
**uma única vez, na inicialização**. Se a aplicação for recriada
(`docker compose up -d --force-recreate`), ela ganha um IP novo — e o NGINX
continua mandando tráfego para o IP antigo, devolvendo 502 até alguém
reiniciar o proxy.

Usando o resolvedor do Docker com uma variável no `proxy_pass`, o nome é
reavaliado a cada 10 s e o proxy se recupera sozinho.

**Contrapartida:** com `proxy_pass` em variável não dá para usar um bloco
`upstream` com `keepalive`. Para um serviço deste porte, a robustez vale mais
que o ganho de reaproveitar conexões. Em produção, com volume alto, a resposta
correta seria um Service do Kubernetes ou um Consul/registrador na frente.

### 4.2 Cabeçalhos de proxy

`X-Real-IP` e `X-Forwarded-For` preservam o IP do cliente — sem eles, todo
acesso no log da aplicação apareceria vindo do IP do container do NGINX.
`X-Request-ID` gera um identificador por requisição, base para rastrear uma
chamada entre o log do proxy e o log da aplicação.

### 4.3 Log de acesso em JSON com tempos do upstream

`request_time` é o total visto pelo cliente; `upstream_response_time` é o que a
aplicação gastou. A diferença entre os dois separa "a aplicação está lenta" de
"a rede ou o proxy está lento" — pergunta que aparece em todo incidente de
latência.

### 4.4 `limit_req` e `client_max_body_size`

Um limite de 30 req/s por IP com folga de 20 protege a aplicação de rajadas, e
1 MB de corpo máximo evita upload abusivo num endpoint que só faz GET. São
duas linhas que eliminam classes inteiras de abuso trivial.

### 4.5 Volume em `:ro`

O NGINX só precisa **ler** a configuração. Montar somente leitura significa
que um comprometimento do container não consegue reescrever arquivos no host.

---

## 5. Docker Compose

### 5.1 `depends_on` com `condition: service_healthy`

`depends_on` sozinho só garante ordem de *início*, não que o serviço esteja
pronto. Com `condition: service_healthy`, o NGINX só sobe depois que o
healthcheck da aplicação passa — eliminando a janela de 502 logo após o `up`.

### 5.2 `expose` em vez de `ports` na aplicação

O desafio pede que a aplicação não exponha portas ao host. `expose` apenas
documenta a porta para leitores e ferramentas; quem publica no host é `ports`,
que a aplicação não tem. O teste de aceitação comprova isso chamando
`docker port` (saída vazia) e tentando `localhost:8080` (recusa de conexão).

### 5.3 Rotação de log

Sem `max-size`, o arquivo JSON de log de um container cresce até encher o
disco do host. É uma das causas mais banais e mais comuns de incidente.
10 MB × 3 arquivos por container é um teto seguro.

### 5.4 Limites de recursos

`cpus: 0.50` e `memory: 128M` na aplicação. Um vazamento de memória fica
contido no container em vez de derrubar o host inteiro por OOM.

### 5.5 Volumes nomeados para Prometheus e Grafana

Dados de série temporal e o banco do Grafana sobrevivem a
`docker compose down`. Só `down --volumes` apaga — e é o que `make limpar` faz
explicitamente.

### 5.6 Perfil `carga`

O gerador de tráfego fica atrás de `profiles: ["carga"]`, então não sobe por
padrão. Numa demonstração, `make carga` dá vida aos gráficos em poucos
segundos, sem precisar de um `while true; do curl; done` num terminal à parte.

---

## 6. Observabilidade

### 6.1 Por que três sinais de disponibilidade

| Sinal | Quem produz | Falha quando |
|---|---|---|
| `korp_service_up` | a aplicação | o processo está vivo mas se sabe degradado |
| `up{job=...}` | o Prometheus, na coleta | o processo morreu ou a rede caiu |
| `probe_success` | o blackbox-exporter | o caminho real (NGINX → app) quebrou |

O caso interessante é `up == 1` com `probe_success == 0`: a aplicação está
perfeita e o problema é o proxy. Um sinal só não distingue isso, e o tempo
gasto descobrindo em qual camada está a falha é o grosso do tempo de um
incidente.

### 6.2 Contador, não gauge, para volume

Contadores só sobem e reiniciam em zero quando o processo reinicia. A função
`rate()` do PromQL entende esse reinício e calcula a taxa corretamente. Um
gauge de "requisições por segundo" calculado pela aplicação perderia
informação a cada raspagem e não permitiria escolher a janela na consulta.

### 6.3 Histograma, não summary, para latência

Histogramas são agregáveis entre instâncias: dá para somar os buckets de
várias réplicas e calcular um p99 global com `histogram_quantile()`. Summaries
calculam quantis no cliente e **não podem ser somados** — a média de dois p99
não é o p99 do conjunto.

Os buckets foram ajustados para a faixa de milissegundos deste serviço; os
buckets padrão do `client_golang` (5 ms a 10 s) desperdiçariam resolução
justamente onde ficam as respostas.

### 6.4 Cardinalidade sob controle

`normalizeRoute()` mapeia qualquer caminho fora da lista conhecida para
`"outros"`. Sem isso, `/aaa`, `/bbb`, `/ccc`... de um scanner viram uma série
temporal cada. Cardinalidade descontrolada é a forma mais comum de derrubar um
Prometheus — e o erro é cometido justamente por quem coloca `r.URL.Path` cru
como label.

### 6.5 Registry próprio, não o global

`prometheus.NewRegistry()` em vez de `prometheus.DefaultRegisterer` dá
controle explícito do que é exposto e permite que cada teste crie um registry
limpo, sem colisão de registro entre testes.

### 6.6 Coleta direto na aplicação, não pelo NGINX

O job do Prometheus aponta para `http-server-projeto-korp:8080`, dentro da
rede. Assim, uma falha do proxy não é confundida com uma falha da aplicação —
e as métricas continuam sendo coletadas mesmo com o NGINX fora do ar.

### 6.7 Grafana provisionado por arquivo

Datasource e dashboard nascem de `datasources.yml`, `dashboards.yml` e do JSON
versionado. Nada é clicado na interface. Isso é o que torna o ambiente
reprodutível: derrubar tudo com `--volumes` e subir de novo devolve exatamente
o mesmo Grafana. Configuração feita a mão morre com o volume.

---

## 7. Ansible

### 7.1 Roles espelhando os requisitos

As sete roles têm exatamente o recorte dos itens do enunciado. É uma escolha
de comunicação: qualquer pessoa lendo `site.yml` vê o desafio sendo cumprido
item a item, sem precisar caçar tarefas dentro de um playbook monolítico.

### 7.2 Repositório oficial da Docker, não o pacote da distro

O `docker.io` do Ubuntu costuma estar atrás em versões e **não** traz o
Compose V2 como plugin (`docker compose`, sem hífen). O repositório oficial
traz engine, CLI, buildx e compose-plugin coerentes entre si.

A chave GPG é instalada com `signed-by` apontando para o keyring específico —
sem isso, a chave da Docker passaria a valer para *todos* os repositórios da
máquina.

### 7.3 Idempotência de verdade

- A imagem só é reconstruída se o código copiado mudou (`fonte_copiada is changed`).
- O NGINX só recarrega se o `.conf` mudou — e só depois de `nginx -t` passar.
- O Prometheus só recarrega se a configuração mudou, via `POST /-/reload`
  (que preserva os dados em memória, ao contrário de reiniciar o container).
- `daemon.json` é validado com `validate:` **antes** de ser gravado: um JSON
  quebrado impediria o daemon de subir, e aí não haveria mais Docker para
  consertar nada.

### 7.4 Instalação automática das coleções

O `site.yml` verifica e instala `community.docker` se faltar. É o que permite
cumprir literalmente o requisito de "um único comando": quem clona o
repositório não precisa lembrar do `ansible-galaxy`.

### 7.5 A validação falha o playbook

A role `validacao` não só imprime a resposta: ela **afirma** que o JSON tem
`nome == "Projeto Korp"`, que o horário casa com o padrão RFC 3339 em UTC, e
que duas chamadas separadas por 2 s devolvem horários diferentes (provando a
resolução dinâmica). Também espera os alvos do Prometheus ficarem `up` e
confirma, pela API do Grafana, que o dashboard e o datasource existem.

Se qualquer uma dessas afirmações falhar, o playbook falha. "Provisionou" e
"funciona" passam a ser a mesma coisa.

---

## 8. O que eu faria diferente em produção

Sendo honesto sobre os limites deste desafio:

| Aqui | Em produção |
|---|---|
| Senha do Grafana em texto no `group_vars` | Ansible Vault, ou um cofre externo (Vault, AWS Secrets Manager) |
| HTTP puro na porta 80 | TLS com certificado gerenciado, redirecionamento 301, HSTS |
| Prometheus com armazenamento local | Thanos ou Mimir para retenção longa e alta disponibilidade |
| Alertas só visíveis na UI | Alertmanager com rotas para Slack/PagerDuty e silenciamento |
| Uma réplica da aplicação | Várias réplicas atrás de um balanceador, deploy sem downtime |
| Logs no `json-file` local | Coleta centralizada (Loki, ELK) com retenção e busca |
| Sem rastreamento distribuído | OpenTelemetry, para correlacionar log, métrica e trace |
| Compose num host único | Kubernetes ou ECS, com autoescala e agendamento |
| Testes dentro do Dockerfile | Estágio dedicado no CI, com relatório de cobertura e SAST |
| Imagem construída no alvo | Registry privado, imagem assinada, varredura de vulnerabilidades |
