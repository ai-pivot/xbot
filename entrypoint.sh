#!/bin/sh
set -e

# PostgreSQL DSN is supplied at runtime via env var — never hard-code
# credentials in the image. Default targets a local PG for local testing.
: "${XBOT_PG_DSN:=postgres://postgres:postgres@127.0.0.1:5432/xbot?sslmode=disable}"

# Create config.json at runtime — no volume mount needed
cat > /app/.xbot/config.json << CFGEOF
{
  "server": {"port": 8083},
  "llm": {
    "provider": "openai",
    "base_url": "",
    "api_key": "",
    "model": "gpt-4o"
  },
  "agent": {
    "work_dir": "/app",
    "max_iterations": 50,
    "memory_provider": "flat"
  },
  "web": {
    "enable": true,
    "port": 8083,
    "static_dir": "/app/web/dist"
  },
  "storage": {
    "type": "pg",
    "dsn": "${XBOT_PG_DSN}"
  },
  "log": {
    "level": "info"
  }
}
CFGEOF

# Create .xbot directory structure
mkdir -p /app/.xbot/sessions /app/.xbot/logs /app/.xbot/memory

export XBOT_HOME=/app/.xbot

exec /app/xbot serve --config /app/.xbot/config.json
