#!/bin/bash
set -euo pipefail
cd /opt/human-relay
git fetch origin main
git reset --hard origin/main
docker compose build --no-cache
/usr/local/bin/bao-env services/human-relay -o /opt/human-relay/.env
chmod 600 /opt/human-relay/.env
docker compose up -d
docker image prune -f
