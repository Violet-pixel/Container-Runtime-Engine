#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null; then
  echo "Instale o Go 1.24 ou mais novo: https://go.dev/dl/" >&2
  exit 1
fi
if ! command -v gcc >/dev/null; then
  echo "Instale um compilador C (no Ubuntu/Debian: sudo apt install build-essential)" >&2
  exit 1
fi
for bin in ip nsenter iptables; do
  command -v "$bin" >/dev/null || echo "aviso: '$bin' não encontrado (instale iproute2, util-linux e iptables)" >&2
done

if command -v npm >/dev/null && [ "${SKIP_UI:-0}" != "1" ]; then
  (cd web && npm ci && npm run build)
fi

CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o bin/silo ./cmd/silo
sudo install -m 0755 bin/silo /usr/local/bin/silo

if [ "${WITH_SERVICE:-0}" = "1" ] && command -v systemctl >/dev/null; then
  sudo install -m 0644 deploy/silo.service /etc/systemd/system/silo.service
  sudo systemctl daemon-reload
  sudo systemctl enable --now silo
  echo "Serviço ativo em http://127.0.0.1:7070"
fi

echo "Silo instalado: $(silo version)"
