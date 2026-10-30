# Fire a single test instruction at the relay's /webhook endpoint.
# Targets the built-in demo connection (demo-connection-key).
# Unsigned (the demo connection does not require a signature).
# Default symbol is BTCUSD (crypto trades 24/7); use XAUUSD when gold is open.
#
# Usage:   .\send-test-webhook.ps1 -RelayUrl "https://relay.mt5tradebot.com" -Symbol BTCUSD -Action open_long -Lots 0.01
param(
  [string]$RelayUrl = "https://relay.mt5tradebot.com",
  [string]$Symbol   = "BTCUSD",
  [string]$Action   = "open_long",
  [double]$Lots     = 0.01
)

$body = @{
  connection_key = "demo-connection-key"
  action         = $Action
  symbol         = $Symbol
  sizing         = @{ mode = "fixed_lots"; value = $Lots }
  schema_version = 1
} | ConvertTo-Json -Depth 5

Write-Host "POST $RelayUrl/webhook"
Write-Host $body
Invoke-RestMethod -Method Post -Uri "$RelayUrl/webhook" -ContentType "application/json" -Body $body | ConvertTo-Json -Depth 8
