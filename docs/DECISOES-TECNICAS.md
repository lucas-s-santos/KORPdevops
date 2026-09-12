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
- A data de build vem do **commit** (`git show -s --format=%cI`), não do
  relógio da execução. Tirá-la de `ansible_date_time` faria o `.env` mudar em
  toda execução e a tarefa voltar sempre `changed`, quebrando a idempotência
  que esta seção afirma. De quebra, amarrar ao commit torna a imagem
  reproduzível: o mesmo commit sempre gera os mesmos metadados.

### 7.4 Instalação automática das coleções — e por que ela não basta

O `site.yml` instala as coleções a partir do `requirements.yml`, para que quem
clona o repositório não precise lembrar do `ansible-galaxy`.

Só que isso tem um limite que vale conhecer: **uma coleção instalada durante a
execução não passa a valer no processo em curso**. O carregador de plugins do
Ansible já resolveu a versão presente no início do play, e continua usando
aquela até o fim. Numa máquina onde o `apt install ansible` deixou uma
`community.docker` antiga, instalar uma mais nova no `pre_tasks` não muda nada
para as roles que vêm depois.

A consequência prática: o playbook **não pode depender de recursos acima da
versão que a distribuição já entrega**, ou o requisito de "um único comando"
falha justamente numa máquina recém-instalada — o cenário mais provável numa
avaliação.

É por isso que a role `stack` espera os containers com `docker_container_info`
em vez de usar a opção `wait` do `docker_compose_v2`: `wait` só existe a partir
da 3.9.0, e o Ubuntu 24.04 entrega a 3.7.0. A espera explícita ainda rende um
erro melhor — diz **qual** container não ficou saudável, em vez de estourar um
timeout genérico do Compose.

### 7.5 A validação falha o playbook

A role `validacao` não só imprime a resposta: ela **afirma** que o JSON tem
`nome == "Projeto Korp"`, que o horário casa com o padrão RFC 3339 em UTC, e
que duas chamadas separadas por 2 s devolvem horários diferentes (provando a
resolução dinâmica). Também espera os alvos do Prometheus ficarem `up` e
confirma, pela API do Grafana, que o dashboard e o datasource existem.

Se qualquer uma dessas afirmações falhar, o playbook falha. "Provisionou" e
"funciona" passam a ser a mesma coisa.

---

## 8. Alertas, SLO e notificação

Esta parte não estava no enunciado. Foi construída depois, porque o projeto
tinha cinco regras de alerta que **disparavam e não avisavam ninguém**: não
existia bloco `alerting:` no `prometheus.yml` nem Alertmanager na stack. É a
falha mais silenciosa que um ambiente de monitoramento pode ter — tudo parece
configurado, a interface fica vermelha, e o incidente passa despercebido.

### 8.1 Alertmanager como peça separada

**Feito:** o Prometheus avalia as regras e encaminha para um Alertmanager, que
agrupa, inibe, silencia e entrega.

**Por quê:** são responsabilidades diferentes. O Prometheus sabe *quando* uma
condição é verdadeira; ele não sabe nada sobre quem está de plantão, o que já
foi avisado, o que está silenciado durante uma manutenção, nem quais alertas
são redundantes entre si. Juntar as duas coisas num processo só transformaria
qualquer mudança de política de notificação num reload do coletor de métricas.

**Descartado:** alertas do próprio Grafana (Grafana Alerting). Funcionam, mas
prendem a regra no banco do Grafana em vez de deixá-la em Git ao lado do
código, e o que se ganha em conveniência de interface se perde em revisão por
pull request.

### 8.2 Agrupamento e inibição

**Feito:** `group_by: [alertname, severidade]` com `group_wait` de 30s, e duas
`inhibit_rules` que suprimem os sintomas quando a causa já foi notificada.

**Por quê:** quando a aplicação cai, quatro alertas disparam quase juntos —
serviço fora, caminho fim-a-fim falhando, taxa de 5xx e latência. São quatro
mensagens sobre **um** incidente, chegando exatamente no momento em que a
pessoa de plantão precisa de clareza. A inibição entrega a causa e segura o
resto.

O efeito prático é o que sustenta o resto do sistema: um canal que só toca
quando importa continua sendo lido. Um canal que toca quatro vezes por
incidente é silenciado pela equipe em duas semanas, e aí nem o alerta bom
chega.

**Descartado:** a regra genérica "severidade crítica inibe severidade aviso".
É o exemplo mais citado na documentação, mas aqui os alertas agregados
(`sum(...)`) perdem as labels de serviço, e o `equal:` casaria rótulo ausente
com rótulo ausente — na prática, qualquer alerta crítico apagaria todos os
avisos do ambiente, inclusive os que não têm relação nenhuma com ele.

### 8.3 Taxa de queima, não apenas limiar

