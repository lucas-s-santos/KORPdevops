#!/usr/bin/env python3
# ===========================================================================
# Notificador de alertas do Projeto Korp
#
# Recebe os webhooks do Alertmanager, registra cada alerta em log estruturado
# (que o Promtail entrega ao Loki), conta cada disparo numa métrica que o
# Prometheus coleta, e reencaminha para o Discord quando houver URL.
#
# Por que existe, em vez de usar `discord_configs` direto no Alertmanager:
#
#   1. O Alertmanager NÃO expande variáveis de ambiente no arquivo de
#      configuração. Usar o receiver nativo obrigaria a commitar a URL do
#      webhook em texto puro no YAML ou a gerar esse YAML por template — no
#      primeiro caso o segredo vaza para o Git, no segundo o arquivo
#      versionado deixa de ser o que roda. Aqui o segredo vive só no `.env`.
#
#   2. O alerta vira métrica e log. Alerta que dispara e some não deixa
#      histórico; com `korp_alertas_recebidos_total` dá para ver no Grafana
#      quantas vezes cada alerta tocou, e correlacionar com o incidente.
#
#   3. Trocar o destino (Telegram, PagerDuty, um webhook interno) passa a ser
#      uma mudança aqui, sem reiniciar o Alertmanager.
#
# Escrito só com a biblioteca padrão de propósito: sem `pip install`, a
# imagem `python:3.12-alpine` sobe como está — sem build, sem lockfile e sem
# árvore de dependências para auditar por CVE.
# ===========================================================================

import json
import os
import sys
import threading
import urllib.error
import urllib.request
from collections import defaultdict
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORTA = int(os.environ.get("PORTA", "9094"))
DISCORD_URL = os.environ.get("DISCORD_WEBHOOK_URL", "").strip()
TIMEOUT_ENVIO = float(os.environ.get("TIMEOUT_ENVIO", "5"))

# Corpo máximo aceito no POST. O Alertmanager agrupa alertas, então o payload
# pode crescer; o limite existe para que um cliente qualquer não consiga
# travar o processo mandando um corpo infinito.
CORPO_MAXIMO = 1024 * 1024

# Cores dos embeds do Discord, por severidade — as mesmas labels usadas nas
# regras de alerta do Prometheus.
CORES = {
    "critico": 0xE01E5A,  # vermelho
    "alto": 0xF2A93B,     # laranja
    "aviso": 0xECB22E,    # amarelo
}
COR_RESOLVIDO = 0x2EB67D  # verde

_trava = threading.Lock()
_alertas_recebidos = defaultdict(int)   # (alerta, severidade, status) -> n
_notificacoes = defaultdict(int)        # (destino, resultado)         -> n


def log(nivel, mensagem, **campos):
    """Log estruturado em JSON, no mesmo formato do slog da aplicação Go.

    Escrever JSON em stdout é o que permite ao Promtail extrair os campos
    como labels no Loki, em vez de tratar a linha como texto solto.
    """
    registro = {
        "time": datetime.now(timezone.utc)
        .isoformat(timespec="milliseconds")
        .replace("+00:00", "Z"),
        "level": nivel,
        "msg": mensagem,
        "componente": "notificador",
    }
    registro.update(campos)
    print(json.dumps(registro, ensure_ascii=False), flush=True)


def escapar_label(valor):
    """Escapa um valor de label no formato de exposição do Prometheus."""
    return str(valor).replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n")


def render_metricas():
    """Monta o texto no formato de exposição do Prometheus.

    Feito à mão porque a `prometheus_client` seria a única dependência da
    imagem — e o formato é trivial: um comentário HELP, um comentário TYPE e
    uma linha por combinação de labels.
    """
    with _trava:
        recebidos = dict(_alertas_recebidos)
        enviadas = dict(_notificacoes)

    linhas = [
        "# HELP korp_notificador_up 1 enquanto o notificador de alertas está de pé.",
        "# TYPE korp_notificador_up gauge",
        "korp_notificador_up 1",
        "# HELP korp_alertas_recebidos_total Alertas recebidos do Alertmanager, por alerta, severidade e status.",
        "# TYPE korp_alertas_recebidos_total counter",
    ]
    for (alerta, severidade, status), total in sorted(recebidos.items()):
        linhas.append(
            'korp_alertas_recebidos_total{alerta="%s",severidade="%s",status="%s"} %d'
            % (escapar_label(alerta), escapar_label(severidade), escapar_label(status), total)
        )

    linhas.append(
        "# HELP korp_notificacoes_enviadas_total Tentativas de envio para destinos externos, por resultado."
    )
    linhas.append("# TYPE korp_notificacoes_enviadas_total counter")
    for (destino, resultado), total in sorted(enviadas.items()):
        linhas.append(
            'korp_notificacoes_enviadas_total{destino="%s",resultado="%s"} %d'
            % (escapar_label(destino), escapar_label(resultado), total)
        )

    return "\n".join(linhas) + "\n"


def montar_embed(alerta):
    """Traduz um alerta do Alertmanager num embed do Discord."""
    labels = alerta.get("labels") or {}
    anotacoes = alerta.get("annotations") or {}
    status = alerta.get("status", "firing")
    severidade = labels.get("severidade", "desconhecida")
    nome = labels.get("alertname", "alerta-sem-nome")

    if status == "resolved":
        titulo = "[RESOLVIDO] %s" % nome
        cor = COR_RESOLVIDO
    else:
        titulo = "[%s] %s" % (severidade.upper(), nome)
        cor = CORES.get(severidade, 0x95A5A6)

    campos = [
        {"name": "Severidade", "value": severidade, "inline": True},
        {
            "name": "Serviço",
            "value": labels.get("servico", labels.get("job", "-")),
            "inline": True,
        },
    ]
    if labels.get("instance"):
        campos.append({"name": "Instância", "value": labels["instance"], "inline": False})

    return {
        "title": titulo,
        "description": anotacoes.get("descricao", anotacoes.get("resumo", "sem descrição")),
        "color": cor,
        "fields": campos,
        "footer": {"text": "Projeto Korp - Alertmanager"},
        "timestamp": datetime.now(timezone.utc).isoformat(),
    }


