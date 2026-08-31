#!/usr/bin/env sh
# swarm.sh — launcher unico e identico per ogni PC.
#
# Lanci sempre lo stesso comando su tutti i dispositivi:
#     ./swarm.sh
#
# Lo script capisce DA SOLO il ruolo:
#   • se su questo PC c'è un modello locale (llama-server raggiungibile) →
#     diventa HOST: serve lo swarm e accende la pagina web su :8090.
#   • altrimenti → diventa NODO: si unisce allo swarm sulla LAN (mDNS) e usa
#     il modello dell'host.
#
# Per SOLO USARE lo swarm da un altro PC/telefono non serve nemmeno questo
# script: apri il browser su  http://<IP-DELL-HOST>:8090
#
# Reti che bloccano il multicast (mDNS): passa il token stampato dall'host:
#     ./swarm.sh <TOKEN>
set -e

DIR=$(cd "$(dirname "$0")" && pwd)
BIN="$DIR/swarmai"
GATEWAY_PORT=8090

# --- 1) assicura il binario -------------------------------------------------
if [ ! -x "$BIN" ]; then
  if command -v go >/dev/null 2>&1; then
    echo "Compilo swarmai (una volta sola)…"
    (cd "$DIR" && go build -o swarmai .)
  else
    echo "Manca il binario 'swarmai' e Go non è installato su questo PC."
    echo "Opzione 1: installa Go da https://go.dev/dl e rilancia ./swarm.sh"
    echo "Opzione 2: copia qui il file 'swarmai' già compilato da un PC gemello (stesso OS)."
    exit 1
  fi
fi

# --- 2) rileva un modello locale (endpoint OpenAI di llama-server) ----------
detect_llama() {
  if [ -n "$SWARMAI_LLAMA_URL" ] && \
     curl -fsS --max-time 2 "$SWARMAI_LLAMA_URL/v1/models" >/dev/null 2>&1; then
    printf '%s' "$SWARMAI_LLAMA_URL"; return 0
  fi
  for url in http://127.0.0.1:8080 http://127.0.0.1:8081 http://127.0.0.1:1234 http://127.0.0.1:11434; do
    if curl -fsS --max-time 2 "$url/v1/models" >/dev/null 2>&1; then
      printf '%s' "$url"; return 0
    fi
  done
  return 1
}

lan_ip() {
  if command -v ip >/dev/null 2>&1; then
    ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="src"){print $(i+1);exit}}'
  elif command -v ipconfig >/dev/null 2>&1; then
    ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null
  fi
}

IP=$(lan_ip); [ -n "$IP" ] || IP="<IP-di-questo-PC>"

# --- 3) avvia con il ruolo giusto ------------------------------------------
if LLAMA=$(detect_llama); then
  echo "=================================================================="
  echo "  RUOLO: HOST (questo PC ha il modello e serve lo swarm)"
  echo "  Modello via: $LLAMA"
  echo ""
  echo "  >> Dagli altri PC/telefoni apri semplicemente:"
  echo "        http://$IP:$GATEWAY_PORT"
  echo "=================================================================="
  "$BIN" node start --llama "$LLAMA" --gateway ":$GATEWAY_PORT" &
  NODE_PID=$!
  trap 'kill "$NODE_PID" 2>/dev/null' INT TERM
  # Stampa il token d'invito (serve solo su reti che bloccano mDNS).
  ( sleep 3
    TOK=$("$BIN" invite 2>/dev/null | sed -n 's/.*"token": *"\([^"]*\)".*/\1/p' | head -1)
    [ -n "$TOK" ] && {
      echo ""
      echo "  Token per altri NODI (solo se la LAN blocca mDNS): ./swarm.sh $TOK"
      echo ""
    }
  ) &
  wait "$NODE_PID"
else
  echo "=================================================================="
  echo "  RUOLO: NODO (nessun modello qui; uso lo swarm sulla LAN)"
  echo "  Cerco l'host via mDNS sulla rete locale…"
  echo ""
  echo "  Suggerimento: per SOLO usare lo swarm ti basta il browser →"
  echo "        http://<IP-DELL-HOST>:$GATEWAY_PORT   (niente da installare)"
  echo "=================================================================="
  if [ -n "$1" ]; then
    "$BIN" node start --join "$1" &
  else
    "$BIN" node start &
  fi
  NODE_PID=$!
  trap 'kill "$NODE_PID" 2>/dev/null' INT TERM
  wait "$NODE_PID"
fi
