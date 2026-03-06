#!/bin/bash
set -euo pipefail
cd /opt/human-relay
git fetch origin main
git reset --hard origin/main
docker compose build --no-cache
docker compose up -d
docker image prune -f