**Feito:** os alertas de limiar continuam (`alertas-projeto-korp.yml`), e ao
lado deles entraram alertas baseados no consumo do orçamento de erro
(`slo-projeto-korp.yml`).

**Por quê:** limiar puro erra dos dois lados. Dispara em pico de trinta
segundos que se resolve sozinho, e fica quieto durante uma degradação lenta de
0,5% de erro que, ao fim do mês, estourou o compromisso inteiro. A taxa de
queima mede a coisa certa: a velocidade com que a confiabilidade prometida
está sendo gasta.

```
SLO 99,9% em 30 dias  ->  orçamento de erro = 0,1% = ~43 min por mês

queima  1x  -> consome o orçamento exatamente no prazo
queima  6x  -> acaba em ~5 dias
queima 14,4x -> acaba em ~2 dias
```

**Descartado:** trocar os alertas de limiar pelos de queima. Os dois tipos
respondem a perguntas diferentes — "isto quebrou agora" e "vamos cumprir o
combinado no fim do mês" — e um não substitui o outro.

### 8.4 Duas janelas por alerta

**Feito:** cada alerta de queima exige que uma janela longa **e** uma curta
estejam acima do limiar ao mesmo tempo (1h com 5m, 6h com 30m).

**Por quê:** a janela longa dá confiança de que o problema é real, e não um
pico. A curta é o que faz o alerta **limpar sozinho** depois que o problema
passa. Sem ela, um incidente de dez minutos deixa o alerta ativo pelo resto da
janela longa, porque a média continua suja — e alerta que não se resolve
sozinho é alerta que a equipe aprende a ignorar.

### 8.5 Dead man's switch

**Feito:** `PipelineDeAlertasViva`, cuja expressão é `vector(1)` — sempre
verdadeira. Dispara para sempre, de propósito, e é roteada para o notificador
a cada cinco minutos.

**Por quê:** todo alerta deste projeto compartilha um ponto único de falha: o
próprio Prometheus. Se ele cair, nenhum alerta chega — e um canal silencioso é
**indistinguível** de um canal saudável. Quem está de plantão não tem como
saber a diferença entre "nada quebrou" e "o monitoramento morreu".

O watchdog inverte o sinal: enquanto ele chega, a corrente inteira está
provada (regra avaliada, Alertmanager roteando, webhook entregue). O silêncio
dele é o alarme.

**Descartado:** confiar apenas em `up{job="prometheus"}`. Um Prometheus que
raspa a si mesmo e reporta que está vivo não prova nada sobre o Alertmanager
nem sobre a entrega do webhook — e, se ele estiver fora, também não há quem
avalie essa regra.

### 8.6 Notificador próprio em vez de `discord_configs`

**Feito:** um serviço de ~250 linhas de Python (biblioteca padrão apenas) que
recebe o webhook do Alertmanager, registra o alerta em log estruturado, conta
numa métrica e repassa ao Discord.

**Por quê:** três motivos, em ordem de peso.

1. **O segredo.** O Alertmanager não expande variáveis de ambiente no arquivo
   de configuração. Usar o receiver nativo obrigaria a commitar a URL do
   webhook em texto puro no YAML, ou a gerar esse YAML por template — na
   primeira opção o segredo vai para o Git, na segunda o arquivo versionado
   deixa de ser o que roda. Com o notificador, a URL vive só no `.env`.

2. **O alerta vira dado.** `korp_alertas_recebidos_total` transforma cada
   disparo em série temporal, e o log estruturado deixa a trilha. Sem isso, um
   alerta que toca e some não deixa histórico — e depois do incidente ninguém
   reconstrói o que avisou o quê, nem em que ordem.

3. **Trocar o destino** (Telegram, PagerDuty, um webhook interno) passa a ser
   uma mudança num arquivo Python, sem tocar na configuração do Alertmanager.

**Descartado:** `discord_configs` nativo (pelo problema do segredo) e a
biblioteca `prometheus_client` (seria a única dependência da imagem; o formato
de exposição é texto simples e tem trinta linhas de gerador).

**Custo assumido:** é mais uma peça para manter, e um Python a mais num
projeto Go. Vale pelo item 1 sozinho.

### 8.7 O notificador sempre responde 200

**Feito:** mesmo quando o envio ao Discord falha, o webhook devolve 200, e a
exceção é registrada e contabilizada.

**Por quê:** em erro, o Alertmanager reenfileira e repete o **lote inteiro**.
Devolver erro por causa de um 429 do Discord produziria alertas duplicados no
canal — e o envio já ficou registrado no log e na métrica de qualquer forma.
Um notificador que morre por causa da instabilidade do destino é pior que um
envio perdido: dali em diante, nenhum alerta chega.

### 8.8 Retenção de 31 dias no Prometheus

**Feito:** `--storage.tsdb.retention.time=31d`, um dia a mais que a janela do
SLO.

