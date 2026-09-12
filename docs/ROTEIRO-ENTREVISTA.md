# Roteiro para a entrevista

Dois blocos: o **roteiro da demonstração** (o que rodar, em que ordem, o que
falar enquanto roda) e o **banco de perguntas** com respostas prontas.

---

# Parte A — Roteiro da demonstração

Tempo alvo: 14 a 16 minutos. Ensaie pelo menos uma vez inteiro antes.

## Antes de começar (5 minutos antes da chamada)

```bash
# 1. Ambiente limpo, para a demonstração ser real
cd ~/projeto-korp
make limpar          # derruba tudo, inclusive volumes e rede
docker system prune -f

# 2. Aqueça o cache de imagens (baixar 11 imagens ao vivo é tempo morto)
docker pull nginx:1.27-alpine
docker pull prom/prometheus:v2.55.1
docker pull grafana/grafana:11.3.0
docker pull prom/blackbox-exporter:v0.25.0
docker pull nginx/nginx-prometheus-exporter:1.3.0
docker pull curlimages/curl:8.10.1
docker pull golang:1.23-alpine
docker pull prom/alertmanager:v0.27.0
docker pull python:3.12-alpine
docker pull grafana/loki:3.3.2
docker pull grafana/promtail:3.3.2
docker pull tecnativa/docker-socket-proxy:0.3.0

# 3. Deixe abertos: terminal, navegador com as abas
#    localhost/projeto-korp, localhost:9090, localhost:3000,
#    localhost:9093 (Alertmanager) e localhost:3000/d/projeto-korp-slo
#
# 4. Se for demonstrar a notificação: DISCORD_WEBHOOK_URL no .env e o
#    Discord aberto no celular, com o canal já na tela.
```

## Passo 1 — O problema, em uma frase (30 s)

> "O desafio pede um serviço HTTP em Go, containerizado, atrás de um proxy
> reverso, monitorado e provisionado por Ansible. Vou mostrar o ambiente
> subindo do zero com um comando e depois abrir cada decisão."

Não leia o enunciado de volta para eles. Mostre logo.

## Passo 2 — Um comando provisiona tudo (3-4 min)

```bash
cd ansible
ansible-playbook -i inventory.ini site.yml
```

Enquanto roda, narre as roles conforme aparecem na tela:

- **docker** — "instala do repositório oficial, não o `docker.io` da distro,
  porque o oficial traz o Compose V2 como plugin."
- **rede** — "rede bridge dedicada, com sub-rede fixa. O ponto aqui é o DNS
  interno: sem uma rede definida pelo usuário, o NGINX não conseguiria
  resolver o nome do container."
- **aplicacao** — "build multi-stage; os testes Go rodam dentro do build,
  então não existe imagem com suíte quebrada."
- **nginx / monitoramento** — "todos os arquivos de configuração vêm do Git."
- **stack** — "sobe e espera todos os healthchecks ficarem verdes."
- **validacao** — "e aqui está a parte que eu mais gosto..."

Quando aparecer o bloco `RESPOSTA DO SERVIÇO`, **pare e mostre**:

> "O playbook não termina dizendo que provisionou. Ele faz uma requisição
> HTTP real, valida o contrato do JSON, faz uma segunda chamada dois segundos
> depois para provar que o horário é dinâmico, e imprime tudo. Se qualquer
> uma dessas afirmações falhar, o playbook falha."

## Passo 3 — O teste do enunciado (30 s)

```bash
curl http://localhost:80/projeto-korp
```

```json
{"nome":"Projeto Korp","horario":"2026-09-05T14:22:31Z"}
```

> "RFC 3339, com o `Z` no fim deixando explícito que é UTC."

## Passo 4 — Prove o isolamento de rede (1 min)

Este passo impressiona porque quase ninguém demonstra.