def enviar_discord(alertas):
    """Posta os alertas no Discord. Falha de envio nunca derruba o processo.

    Um alerta perdido é ruim; um notificador que morre por causa de um 429 do
    Discord é pior — dali em diante nenhum alerta chega. Por isso a exceção é
    registrada e contabilizada, e o webhook responde 200 de qualquer forma.
    """
    if not DISCORD_URL:
        return

    # O Discord aceita no máximo 10 embeds por mensagem.
    corpo = json.dumps({"embeds": [montar_embed(a) for a in alertas[:10]]}).encode("utf-8")
    requisicao = urllib.request.Request(
        DISCORD_URL,
        data=corpo,
        headers={"Content-Type": "application/json", "User-Agent": "korp-notificador/1.0"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(requisicao, timeout=TIMEOUT_ENVIO) as resposta:
            with _trava:
                _notificacoes[("discord", "sucesso")] += 1
            log("info", "notificacao enviada", destino="discord", codigo=resposta.status)
    except urllib.error.HTTPError as erro:
        with _trava:
            _notificacoes[("discord", "erro_http")] += 1
        log("error", "Discord recusou a notificacao", destino="discord", codigo=erro.code)
    except Exception as erro:  # rede fora, DNS, timeout
        with _trava:
            _notificacoes[("discord", "erro_rede")] += 1
        log("error", "falha ao enviar notificacao", destino="discord", erro=str(erro))


def registrar(payload):
    """Contabiliza e loga cada alerta do lote recebido."""
    alertas = payload.get("alerts") or []
    for alerta in alertas:
        labels = alerta.get("labels") or {}
        anotacoes = alerta.get("annotations") or {}
        chave = (
            labels.get("alertname", "desconhecido"),
            labels.get("severidade", "desconhecida"),
            alerta.get("status", "firing"),
        )
        with _trava:
            _alertas_recebidos[chave] += 1

        log(
            "warn" if chave[2] == "firing" else "info",
            anotacoes.get("resumo", "alerta recebido"),
            alerta=chave[0],
            severidade=chave[1],
            status=chave[2],
            descricao=anotacoes.get("descricao", ""),
            instancia=labels.get("instance", ""),
        )
    return alertas


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    # O log de acesso padrão do http.server escreve texto solto em stderr e
    # polui a saída estruturada; silenciado aqui.
    def log_message(self, formato, *args):
        pass

    def _responder(self, codigo, corpo, tipo="text/plain; charset=utf-8"):
        dados = corpo.encode("utf-8")
        self.send_response(codigo)
        self.send_header("Content-Type", tipo)
        self.send_header("Content-Length", str(len(dados)))
        self.end_headers()
        self.wfile.write(dados)

    def do_GET(self):
        if self.path.startswith("/metrics"):
            self._responder(200, render_metricas(), "text/plain; version=0.0.4; charset=utf-8")
        elif self.path.startswith("/healthz"):
            self._responder(200, '{"status":"ok"}', "application/json")
        else:
            self._responder(404, '{"erro":"rota não encontrada"}', "application/json")

    def do_POST(self):
        if not self.path.startswith("/alertas"):
            self._responder(404, '{"erro":"rota não encontrada"}', "application/json")
            return

        try:
            tamanho = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            tamanho = 0

        if tamanho <= 0 or tamanho > CORPO_MAXIMO:
            self._responder(400, '{"erro":"corpo ausente ou grande demais"}', "application/json")
            return

        try:
            payload = json.loads(self.rfile.read(tamanho).decode("utf-8"))
        except (json.JSONDecodeError, UnicodeDecodeError) as erro:
            log("error", "payload invalido no webhook", erro=str(erro))
            self._responder(400, '{"erro":"json invalido"}', "application/json")
            return

        alertas = registrar(payload)

        # O watchdog dispara a cada 5 minutos, para sempre, de propósito.
        # Mandá-lo ao Discord encheria o canal de ruído e treinaria todo mundo
        # a ignorar as notificações — matando justamente o alerta de verdade
        # que aparecesse no meio. Ele fica no log e na métrica, que é onde o
        # silêncio dele é detectável.
        externos = [
            a for a in alertas
            if (a.get("labels") or {}).get("severidade") != "watchdog"
        ]
        if externos:
            enviar_discord(externos)

        # Sempre 200: o Alertmanager reenfileira e repete o lote inteiro em
        # caso de erro, e alerta duplicado no canal é pior que um envio
        # perdido que já ficou registrado no log e na métrica.
        self._responder(
            200, '{"status":"recebido","alertas":%d}' % len(alertas), "application/json"
        )


def main():
    servidor = ThreadingHTTPServer(("", PORTA), Handler)
    log(
        "info",
        "notificador iniciado",
        porta=PORTA,
        destino_discord="configurado" if DISCORD_URL else "não configurado",
    )
    try:
        servidor.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        # `docker stop` manda SIGTERM; o encerramento explicito fecha o socket
        # e termina as conexões em curso, em vez de morrer no SIGKILL.
        servidor.server_close()
        log("info", "notificador encerrado")
    return 0


if __name__ == "__main__":
    sys.exit(main())