**Por quê:** com os 15 dias anteriores, a regra que calcula a disponibilidade
de 30 dias faria a média de **meia** janela e reportaria um número otimista —
e um SLO que mente é pior que um SLO que não existe.

---

## 9. Logs

### 9.1 Loki em modo de nó único

**Feito:** um processo com tudo dentro (ingester, distributor, querier,
compactor), chunks em disco, schema TSDB v13, retenção de 7 dias.

**Por quê:** o modo distribuído só faz sentido com vários nós e um object
store de verdade (S3, GCS); num host só, ele adiciona componentes sem
adicionar disponibilidade. A retenção é menor que a das métricas de propósito:
métrica é barata e responde perguntas sobre tendência; log é caro e responde
perguntas sobre o passado recente.

**Descartado:** ELK. Resolve o mesmo problema, mas pede um Elasticsearch — que
sozinho consome mais memória que esta stack inteira — e indexa o conteúdo das
linhas. O Loki indexa só as labels e guarda o resto comprimido, o que é a
troca certa quando a busca quase sempre começa por "qual serviço, qual
janela".

### 9.2 Descoberta pela API do Docker, não por glob no disco

**Feito:** `docker_sd_configs` no Promtail, filtrando pela label do projeto.

**Por quê:** por glob em `/var/lib/docker/containers/*/*-json.log`, o único
identificador disponível é o ID do container — um hash que muda a cada
recriação. Os painéis do Grafana quebrariam no primeiro
`docker compose up --force-recreate`. Pela API vêm o nome e as labels do
Compose, e o painel pode filtrar por `servico="nginx"` para sempre.

### 9.3 Cardinalidade das labels

**Feito:** só `container`, `servico`, `ambiente`, `origem` e `level` viram
label. `msg`, `alerta` e o resto ficam no corpo da linha.

**Por quê:** no Loki, cada combinação distinta de labels cria um fluxo
separado, com seus próprios chunks e índice. Promover um campo de alta
cardinalidade (ID de requisição, IP, path com parâmetro) multiplica os fluxos
por milhares e derruba o Loki — é o erro mais comum de quem vem do
Elasticsearch, onde indexar tudo é o comportamento normal.

### 9.4 O socket do Docker atrás de um proxy

**Feito:** o Promtail fala com um `docker-socket-proxy` que só libera leitura
(`CONTAINERS=1`, `NETWORKS=1`, `POST=0`); só o proxy tem o socket montado.

**Por quê:** montar `/var/run/docker.sock` num container é equivalente a dar
root no host para ele — quem fala com o socket cria container privilegiado e
monta o filesystem inteiro. E o Promtail é justamente o processo que parseia
conteúdo não confiável (linhas de log), ou seja, o que menos deveria ter esse
poder.

O `:ro` no mount do socket é cosmético e não conta como mitigação: a restrição
se aplica ao arquivo, não às chamadas de API que passam por ele. O proxy é a
mitigação real, e o teste de aceitação verifica que o Promtail não tem o
socket montado.

**Custo assumido:** mais um container na stack. Em troca, o componente que lê
dado não confiável perde a capacidade de criar containers.

### 9.5 Horário do registro, não da coleta

**Feito:** `pipeline_stages` extrai o campo `time` do JSON do `slog` e o usa
como timestamp da linha.

**Por quê:** sem isso, a linha entra no Loki com a hora em que o Promtail a
leu. Um atraso de coleta desloca o log em relação à métrica, e a correlação no
Grafana — que é a razão de existir de ter os dois no mesmo lugar — passa a
mentir justamente durante um incidente, que é quando o atraso é maior.

---

## 10. O que eu faria diferente em produção

Sendo honesto sobre os limites deste desafio:

| Aqui | Em produção |
|---|---|
| Senha do Grafana em texto no `group_vars`; webhook do Discord no `.env` | Ansible Vault, ou um cofre externo (Vault, AWS Secrets Manager) |
| HTTP puro na porta 80 | TLS com certificado gerenciado, redirecionamento 301, HSTS |
| Prometheus com armazenamento local | Thanos ou Mimir para retenção longa e alta disponibilidade |
| Um Alertmanager só | Três em cluster, que deduplicam entre si — hoje ele é ponto único de falha bem na hora que importa |
| Uma réplica da aplicação | Várias réplicas atrás de um balanceador, deploy sem downtime |
| Loki gravando em disco local | Object store (S3/GCS) e Loki distribuído, para retenção longa e busca sob carga |
| Log e métrica correlacionados por tempo e serviço | OpenTelemetry com trace ID propagado, para correlacionar por requisição |
| Compose num host único | Kubernetes ou ECS, com autoescala e agendamento |
| Testes dentro do Dockerfile | Estágio dedicado no CI, com relatório de cobertura e SAST |
| Imagem construída no alvo | Registry privado, imagem assinada, varredura de vulnerabilidades |
