#!/usr/bin/env bash
# Fire a single test instruction at the relay's /webhook endpoint.
# Targets the built-in demo connection (demo-connection-key).
# Default symbol is BTCUSD (crypto trades 24/7); use XAUUSD when gold is open.
# Usage:  ./send-test-webhook.sh https://relay.mt5tradebot.com [BTCUSD] [open_long] [0.01]
set -euo pipefail
RELAY="${1:-https://relay.mt5tradebot.com}"
SYMBOL="${2:-BTCUSD}"
ACTION="${3:-open_long}"
LOTS="${4:-0.01}"

BODY="{\"connection_key\":\"demo-connection-key\",\"action\":\"${ACTION}\",\"symbol\":\"${SYMBOL}\",\"sizing\":{\"mode\":\"fixed_lots\",\"value\":${LOTS}},\"schema_version\":1}"

echo "POST ${RELAY}/webhook"
echo "${BODY}"
curl -sS -X POST "${RELAY}/webhook" -H "Content-Type: application/json" -d "${BODY}"
echo
