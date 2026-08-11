#!/bin/bash
# Generic single-host deploy: pull, rebuild, restart.
# Site-specific deployment (env rendering, cron installs, compose overrides)
# belongs in a private site layer invoked by CI's site deploy hook — see the
# deploy job in .forgejo/workflows/ci.yml.
set -euo pipefail
cd "$(dirname "$0")"
git fetch origin main
git reset --hard origin/main
docker compose build --no-cache
docker compose up -d
docker image prune -f