```bash
# A aplicação não publica porta nenhuma:
docker port http-server-projeto-korp        # saída vazia

# Direto na 8080 do host não tem ninguém:
curl --max-time 3 http://localhost:8080/projeto-korp
# curl: (7) Failed to connect

# Mas de dentro da rede ela responde:
docker run --rm --network korp-net curlimages/curl \
  -s http://http-server-projeto-korp:8080/projeto-korp
```

> "Só o NGINX é exposto. A aplicação existe apenas dentro da `korp-net`."

## Passo 5 — Métricas e Prometheus (1-2 min)

```bash
make carga                    # liga o gerador de tráfego
curl -s localhost/metrics | grep '^korp_' | head -20
```

Abra `http://localhost:9090/targets`:

> "Cinco jobs, todos verdes: a aplicação, o próprio Prometheus, o NGINX via
> exporter, e dois probes do blackbox."

Rode uma consulta em `http://localhost:9090/graph`:

```promql
sum(rate(korp_http_requests_total[1m]))
```

Mostre também `http://localhost:9090/alerts` — cinco regras carregadas.

## Passo 6 — O dashboard (2 min)

Abra `http://localhost:3000/d/projeto-korp` (admin/admin).

> "Datasource e dashboard são provisionados por arquivo — eu nunca cliquei
> em nada nessa interface. O JSON está versionado no Git."

Percorra as três primeiras linhas de painéis, agrupando por pergunta:
"está no ar", "quanto tráfego", "está rápido", "está com erro".

Pare no painel **Disponibilidade ao longo do tempo**:

> "Três linhas: `up`, que o Prometheus gera na coleta; `korp_service_up`, que
> a aplicação reporta de si mesma; e `probe_success`, que é uma requisição
> real passando pelo NGINX. Se as duas primeiras estão em 1 e a terceira em 0,
> o problema é o proxy, não a aplicação."

## Passo 7 — O momento decisivo: quebre o ambiente (2 min)

**Faça isso.** É o que separa uma demonstração de uma prova.

```bash
docker stop http-server-projeto-korp
```

No Grafana, em ~15 s: o cartão vira **FORA DO AR**, `probe_success` cai, o
`curl` passa a devolver 502. Em `localhost:9090/alerts`, o alerta
`ServicoIndisponivel` entra em `PENDING` e depois `FIRING`.

```bash
docker start http-server-projeto-korp
```

Tudo volta sozinho. E aqui vem o detalhe técnico forte:

> "Repare que o NGINX voltou a funcionar sem eu reiniciá-lo. Isso não é de
> graça: o NGINX resolve nomes de `upstream` uma única vez, na inicialização.
> Se eu recriasse o container, ele ganharia um IP novo e o NGINX continuaria
> mandando tráfego para o IP antigo — 502 até alguém reiniciar o proxy. Eu uso
> o resolvedor DNS do Docker, o `127.0.0.11`, com o hostname numa variável, o
> que força a reavaliação a cada 10 segundos."

## Passo 8 — Siga o alerta até o fim (2-3 min)

O passo anterior mostrou o alerta ficando vermelho na tela. Este mostra o
alerta **chegando em alguém** — que é a parte que a maioria dos projetos de
portfólio não tem.

Com a aplicação ainda parada (ou pare de novo com `make incidente`):

```bash
make alertas          # o que o Alertmanager tem ativo agora
make notificacoes     # o que efetivamente chegou ao notificador
```

Enquanto roda, a fala:

> "O Prometheus só avalia a regra. Quem agrupa, inibe e entrega é o
> Alertmanager. Repare no que **não** apareceu: quando a aplicação cai, quatro
> alertas disparam juntos — serviço fora, caminho fim-a-fim falhando, taxa de
> 5xx e latência. São quatro mensagens sobre o mesmo incidente. As regras de
> inibição entregam só a causa e seguram os sintomas, porque um canal que toca
> quatro vezes por incidente é silenciado pela equipe em duas semanas."

Se o webhook do Discord estiver configurado, **mostre o celular**. É o momento
mais forte da demonstração inteira.

