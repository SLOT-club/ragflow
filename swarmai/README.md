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
  chunk **content-defined (GearHash/FastCDC)** — i confini dipendono dal contenuto, non da offset fissi —
  ciascuno content-addressed (SHA-256), descritti da un manifest anch'esso content-addressed. Un nodo
  annuncia i modelli che seeda; un altro li **streama a pezzi dai peer** con finestra di prefetch,
  **verifica ogni chunk**, riassembla il file e ridiventa seeder. Il CDC dà la **dedup fra versioni**:
  inserire o cambiare pochi byte reshapa solo i chunk locali, il resto resta identico e condivisibile.
  Verificato: fetch byte-identico via P2P; e dopo un'inserzione di byte il CDC mantiene il 99% dei chunk
  condivisi contro il 2% della taglia fissa.
- **Fetch on-demand per esperto** (`internal/node/expert.go`): un manifest può portare una mappa
  **parte→intervallo di byte** (esperto/layer). Un nodo richiede **solo i chunk che coprono la parte
  attiva**, non il file intero, li assembla e ne estrae i byte esatti. Una **cache LRU degli esperti**
  in RAM (policy FreeToken) tiene residenti quelli caldi e streama i freddi dai peer — così un modello
  molto più grande della RAM resta usabile finché il working set entra nel budget. `Prefetch` scalda in
  anticipo gli esperti che il router MoE prevede. Verificato: fetch di un solo esperto con cache ≪
  dimensione del modello.
- **Layout GGUF automatico** (`internal/gguf`): condividendo un `.gguf`, swarmai ne **legge la directory
  dei tensori** (solo la testa del file, mai i dati) e allega da solo la mappa parte→intervallo, così il
  fetch per-esperto usa i **nomi dei tensori reali** (`blk.0.ffn.experts.7`, …). Verificato end-to-end:
  condivisione di un GGUF → parti derivate → fetch di un esperto per nome con byte esatti.
- **Cellula LAN via llama.cpp RPC** (`internal/cell`, protocollo `/swarmai/rpc/1.0.0`). Più nodi fanno
  girare insieme un modello che non sta su una macchina sola. Punto critico di **sicurezza**: la porta
  di `rpc-server` non ha autenticazione e ha avuto una RCE (CVE-2026-34159), quindi swarmai **non la
  espone mai**: il worker lega `rpc-server` a loopback e il coordinatore ci arriva **dentro lo stream
  libp2p autenticato**, con `--tensor-split` calcolato dalla RAM libera dei membri. Verificato: byte
  instradati coordinatore→libp2p→worker e ritorno.
- **Draft→verify (M2)** (`internal/verify`, protocollo `/swarmai/verify/1.0.0`). Il nodo debole genera
  una bozza col suo modello piccolo; un peer più forte la **verifica o corregge in un solo giro** (su
  WAN ammortizza la latenza: un round-trip per tutta la bozza, non uno per token). Verificato end-to-end.
- **Fiducia locale** (`internal/trust`): ledger **kudos** (crediti non-monetari per lavoro utile, stile
  AI-Horde) + **reputazione** (tasso di accordo). L'**esecuzione ridondante N-di-M** manda la stessa
  richiesta a più peer, prende la maggioranza, accredita chi concorda e segna chi diverge (stile BOINC);
  la reputazione modula la **replica adattiva** (peer nuovi più controllati, peer provati meno).
  Verificato: 2 verificatori concordi, entrambi accreditati.

## Build e test
```bash
cd swarmai
go build -o swarmai .
go test ./...     # unit + integrazione (nodi libp2p reali in-process)
```
La suite copre: chunking/roundtrip dei pesi (`blob`), ledger e replica adattiva (`trust`),
verdetto draft→verify (`verify`), il tunnel RPC sicuro e il tensor-split (`cell`), e un test
d'integrazione end-to-end (`node`) che avvia più nodi, li fa scoprire via gossip e verifica routing
del calcolo, streaming dei pesi byte-identico ed esecuzione ridondante con maggioranza.

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
swarmai run "domanda" --redundancy 3   # esegue su 3 peer e prende la maggioranza
swarmai draft "quanto fa 6x7"  # bozza locale, verificata/corretta da un peer forte (M2)
swarmai credits                # ledger locale: crediti + accordo per peer
```

Condividere e streamare un modello fra nodi:
```bash
swarmai model share ./qwen3.5-4b-q6_k.gguf   # su un nodo: chunk + seed, stampa il manifest id
swarmai model list                            # vedere chi seeda cosa
swarmai model fetch <manifest-id> -o ./m.gguf # su un altro nodo: streama a pezzi dai peer, verifica
swarmai model part <manifest-id> <nome-parte>  # scarica SOLO i chunk di un esperto/layer
swarmai model cache                            # uso della cache degli esperti caldi
```

Formare una cellula LAN (un modello su più macchine):
```bash
# su ogni macchina worker (rpc-server legato a loopback, mai esposto):
swarmai node start --rpc-worker --rpc-bin /path/to/rpc-server --rpc-port 50052
# sul coordinatore: apre i tunnel sicuri e stampa i flag di llama.cpp
swarmai cell prepare --workers <peerid-worker1>,<peerid-worker2>
#   -> { "rpc": "127.0.0.1:PA,127.0.0.1:PB", "tensor_split": "0.5,0.3,0.2",
#        "launch_hint": "llama-server -m <model> --rpc ... --tensor-split ..." }
# poi lancia llama-server col launch_hint: il traffico RPC scorre dentro libp2p.
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
1. **Integrare il prefetch col router MoE reale**: agganciare `Prefetch` alle attivazioni degli esperti
   di `llama-server` (o del backend) così gli esperti del prossimo token si streamano prima di servire.
   Il layout GGUF che dice *dove* sta ogni esperto è **già fatto** (`internal/gguf`).
2. **Riabilitare QUIC** aggiornando go-libp2p/quic-go (oggi TCP-only per aggirare un panic di quic-go
   sotto Go 1.26); poi PSK/pnet per la cella di amici e TOPLOC per lo swarm pubblico.
3. **Sandbox** dei task non fidati (seccomp/Landlock attorno all'inferenza, wasmtime per il codice di
   orchestrazione) e **costo d'identità** anti-Sybil per lo swarm aperto.
4. **Nodo browser via WebRTC (M12)**.

(Cellula LAN via llama.cpp RPC, Draft→verify M2, fiducia locale, chunking CDC, fetch on-demand per
esperto con cache LRU e layout GGUF automatico sono **già implementati e verificati**, vedi sopra.)

## Architettura (file)
- `main.go` — CLI (`node start`, `peers`, `status`, `run`, `model share|fetch|list`).
- `internal/node/` — host libp2p, scoperta, gossip, registro peer, protocollo infer (`infer.go`) +
  esecuzione ridondante, streaming dei pesi (`stream.go`), draft→verify (`speculative.go`).
- `internal/blob/` — chunking content-defined (GearHash), chunk store, cache LRU degli esperti.
- `internal/gguf/` — parser del layout dei tensori GGUF (mappa parte→intervallo).
- `internal/node/expert.go` — fetch on-demand per parte + prefetch + gerarchia cache→disco→rete.
- `internal/cell/` — cellula LAN via llama.cpp RPC + tunnel sicuro loopback↔libp2p.
- `internal/verify/` — pattern draft→verify (M2): prompt di verifica e interpretazione del verdetto.
- `internal/trust/` — ledger kudos + reputazione (replica adattiva).
- `internal/backend/` — backend d'inferenza (`llama-server`, stub).
- `internal/control/` — control API loopback per la CLI.
