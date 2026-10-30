# Fire a single test instruction at the relay's /webhook endpoint.
# Targets the built-in demo connection (demo-connection-key, symbol XAUUSD).
# Unsigned (the demo connection does not require a signature).
#
# Usage:   .\send-test-webhook.ps1 -RelayUrl "https://relay.example.com"
param(
  [string]$RelayUrl = "https://relay.example.com",
  [string]$Action   = "open_long",
  [double]$Lots     = 0.01
)

$body = @{
  connection_key = "demo-connection-key"
  action         = $Action
  symbol         = "XAUUSD"
  sizing         = @{ mode = "fixed_lots"; value = $Lots }
  schema_version = 1
} | ConvertTo-Json -Depth 5

Write-Host "POST $RelayUrl/webhook"
Write-Host $body
Invoke-RestMethod -Method Post -Uri "$RelayUrl/webhook" -ContentType "application/json" -Body $body | ConvertTo-Json -Depth 8