```bash
make fim-incidente
```

> "E o alerta resolve sozinho em cerca de um minuto, com a notificação de
> resolvido."

### O detalhe que costuma impressionar

```bash
make slo
```

> "Além dos alertas de limiar, existem alertas por taxa de queima do orçamento
> de erro. O SLO é 99,9% em 30 dias, o que dá 43 minutos ruins por mês. Se a
> queima está em 14,4x, o orçamento do mês acaba em dois dias — isso acorda
> alguém. Se está em 1x, vira tarefa, não plantão. Cada alerta exige uma
> janela longa e uma curta ao mesmo tempo: a longa dá confiança de que é real,
> a curta faz o alerta limpar sozinho quando passa."

E, se sobrar fôlego, o que costuma render a melhor pergunta de volta:

> "Tem um alerta aqui que dispara sempre, de propósito: `PipelineDeAlertasViva`,
> cuja expressão é `vector(1)`. Ele não monitora o serviço, monitora o
> monitoramento. Se o Prometheus cair, todos os outros alertas param de chegar
> — e um canal silencioso é indistinguível de um canal saudável. O silêncio
> desse alerta é o alarme."

## Passo 9 — Métrica e log na mesma tela (1 min)

Abra o dashboard de SLO em `localhost:3000/d/projeto-korp-slo`.

> "Aqui embaixo estão os logs, vindos do Loki, na mesma tela dos gráficos. A
> métrica diz *o que* quebrou; o log diz *por quê*. Ter os dois no mesmo lugar
> é a diferença entre dois cliques e trocar de ferramenta no meio de um
> incidente."

Se perguntarem como o Promtail descobre os containers:

> "Pela API do Docker, não por glob nos arquivos de log — pelo caminho em disco
> o único identificador é o ID do container, que muda a cada recriação, e os
> painéis quebrariam no primeiro `--force-recreate`. Mas ele não tem o socket
> montado: montar o socket num container é dar root no host para ele, e o
> Promtail é justamente quem parseia conteúdo não confiável. Ele fala com um
> socket-proxy que só libera leitura de containers. O teste de aceitação
> verifica que o socket não está lá."

## Passo 10 — Idempotência (30 s)

```bash
ansible-playbook -i inventory.ini site.yml
```

> "Segunda execução: `changed=0`. A imagem só é reconstruída se o código
> mudou, o NGINX só recarrega se o `.conf` mudou."

## Passo 11 — Fechamento (30 s)

```bash
make testar     # 55 verificações, todas verdes
```

> "E esse script roda as mesmas verificações num pipeline de CI."

Termine oferecendo o assunto que você domina:

> "Posso abrir o Dockerfile, a configuração do NGINX, as regras de SLO ou o
> playbook, o que for mais útil."

---

# Parte B — Banco de perguntas

## Sobre Go

**Por que não usou um framework como Gin?**
São quatro rotas. O `ServeMux` do Go 1.22 já aceita padrões com método —
`"GET /projeto-korp"` — e devolve 405 sozinho quando o método não bate. Um
framework traria middlewares prontos, mas também uma árvore de dependências
para auditar e manter. Se o projeto crescesse para dezenas de rotas com
parâmetros e validação de corpo, eu iria de Chi.

**Como você testa o horário, se ele muda a cada chamada?**
Injetando o relógio. A struct `application` tem um campo
`now func() time.Time`; em produção é `time.Now`, no teste é uma função que
devolve uma data fixa. Uso propositalmente um horário em `-03:00` para provar
que a conversão para UTC acontece: a saída esperada é exatamente
`2026-09-05T15:30:45Z`. Tem também um teste com relógio que avança um segundo
por leitura, verificando que três chamadas devolvem três horários distintos.

**O que acontece se um handler entrar em pânico?**
O middleware `recoverPanic` captura, registra o stack trace no log
estruturado e devolve 500. Sem ele, um pânico numa goroutine de requisição
derruba o processo inteiro — o `net/http` recupera pânicos por conexão, mas o
comportamento padrão é fechar a conexão sem resposta útil.

