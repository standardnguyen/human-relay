#!/bin/bash
set -euo pipefail
cd /opt/human-relay
git pull origin main
docker compose build --no-cache
docker compose up -d
docker image prune -f
