# swarmai — rete P2P per la condivisione di hardware d'inferenza

Un nodo `swarmai` è un peer che scopre gli altri (mDNS in LAN, DHT Kademlia in WAN), **annuncia le
proprie capacità hardware** e sa sia **offrire** sia **consumare** inferenza sulla rete: una macchina
senza modello può prendere in prestito il calcolo di una che ce l'ha. È l'MVP-0 del Binario B descritto
in `research/swarm-ai-prototype-plan.md`.

Modulo Go autonomo (non tocca la build nativa di RAGFlow). Costruito su [libp2p](https://libp2p.io).

## Cosa fa già (verificato)
- **Identità persistente** per peer (Ed25519, stabile fra i riavvii).
- **Trasporti** TCP + QUIC; NAT port-map, relay e hole-punching abilitati per la WAN.
- **Scoperta**: mDNS in LAN (cellula di casa istantanea) + DHT Kademlia con rendezvous condiviso.
- **Gossip delle capacità** (GossipSub): ogni nodo pubblica una `CapabilityCard` (RAM, CPU, backend,
  modello servito, `can_infer`, politica di schedule).
- **Routing del calcolo**: `swarmai run "..."` risponde usando il miglior peer capace — se il nodo
  locale non ha un modello, la richiesta viene instradata via P2P a chi ce l'ha (protocollo di stream
  `/swarmai/infer/1.0.0`), e la risposta torna con l'id del peer che l'ha servita.
- **Backend d'inferenza**: adattatore `llama-server` (OpenAI-compatibile, con gli switch di compat —
  `max_tokens`, niente ruolo `developer`, `enable_thinking:false`); i nodi senza modello usano lo stub e
  restano utili come router/relay.
- **Streaming dei pesi (Stremio-style)**: protocollo `/swarmai/blob/1.0.0`. Un modello è spezzato in
  chunk content-addressed (SHA-256) descritti da un manifest anch'esso content-addressed. Un nodo
  annuncia i modelli che seeda nella capability card; un altro li **streama a pezzi dai peer** con
  finestra di prefetch, **verifica ogni chunk**, riassembla il file e a sua volta ridiventa seeder.
  Verificato: fetch byte-identico di un modello via P2P.

## Build
```bash
cd swarmai
go build -o swarmai .
```

## Uso
Nodo con un modello (avvia prima un `llama-server` su :8080):
```bash
swarmai node start --name pc-di-casa --llama http://127.0.0.1:8080 --model qwen3.5-4b
```
Nodo senza modello (userà il calcolo dei peer):
```bash
swarmai node start --name portatile --port 4879 \
  --bootstrap /ip4/<IP-del-primo-nodo>/tcp/4779/p2p/<PEER-ID>
```
In LAN il `--bootstrap` non serve: mDNS trova i peer da solo.

Interrogare e usare la rete:
```bash
swarmai status                 # scheda e indirizzi di questo nodo
swarmai peers                  # capacità dei peer scoperti
swarmai run "quanto fa 2+2"    # instradato al miglior nodo capace
```

Condividere e streamare un modello fra nodi:
```bash
swarmai model share ./qwen3.5-4b-q6_k.gguf   # su un nodo: chunk + seed, stampa il manifest id
swarmai model list                            # vedere chi seeda cosa
swarmai model fetch <manifest-id> -o ./m.gguf # su un altro nodo: streama a pezzi dai peer, verifica
```
`peers`/`status`/`run` parlano con la control API locale del demone (`127.0.0.1:<porta+1>`).

## Prova a due nodi in locale
```bash
HOME=/tmp/a swarmai node start --name A --port 4779 &
# copia il peer id di A, poi:
HOME=/tmp/b swarmai node start --name B --port 4879 --llama http://127.0.0.1:8080 --model m \
  --bootstrap /ip4/127.0.0.1/tcp/4779/p2p/<PEER-ID-DI-A> &
curl "http://127.0.0.1:4780/run?prompt=ciao"   # A (senza modello) -> servito da B
```

## Prossimi milestone (dal piano e dalla ricerca in `research/swarm-ai-tech-integration.md`)
1. **Fetch on-demand per esperto** (non a file intero): chunking content-defined (GearHash) per dedup
   fra quantizzazioni, e richiesta dei soli esperti/layer attivi con prefetch predittivo. Basi già qui
   (`internal/blob`, `/swarmai/blob/1.0.0`).
2. **Cellula LAN via llama.cpp RPC** (`rpc-server`/`--rpc`): swarmai fa da control plane (scoperta,
   `--tensor-split` dalle capability, membership per RTT) e **tunnela l'RPC nello stream libp2p
   autenticato** — mai la porta TCP grezza (CVE-2026-34159, zero-auth).
3. **Draft→verify (M2)**: il nodo povero manda la bozza locale, un super-nodo la verifica in batch.
4. **Fiducia stadiata**: PeerID Noise + PSK per la cellula di amici; poi esecuzione ridondante N-di-3 +
   replica adattiva (BOINC), kudos (AI-Horde), TOPLOC per lo swarm pubblico; sandbox seccomp/wasmtime.
5. **Nodo browser via WebRTC (M12)**.

## Architettura (file)
- `main.go` — CLI (`node start`, `peers`, `status`, `run`, `model share|fetch|list`).
- `internal/node/` — host libp2p, scoperta, gossip delle capacità, registro dei peer, protocollo infer
  (`infer.go`) e protocollo di streaming dei pesi (`stream.go`).
- `internal/blob/` — chunking content-addressed e chunk store locale.
- `internal/backend/` — backend d'inferenza (`llama-server`, stub).
- `internal/control/` — control API loopback per la CLI.