**Por que timeouts explícitos no `http.Server`?**
Porque o padrão do Go é **sem timeout nenhum**. Um cliente que abre a conexão
e manda um cabeçalho por minuto segura um descritor de arquivo
indefinidamente — é o Slowloris. Algumas centenas dessas conexões esgotam os
FDs do processo.

**Por que `log/slog` em JSON?**
É o log estruturado da biblioteca padrão desde o Go 1.21. JSON é consumível
direto por Loki ou ELK sem regex de parsing, e cada campo (status, duração,
IP do cliente) vira um campo pesquisável.

## Sobre Docker

**Por que multi-stage?**
Para não levar o compilador para produção. A imagem do Go tem ~850 MB; a
final tem ~8 MB. Menos bytes para transferir e muito menos superfície de
ataque.

**Por que `scratch` e não `alpine`?**
`scratch` é uma imagem vazia. Sem shell, sem `apk`, sem libc. Se alguém
conseguir execução de comando dentro do container, não tem `sh` nem `curl`
para dar o próximo passo. Exige `CGO_ENABLED=0` para gerar um binário
estaticamente ligado.

**Mas aí como você faz o healthcheck, se não tem curl?**
O próprio binário faz. Ele aceita a flag `-healthcheck`: nesse modo chama
`/healthz` em si mesmo e sai com 0 ou 1. O `HEALTHCHECK` do Dockerfile invoca
essa flag. Custo zero de tamanho.

**Por que a forma exec no `ENTRYPOINT`?**
Na forma shell, o Docker executa `/bin/sh -c "comando"`. O `sh` vira PID 1 e
não repassa sinais aos filhos, então o SIGTERM do `docker stop` nunca chega
na aplicação e o shutdown gracioso nunca acontece — o container é morto a
SIGKILL depois de 10 segundos. Na forma exec, o binário é o PID 1 e recebe o
sinal diretamente.

**Como você garante que o build usa cache?**
Copiando `go.mod` e baixando as dependências **antes** de copiar o código.
Mudar um `.go` invalida só a camada de build, não a de download. Uso também
`--mount=type=cache` para o cache de módulos e de compilação do Go.

**Por que `read_only: true` funciona?**
Porque a aplicação não escreve nada em disco: só lê variáveis de ambiente e
responde HTTP. Se precisasse de arquivos temporários, eu montaria um `tmpfs`
em `/tmp`.

## Sobre rede

**Por que criar uma rede em vez de usar a `bridge` padrão?**
Pelo DNS. Numa rede definida pelo usuário, o Docker sobe um resolvedor em
`127.0.0.11` que traduz nome de container para IP. É o que permite o NGINX
escrever `proxy_pass http://http-server-projeto-korp:8080`. Na `bridge`
padrão não existe essa resolução — só o `--link`, obsoleto. Além disso, uma
rede própria isola o projeto de outros containers da máquina.

**Como o NGINX alcança a aplicação se ela não expõe portas?**
`ports` publica no host; `expose` só documenta. Dentro de uma mesma rede
Docker, os containers se enxergam em qualquer porta sem publicação nenhuma —
o tráfego nunca sai para a interface do host.

**Qual a diferença entre bridge, host e overlay?**
`bridge` cria uma rede virtual isolada com NAT — é o padrão para um host só.
`host` remove o isolamento: o container usa a pilha de rede do host
diretamente, mais rápido mas sem isolamento e com risco de conflito de portas.
`overlay` conecta containers em **hosts diferentes**, é o que Swarm e
Kubernetes usam por baixo.

## Sobre NGINX

**O que é proxy reverso e por que usar?**
Ele fica na frente do serviço e recebe as requisições em nome dele. Ganhos:
um único ponto de entrada, terminação de TLS, balanceamento entre réplicas,
cache, rate limit, e o serviço interno nunca fica exposto.

