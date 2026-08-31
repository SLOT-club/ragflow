# swarm.ps1 — launcher unico e identico per Windows (equivalente di swarm.sh).
#
# Lanci sempre lo stesso comando su tutti i PC Windows:
#     .\swarm.ps1
#
# Lo script capisce DA SOLO il ruolo:
#   • se c'e' un modello locale (llama-server raggiungibile) -> HOST: serve lo
#     swarm e accende la pagina web su :8090.
#   • altrimenti -> NODO: si unisce allo swarm sulla LAN (mDNS).
#
# Per SOLO USARE lo swarm da un altro PC/telefono non serve nulla: apri il
# browser su  http://<IP-DELL-HOST>:8090
#
# Reti che bloccano mDNS: passa il token stampato dall'host:  .\swarm.ps1 <TOKEN>
param([string]$Token)
$ErrorActionPreference = 'Stop'
$DIR = Split-Path -Parent $MyInvocation.MyCommand.Path
$BIN = Join-Path $DIR 'swarmai.exe'
$GW  = 8090

if (-not (Test-Path $BIN)) {
  if (Get-Command go -ErrorAction SilentlyContinue) {
    Write-Host "Compilo swarmai (una volta sola)..."
    Push-Location $DIR; go build -o swarmai.exe .; Pop-Location
  } else {
    Write-Host "Manca swarmai.exe e Go non e' installato su questo PC."
    Write-Host "Opzione 1: installa Go da https://go.dev/dl e rilancia .\swarm.ps1"
    Write-Host "Opzione 2: copia qui il file 'swarmai.exe' da un PC gemello (Windows)."
    exit 1
  }
}

function Detect-Llama {
  $cands = @()
  if ($env:SWARMAI_LLAMA_URL) { $cands += $env:SWARMAI_LLAMA_URL }
  $cands += 'http://127.0.0.1:8080','http://127.0.0.1:8081','http://127.0.0.1:1234','http://127.0.0.1:11434'
  foreach ($u in $cands) {
    try { Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 "$u/v1/models" | Out-Null; return $u } catch {}
  }
  return $null
}

$ip = (Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
       Where-Object { $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' } |
       Select-Object -First 1 -ExpandProperty IPAddress)
if (-not $ip) { $ip = '<IP-di-questo-PC>' }

$llama = Detect-Llama
if ($llama) {
  Write-Host "=================================================================="
  Write-Host "  RUOLO: HOST (questo PC ha il modello e serve lo swarm)"
  Write-Host "  Modello via: $llama"
  Write-Host ""
  Write-Host "  >> Dagli altri PC/telefoni apri:  http://${ip}:$GW"
  Write-Host "  (token per reti senza mDNS: esegui  .\swarmai.exe invite  in un'altra finestra)"
  Write-Host "=================================================================="
  & $BIN node start --llama $llama --gateway ":$GW"
} else {
  Write-Host "=================================================================="
  Write-Host "  RUOLO: NODO (nessun modello qui; uso lo swarm sulla LAN)"
  Write-Host "  Per SOLO usare lo swarm basta il browser:  http://<IP-DELL-HOST>:$GW"
  Write-Host "=================================================================="
  if ($Token) { & $BIN node start --join $Token }
  else        { & $BIN node start }
}
