#!/usr/bin/env bash
# Разворачивает/сносит любую локальную ветку этого репозитория на тестовом
# VPS для ручной проверки. Запускать ЛОКАЛЬНО (Git Bash), из корня репо.
# GitHub не участвует — код уходит прямо с вашей машины через `git archive`
# по SSH, ровно так, как коммит будет выглядеть в ветке (некоммиченные
# правки не попадут — сначала закоммитьте то, что хотите проверить).
#
# Использование:
#   scripts/vps-test.sh init                 # один раз: токены на VPS
#   scripts/vps-test.sh start [ветка]        # поднять (по умолчанию — текущая ветка)
#   scripts/vps-test.sh stop                 # остановить, данные/сертификат оставить
#   scripts/vps-test.sh wipe                 # остановить и снести всё подчистую
#   scripts/vps-test.sh status               # что сейчас крутится
#   scripts/vps-test.sh logs                 # логи бота (Ctrl+C — выйти)
set -euo pipefail

VPS_USER_HOST="vlad@194.58.39.250"
VPS_PORT=6665
REMOTE_DIR="cs2-predictor-branch-test"
SECRETS_FILE=".cs2predictor-secrets.env"   # в $HOME на VPS, переживает start/stop

ssh_() { ssh -o BatchMode=yes -p "$VPS_PORT" "$VPS_USER_HOST" "$@"; }

cmd="${1:-}"

case "$cmd" in
  init)
    : "${TELEGRAM_BOT_TOKEN:?TELEGRAM_BOT_TOKEN=... нужен только для init}"
    : "${PANDASCORE_TOKEN:?PANDASCORE_TOKEN=... нужен только для init}"
    ssh_ "cat > \$HOME/$SECRETS_FILE <<EOF
TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
TELEGRAM_WEBHOOK_SECRET=\$(openssl rand -hex 32)
PANDASCORE_TOKEN=${PANDASCORE_TOKEN}
POSTGRES_PASSWORD=\$(openssl rand -hex 24)
CADDY_DOMAIN=\$(curl -fsS https://api.ipify.org).nip.io
EOF
    chmod 600 \$HOME/$SECRETS_FILE
    echo Secrets saved on the VPS, reused by every future start."
    ;;

  start)
    branch="${2:-$(git rev-parse --abbrev-ref HEAD)}"
    ssh_ "test -f \$HOME/$SECRETS_FILE" || { echo "Сначала: scripts/vps-test.sh init  (один раз)"; exit 1; }
    echo "Отправляю ветку '$branch' на VPS..."
    ssh_ "rm -rf \$HOME/$REMOTE_DIR && mkdir -p \$HOME/$REMOTE_DIR"
    git archive "$branch" | ssh -o BatchMode=yes -p "$VPS_PORT" "$VPS_USER_HOST" "tar -x -C \$HOME/$REMOTE_DIR"
    echo "Собираю образ и поднимаю стек..."
    ssh_ "cd \$HOME/$REMOTE_DIR && docker build -f docker/Dockerfile -t cs2predictor:branch-test . && \
      cd docker && \
      set -a && source \$HOME/$SECRETS_FILE && set +a && \
      APP_IMAGE=cs2predictor:branch-test docker compose --project-directory . --file compose.prod.yml --env-file \$HOME/$SECRETS_FILE up -d"
    echo "Жду /healthz/ready..."
    ssh_ "set -a && source \$HOME/$SECRETS_FILE && set +a && \
      for i in \$(seq 1 30); do curl -fsS \"https://\${CADDY_DOMAIN}/healthz/ready\" >/dev/null 2>&1 && break; sleep 5; done && \
      curl -sS -X POST \"https://api.telegram.org/bot\${TELEGRAM_BOT_TOKEN}/setWebhook\" \
        -d \"url=https://\${CADDY_DOMAIN}/telegram/webhook\" \
        -d \"secret_token=\${TELEGRAM_WEBHOOK_SECRET}\" \
        -d 'allowed_updates=[\"message\",\"callback_query\",\"poll_answer\",\"my_chat_member\"]' && \
      echo && echo \"Готово: https://\${CADDY_DOMAIN}\""
    ;;

  stop)
    ssh_ "cd \$HOME/$REMOTE_DIR/docker 2>/dev/null && \
      set -a && source \$HOME/$SECRETS_FILE && set +a && \
      APP_IMAGE=cs2predictor:branch-test docker compose --project-directory . --file compose.prod.yml --env-file \$HOME/$SECRETS_FILE down" \
      || echo "Нечего останавливать — стек уже не поднят."
    ;;

  wipe)
    ssh_ "cd \$HOME/$REMOTE_DIR/docker 2>/dev/null && \
      set -a && source \$HOME/$SECRETS_FILE && set +a && \
      APP_IMAGE=cs2predictor:branch-test docker compose --project-directory . --file compose.prod.yml --env-file \$HOME/$SECRETS_FILE down -v" \
      || echo "Нечего сносить."
    echo "Данные и сертификат снесены. Секреты (\$HOME/$SECRETS_FILE) не тронуты — следующий start сработает без init."
    ;;

  status)
    ssh_ "docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}' | grep -E 'cs2predictor|NAMES' || echo 'ничего не запущено'"
    ;;

  logs)
    ssh_ "docker logs -f --tail 50 cs2predictor-bot-1"
    ;;

  *)
    echo "Использование: $0 {init|start [ветка]|stop|wipe|status|logs}"
    exit 1
    ;;
esac