**Como funciona a sua configuração?**
Escuta na 80, resolve o nome do container pelo DNS do Docker, encaminha para
a 8080 preservando os cabeçalhos de origem (`X-Real-IP`, `X-Forwarded-For`,
`X-Forwarded-Proto`). Tem timeouts curtos, rate limit por IP, log de acesso em
JSON com os tempos do upstream, e um `server` separado na 8081 com
`stub_status` para o exporter — essa porta não é publicada no host.

**Por que a variável no `proxy_pass`?** *(a pergunta mais técnica que pode vir)*
Porque o NGINX resolve nomes de `upstream` uma única vez, na inicialização. Se
o container da aplicação for recriado, ele ganha um IP novo e o NGINX continua
mandando para o IP morto — 502 até reiniciarem o proxy. Com o `resolver` do
Docker e o hostname numa variável, o nome é reavaliado a cada 10 segundos e
ele se recupera sozinho. O preço é abrir mão do `keepalive` do bloco
`upstream`, o que para este volume não pesa.

**Qual a diferença entre `proxy_pass` com e sem barra no fim?**
Sem barra, o caminho da requisição é repassado inteiro. Com barra, a parte que
casou com o `location` é removida antes de repassar. Aqui uso
`$request_uri` explicitamente, que preserva o caminho e a query string sem
ambiguidade.

## Sobre monitoramento

**Como você mede disponibilidade?**
Em três camadas, porque cada uma falha por um motivo diferente:
`korp_service_up` é a aplicação dizendo que se considera saudável; `up` é o
Prometheus registrando se conseguiu raspar o alvo; e `probe_success` é o
blackbox-exporter fazendo uma requisição real através do NGINX. O caso
interessante é `up == 1` com `probe_success == 0`: a aplicação está bem e o
problema é o proxy.

**Por que contador e não gauge para volume?**
Contadores só sobem e zeram no restart. O `rate()` do PromQL entende esse
reinício e calcula a taxa corretamente. Se a aplicação calculasse "req/s"
num gauge, eu perderia informação entre raspagens e não poderia escolher a
janela na hora da consulta.

**Histograma ou summary para latência?**
Histograma. Ele é agregável entre instâncias: dá para somar os buckets de
várias réplicas e tirar um p99 global com `histogram_quantile()`. Summaries
calculam quantis no cliente e não podem ser somados — a média de dois p99 não
é o p99 do conjunto.

**O que é cardinalidade e por que você se preocupou com ela?**
Cardinalidade é o número de séries temporais distintas — uma para cada
combinação de labels. Se eu usasse `r.URL.Path` cru como label, um scanner
batendo em `/aaa`, `/bbb`, `/ccc` criaria uma série por URL e estouraria a
memória do Prometheus. A função `normalizeRoute` mapeia qualquer caminho fora
da lista conhecida para `"outros"`, deixando a cardinalidade limitada.

**Por que o Prometheus raspa a aplicação direto, e não pelo NGINX?**
Para não confundir falha do proxy com falha da aplicação. Coletando pela rede
interna, se o NGINX cair eu continuo tendo as métricas da aplicação — e o
contraste com o `probe_success` mostra exatamente onde está o problema.

**Como o Grafana já sobe com o dashboard pronto?**
Provisionamento por arquivo: `datasources.yml` define o datasource com um
`uid` fixo, `dashboards.yml` aponta uma pasta, e o JSON do dashboard fica
versionado no Git referenciando aquele `uid`. Zero cliques na interface — e
por isso o ambiente é reprodutível: derrubo tudo com `--volumes`, subo de
novo, e o Grafana volta idêntico.

**O que você monitoraria além disso?**
Os quatro sinais dourados do Google SRE: latência, tráfego, erros e saturação
— os quatro estão no dashboard. Além disso, em produção: SLO com orçamento de
erro, rastreamento distribuído com OpenTelemetry, logs centralizados no Loki
correlacionados pelo `X-Request-ID`, e métricas de negócio, não só técnicas.

