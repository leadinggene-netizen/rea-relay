#!/usr/bin/env bash
# Fire a single test instruction at the relay's /webhook endpoint.
# Targets the built-in demo connection (demo-connection-key, symbol XAUUSD).
# Usage:  ./send-test-webhook.sh https://relay.example.com [open_long] [0.01]
set -euo pipefail
RELAY="${1:-https://relay.example.com}"
ACTION="${2:-open_long}"
LOTS="${3:-0.01}"

read -r -d '' BODY <<JSON || true
{"connection_key":"demo-connection-key","action":"${ACTION}","symbol":"XAUUSD","sizing":{"mode":"fixed_lots","value":${LOTS}},"schema_version":1}
JSON

echo "POST ${RELAY}/webhook"
echo "${BODY}"
curl -sS -X POST "${RELAY}/webhook" -H "Content-Type: application/json" -d "${BODY}"
echo
