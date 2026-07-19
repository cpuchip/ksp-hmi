# run-capcom.ps1 — launch the CAPCOM voice bot on port 7862 (desktop-first).
#
# Prereqs:
#   - loom serve is up with a `capcom` seat (sonnet#capcom). See, on the host,
#     ~/.stewards/capcom-seat-README.md.
#   - The live KSP game is running with the kRPC server started (localhost:50000).
#   - First run downloads the Whisper model + Kokoro voice (a one-time wait) and
#     sets up the venv.
#
# Then open  http://localhost:7862  on THIS machine, allow the mic, click connect,
# and talk. (localhost is a secure context, so the browser mic works over plain http.
# For a phone, bind -Host 0.0.0.0 and reach it over TLS or a native WebRTC client.)

param(
  [string]$VenvHost = "localhost",
  [int]$Port = 7862
)

$ErrorActionPreference = "Stop"
Set-Location -Path $PSScriptRoot

if (-not (Test-Path ".venv")) {
  Write-Host "Creating venv + installing deps (one-time)..."
  uv venv
  uv pip install -r requirements.txt
}
if (-not (Test-Path ".env")) {
  Copy-Item ".env.example" ".env"
  Write-Host "Copied .env.example -> .env — edit it if your loom serve isn't on localhost:7791."
}

Write-Host "CAPCOM coming up on http://$VenvHost`:$Port  (open it, allow the mic, connect, talk)"
uv run capcom_bot.py --host $VenvHost --port $Port