## Sobre Ansible

**Por que Ansible e não um shell script?**
Idempotência e declaratividade. O script diz *como* fazer e roda tudo de novo
a cada execução; o Ansible diz *qual estado eu quero* e só age no que está
diferente. Além disso vem com inventário para múltiplos hosts, `--check` para
simular, tags para executar partes, e handlers para reagir a mudanças.

**O que é idempotência, na prática, aqui?**
A segunda execução do playbook devolve `changed=0`. A imagem só é reconstruída
se o código mudou; o NGINX só recarrega se o `.conf` mudou; o Prometheus só
recarrega se a configuração mudou.

**Diferença entre role, task, handler e playbook?**
Task é a unidade de trabalho. Role agrupa tasks, templates, arquivos e
variáveis com uma estrutura de diretórios padrão, e é reutilizável. Handler é
uma task que só roda se for notificada por uma mudança — é como o "recarregar
o NGINX" só acontece quando a configuração muda de verdade. Playbook amarra
hosts a roles.

**Como você garantiria o segredo do Grafana?**
Ansible Vault: `ansible-vault encrypt_string` para a senha, e a execução pede
a chave (`--ask-vault-pass`) ou lê de um arquivo protegido. Deixei em texto
aqui porque o ambiente é descartável e a avaliação precisa entrar — e isso
está registrado explicitamente na seção "o que eu faria diferente em
produção".

**Por que a validação está dentro do playbook?**
Porque "provisionou" e "funciona" precisam ser a mesma coisa. A role
`validacao` faz a requisição HTTP, afirma o contrato do JSON, prova que o
horário é dinâmico comparando duas chamadas, espera os alvos do Prometheus
ficarem `up` e confirma pela API do Grafana que o dashboard existe. Qualquer
falha aí derruba o playbook.

## Perguntas de arquitetura

**Como você escalaria isso?**
Horizontalmente: várias réplicas da aplicação, um bloco `upstream` no NGINX
com balanceamento, e sessão sem estado (que já é o caso). Passando disso, eu
sairia do Compose para Kubernetes — Deployment com HPA, Service, Ingress — e
manteria as mesmas métricas, que já são agregáveis entre réplicas justamente
por serem histogramas e contadores.

**E se o serviço precisasse de banco de dados?**
Entraria como outro serviço no Compose, na mesma rede, sem porta publicada.
O `/readyz` passaria a checar a conexão com o banco (o `/healthz` não —
reiniciar o container não conserta um banco fora do ar). Adicionaria métricas
de pool de conexões e o `pg_exporter` no Prometheus. Credenciais viriam de
secrets, não de variáveis de ambiente em texto.

**Como seria o deploy sem downtime?**
Com réplicas atrás do NGINX: sobe a nova versão, espera o healthcheck passar,
tira a antiga do balanceamento e derruba. O shutdown gracioso já implementado
é o que garante que as requisições em curso terminam. Em Kubernetes isso é o
rolling update com readiness probe.

**O que falta para isso ir para produção?**
TLS, segredos em cofre, registry privado com imagem assinada e varredura de
vulnerabilidades, Alertmanager, logs centralizados, retenção longa das
métricas, e backup do volume do Grafana. Está tudo listado na última seção do
`DECISOES-TECNICAS.md`.

---

## Se travar numa pergunta

Não invente. A resposta que funciona é:

> "Não sei de cabeça. Meu raciocínio seria [linha de investigação], e eu
> confirmaria [na documentação / testando assim]."

Demonstrar método vale mais do que acertar um detalhe de sintaxe.

## Três coisas para não esquecer de mencionar

1. **Quebrar o ambiente ao vivo** (Passo 7) — poucos candidatos fazem.
2. **O `resolver` do NGINX** — é o detalhe que mostra experiência real de
   operação, não só de tutorial.
3. **A validação que falha o playbook** — mostra que você pensa em garantia,
   não só em automação.
