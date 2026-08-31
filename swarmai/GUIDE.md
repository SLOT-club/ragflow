# Guida completa a swarmai

> Documento tecnico onesto, basato esclusivamente sui fatti verificati nel codice sotto `swarmai/` e sui documenti di ricerca in `research/` (percorsi relativi alla radice del repo). Dove una funzione è un segnaposto euristico, un ponte temporaneo o non ancora cablata, è detto esplicitamente.

---

## 1. Cos'è swarmai

swarmai è una rete **peer-to-peer** che punta a far girare modelli LLM di classe frontier su **hardware "povero" e già posseduto** (telefoni, PC dormienti, laptop senza GPU serie), a costo zero, **condividendo e facendo streaming del calcolo** tra i nodi dello sciame.

L'idea centrale, ereditata dai documenti di ricerca, è che una macchina senza modello locale può **prendere in prestito il calcolo** di una che ce l'ha, e che un modello più grande della RAM disponibile può restare usabile finché il *working set* attivo (gli esperti "caldi" di un MoE) sta nel budget di cache.

**Cosa fa oggi, concretamente** (tutto implementato e verificato nel codice):

- Ogni nodo è un **peer libp2p** che si scopre sulla LAN (mDNS) e sulla WAN (DHT Kademlia), diffonde in gossip una **capability card** con il proprio hardware/modello, e **instrada un prompt** verso il peer più adatto a servirlo (`swarmai run`).
- **Streaming dei pesi in stile "Stremio"**: un file di modello è spezzato in chunk a dimensione variabile con chunking content-defined (GearHash/FastCDC), indirizzati per contenuto (SHA-256), e i peer scaricano/verificano solo i chunk che servono. Ogni downloader diventa a sua volta seeder.
- **Fetch per singolo esperto** da un GGUF: si scaricano solo i chunk che coprono la "parte" (esperto/layer) che l'inferenza tocca davvero, con gerarchia di memoria RAM → disco → rete.
- **Cellula LAN** su llama.cpp RPC, con **tunnel sicuro** libp2p per non esporre mai la porta RPC (che, per stessa ammissione del codice, non ha né autenticazione né cifratura — CVE-2026-34159).
- **Draft → verify (M2)**: un nodo debole scrive una bozza locale, un peer più forte la accetta o la corregge in un solo round-trip.
- **Esecuzione ridondante N-of-3** con voto a maggioranza + **ledger di reputazione** locale non monetario (kudos in stile AI Horde, agreement/disagreement in stile BOINC).
- **Ensemble "combo"**: classificazione locale (difficoltà/dominio) → risposta primaria → cross-check → arbitro solo in caso di disaccordo.
- **Nodo browser**: una web UI zero-install servita da un nodo permette a un telefono/browser di *consumare* lo sciame e di *contribuire* calcolo eseguendo un modellino in WebGPU (web-llm) che si ricollega come backend del nodo.

**Onestà di fondo**: gran parte della visione dei documenti di ricerca (i 12 metodi M1–M12) è ancora un piano. Ciò che segue documenta **il codice che esiste**, non il manifesto.

---

## 2. Architettura d'insieme

### 2.1 Diagramma a livelli

```
                              ┌─────────────────────────────────────────────┐
                              │            SWARM PUBBLICO (WAN)               │
                              │  DHT Kademlia (ModeServer) · GossipSub caps   │
                              │  Circuit Relay v2 · DCUtR hole-punching       │
                              │  Solo BATCH e CHUNK attraversano la WAN,      │
                              │  mai un singolo token (regola di progetto)    │
                              └───────────────▲───────────────▲──────────────┘
                                              │               │
                        /p2p-circuit (NAT)    │               │  relay pubblico (--relay)
                                              │               │
        ┌─────────────────────────────────────┴───┐   ┌───────┴──────────────────────────┐
        │            CELLULA LAN (mDNS)            │   │        NODO RELAY REACHABLE       │
        │  Rendezvous "swarmai/rendezvous/1"       │   │  relayv2.New(host) · dialabile    │
        │  llama.cpp RPC via tunnel /swarmai/rpc   │   │  fa da bootstrap + relay ai NAT'd │
        │  tensor-split pesato sulla RAM libera    │   └───────────────────────────────────┘
        └───▲──────────────▲──────────────▲────────┘
            │              │              │
    ┌───────┴────┐  ┌──────┴─────┐  ┌─────┴──────────────────────────┐
    │ DISPOSITIVO │  │ DISPOSITIVO │  │ DISPOSITIVO + GATEWAY (:8090)  │
    │ llama-server│  │  Stub       │  │  web UI zero-install           │
    │ (nodo-modello)│ │ (router/    │  │  /ws/worker ← browser WebGPU  │
    │ backend     │  │  relay/seed) │  │  (web-llm Qwen2.5-0.5B) come  │
    │ Available() │  │  CanInfer=no │  │  backend swappato del nodo    │
    └─────────────┘  └────────────┘  └────────────────────────────────┘
         │                                        ▲
         └── control API loopback 127.0.0.1:P+1 ──┘  (default 4780; la CLI parla qui)
```

Regole strutturali chiave (dai documenti e dal codice):

- Il **control plane** (scoperta, membership, routing, incentivi) è ciò che swarmai aggiunge; il **data plane** di inferenza è delegato a motori esistenti (llama-server, llama.cpp RPC, web-llm). swarmai *non* reimplementa la matematica dei tensori.
- La **control API** di ogni nodo è in ascolto solo su loopback `127.0.0.1:(porta_nodo+1)` — default `4780`. È lì che la CLI (`swarmai peers`, `run`, ecc.) manda le richieste.

### 2.2 Protocolli libp2p custom

| Protocol ID | Scopo | Definito/servito in |
|---|---|---|
| `/swarmai/infer/1.0.0` | Inferenza one-shot request/response (JSON `wireRequest`/`wireResponse`), deadline 10 min | `internal/node/infer.go` (`handleInferStream`, `requestRemote`) |
| `/swarmai/blob/1.0.0` | Streaming chunk di modello: richiesta JSON `{type:"manifest"|"chunk", id}`, risposta = 1 byte di stato + (manifest JSON \| 8 byte BE di lunghezza + bytes), deadline 5 min | `internal/node/stream.go` (`handleBlobStream`) |
| `/swarmai/verify/1.0.0` | Draft→verify: JSON `verify.Request` in, `verify.Verdict` out, deadline 10 min | `internal/node/speculative.go` (`handleVerifyStream`) |
| `/swarmai/rpc/1.0.0` | Tunnel autenticato/cifrato coordinatore↔worker per llama.cpp RPC | `internal/cell/cell.go` (`RegisterWorker`, `Coordinator`) |

Chiavi condivise di scoperta e gossip:

- **Rendezvous** `"swarmai/rendezvous/1"` — usato sia come service tag mDNS sia come chiave di advertise/FindPeers sulla DHT.
- **Topic GossipSub** `"swarmai/caps/1"` — diffusione delle capability card.

---

## 3. Avvio rapido — scenari concreti

> Tutti i comandi usano la porta di default del nodo `4779` (TCP+QUIC). La control API sta su `4779+1 = 4780`, loopback. Cambiando `--port P` sul nodo, la control API diventa `P+1`.

### (0) Il modo più semplice: un solo file, doppio-clic

Il binario **`swarmai.exe`** (Windows) è auto-sufficiente: **fai doppio-clic e basta**. Senza argomenti sceglie da solo il ruolo — se trova un `llama-server` locale diventa **host** (serve lo swarm + pagina web su `:8090`), altrimenti diventa **nodo** che usa lo swarm sulla LAN — accende la web UI e resta in esecuzione mostrando l'indirizzo da aprire. Nessun altro file, niente da configurare. Per fermarlo chiudi la finestra.

Per **usare** lo swarm da un altro PC/telefono non serve nemmeno quello: apri il browser su `http://<IP-DELL-HOST>:8090`.

In alternativa (Linux/macOS, o per compilare dai sorgenti) ci sono i launcher identici `swarm.sh` / `swarm.ps1` con la stessa logica di ruolo automatico:

```bash
./swarm.sh            # Linux / macOS (anche Git Bash o WSL su Windows)
.\swarm.ps1           # Windows (PowerShell)
# su una rete che blocca il multicast mDNS, passa il token stampato dall'host:
./swarm.sh <TOKEN>
```

Su Windows senza toccare il terminale c'è **`swarm.bat`**: doppio-clic e parte (chiama `swarm.ps1`). Per avere l'icona sul desktop, doppio-clic **una volta** su `metti-sul-desktop.bat`: crea un collegamento "Swarm AI" sul desktop che punta a `swarm.bat` nella cartella del repo.

Il launcher compila il binario al primo avvio (serve Go, oppure si copia il file `swarmai`/`swarmai.exe` già compilato da un PC gemello con lo stesso OS). Rileva il modello sondando gli endpoint OpenAI comuni (`127.0.0.1:8080/8081/1234/11434`, o `$SWARMAI_LLAMA_URL`).

**Per SOLO usare lo swarm** da un altro PC o da un telefono non serve nemmeno il launcher: apri il browser su `http://<IP-DELL-HOST>:8090`. Nessuna installazione.

Le sezioni (a)–(e) sotto spiegano cosa fa il launcher sotto il cofano, comando per comando.

### (a) Ho un llama-server: divento nodo-modello

Prerequisito: un `llama-server` (o qualsiasi endpoint OpenAI-compatibile) in ascolto, es. su `http://127.0.0.1:8080`, che espone `/health`, `/v1/models`, `/v1/chat/completions`.

```bash
# Il nome del modello viene auto-rilevato da /v1/models se non lo passi.
swarmai node start --llama http://127.0.0.1:8080
# oppure fissando esplicitamente il modello servito:
swarmai node start --llama http://127.0.0.1:8080 --model qwen3.5-4b.gguf
```

In alternativa via ambiente: `SWARMAI_LLAMA_URL` e `SWARMAI_MODEL` forniscono i default di `--llama` e `--model`.

Il nodo:
- costruisce un backend `LlamaServer` (se `--model` è vuoto, chiama `detectModel()` su `/v1/models` e prende il **primo** id, ripulendo l'eventuale prefisso di path);
- pubblica la sua capability card con `CanInfer = backend.Available() && backend.Name() != "stub"`. `Available()` è una GET su `/health` con timeout 2s: è vero solo su HTTP 200.

Verifica:

```bash
swarmai status            # la tua card e i tuoi indirizzi
swarmai peers             # le card dei peer scoperti
swarmai run "Ciao, chi sei?"     # instrada localmente (hai il modello) → "... (local)"
```

### (b) Telefono/PC senza modello, mi unisco con un token d'invito

Sul **nodo che invita** (già avviato):

```bash
swarmai invite
# stampa {token, join: "swarmai node start --join <token>", addrs}
```

Il token è `base64url(JSON dei multiaddr /p2p/... del nodo)`. **Non è cifrato, firmato né scade**: chi lo ha può fare bootstrap e usare quel nodo come relay.

Sul **dispositivo che si unisce** (senza modello locale → backend `Stub`):

```bash
swarmai node start --join <token>
```

I peer decodificati dal token vengono aggiunti **sia come bootstrap DHT sia come relay** (AutoRelay). Il nodo Stub resta utile come router/relay/seeder: la sua card ha `CanInfer=false`, quindi non riceve inferenze ma può *instradarle*.

```bash
swarmai run "Spiega la relatività ristretta"
# Non hai modello: Run() chiama BestInferPeer(model), invia via /swarmai/infer/1.0.0,
# accredita credits.Earn(peer,1) sul successo, e restituisce l'id del peer che ha servito.
```

Nota onesta: se il peer remoto fallisce, `Run()` fa fallback locale (anche sullo Stub, che si limita a fare eco del prompt) taggando `" (local fallback)"` e restituendo comunque l'errore accanto al risultato.

### (c) Faccio da relay pubblico per i dispositivi dietro NAT

Su una macchina **raggiungibile** (IP pubblico o port-forward):

```bash
swarmai node start --relay
# relayv2.New(host): se fallisce è NON fatale (solo log), il nodo prosegue senza relay.
# In log su successo: "relay service active".
```

I dispositivi dietro NAT lo useranno passandolo con `--relays <multiaddr>` (ripetibile) oppure ricevendolo dentro un token `--join` (che lo aggiunge automaticamente ai relay). Il nodo NAT'd combina di suo tre meccanismi sempre attivi: `NATPortMap()` (UPnP/pmp), `EnableHolePunching()` (DCUtR, aiutato da QUIC) e Circuit Relay v2.

Attenzione (limiti reali):
- `--join`/`--relays` aggiungono ogni peer decodificato alla lista relay **indiscriminatamente**, anche se quel peer non offre davvero un servizio relay; `parsePeerAddr` filtra solo i multiaddr sintatticamente invalidi.
- AutoRelay è abilitato **solo** se almeno un indirizzo relay/join si risolve in un `peer.AddrInfo` valido.
- Gli indirizzi di ascolto sono **solo IPv4** (`/ip4/0.0.0.0/tcp/<port>` e `/udp/<port>/quic-v1`).

### (d) Formo una cellula LAN via RPC

Obiettivo: far girare in cooperazione, su più macchine LAN, un modello troppo grande per una sola, **senza mai esporre** la porta di llama.cpp `rpc-server`.

Su ciascun **worker** LAN:

```bash
swarmai node start --rpc-worker --rpc-bin /path/to/rpc-server --rpc-port 50052
# StartRPCServer lancia:  rpc-server -H 127.0.0.1 -p 50052   (solo loopback)
# RegisterWorker imposta l'handler /swarmai/rpc/1.0.0 che fa da proxy verso quel loopback.
```

Sul **coordinatore** (un nodo già avviato che conosce i worker):

```bash
swarmai cell prepare --workers <peerID_worker1>,<peerID_worker2>
# → JSON {"rpc": <addr loopback tunnel in ordine worker>,
#         "tensor_split": <pesi>,
#         "launch_hint": "llama-server -m <model.gguf> --rpc <addrs> --tensor-split <weights>"}
```

Il coordinatore apre **un listener `127.0.0.1:0` per worker**, che fa da ponte su uno stream libp2p cifrato verso quel worker. Il `tensor_split` è pesato sulla RAM libera: la quota di ogni membro è la sua RAM libera divisa per la **somma** delle RAM libere di tutta la cellula (3 decimali), con il **coordinatore per primo** poi i worker in ordine — es. RAM libere 100/100/300 MB → `0.200,0.200,0.600`.

Punto cruciale: swarmai **non esegue** `llama-server`. Restituisce solo il `launch_hint` come stringa; sei tu a lanciarlo a mano con quei `--rpc`/`--tensor-split`. I byte RPC grezzi viaggiano solo su loopback e dentro il canale sicuro libp2p, mai su una porta TCP aperta.

Limite di sicurezza onesto: la mitigazione è **"non esporre la porta"**, non "autorizza il chiamante". Non c'è ACL/allowlist su chi può aprire lo stream `/swarmai/rpc/1.0.0` verso un worker: chiunque riesca ad aprirlo viene proxato dritto al `rpc-server` loopback. La sicurezza dipende **interamente** dalla transport security di libp2p.

### (e) Uso e contribuisco dal browser (WebGPU)

Avvia un nodo con il gateway:

```bash
swarmai node start --gateway :8090
# --gateway è l'UNICO modo di abilitare la web UI/worker; vuoto (default) = disabilitato.
```

Poi apri `http://<host>:8090/` da qualunque telefono/PC in rete. La pagina (single-file, **in italiano**, `lang="it"`):

- fa polling di `/api/status` ogni 4s (self + peers);
- invia i prompt a `/api/ask?prompt=` (anche con Ctrl/Cmd+Enter), che gira `RunEnsemble` e mostra chip di metadata (primary, verifier, adjudicator, dominio, difficoltà, confidence).

Per **contribuire** calcolo, il pulsante *contribute* nella pagina:
- controlla `navigator.gpu` (WebGPU);
- importa dinamicamente `https://esm.run/@mlc-ai/web-llm` (CDN esterno **hardcoded**);
- carica il modello **hardcoded** `Qwen2.5-0.5B-Instruct-q4f16_1-MLC`;
- apre un WebSocket su `/ws/worker`, invia `{type:'register', model:...}`, e a ogni `{type:'infer'}` risponde con `{type:'result', id, text}`.

Sul lato Go, `/ws/worker` fa l'upgrade (origin sempre accettato), crea un `browserWorker` e chiama `node.SwapBackend(worker)`: il browser **diventa il backend del nodo** (via `backend.Swappable`) finché resta connesso; alla disconnessione `node.RestoreBackend()` ripristina il backend di base.

Onestà importante:
- **Non è** un peer libp2p diretto nel browser. È un worker **forwardato via WebSocket** al nodo gateway. Il peer js-libp2p è indicato come passo futuro nei commenti.
- Modello **single-worker, last-writer-wins**: una seconda registrazione su `/ws/worker` sovrascrive lo swap della prima; la disconnessione di uno dei due ripristina il backend base. Nessun multiplexing/coda di worker browser.
- `CheckOrigin` restituisce sempre `true` (nessuna auth su `/ws/worker`, `/api/ask`, `/api/status`).

---

## 4. Riferimento CLI completo

Tutti i comandi accettano `--port int` (default `4779`) che indica la porta del **nodo** la cui control API loopback (`porta+1`) va interrogata. Alias di help: `swarmai help`, `-h`, `--help`.

### `swarmai node start`
Avvia il daemon del nodo (scoperta + gossip + inferenza). `start` è l'**unico** sottocomando di `node`.

| Flag | Default | Note |
|---|---|---|
| `--name` | hostname | nome nella card |
| `--port int` | `4779` | porta di ascolto TCP+QUIC; control API su `porta+1` |
| `--llama URL` | `$SWARMAI_LLAMA_URL` | base URL OpenAI-compatibile; vuoto = nessun modello (Stub) |
| `--model` | `$SWARMAI_MODEL` | nome modello servito; vuoto = auto-detect da `/v1/models` |
| `--schedule` | `always` | `idle\|night\|always\|manual` |
| `--identity path` | `~/.swarmai/identity.key` | chiave Ed25519 persistente → peer id stabile |
| `--rpc-worker bool` | `false` | attiva il ruolo worker per la cellula LAN |
| `--rpc-bin path` | `""` | binario `rpc-server`; vuoto = non lancia nulla |
| `--rpc-port int` | `50052` | porta loopback del `rpc-server` |
| `--tier` | — | `small\|medium\|large` (metadata di routing ensemble) |
| `--tags CSV` | — | es. `code,math` (metadata di routing ensemble) |
| `--join token` | — | token d'invito: i suoi peer diventano bootstrap **e** relay |
| `--relay bool` | `false` | agisce da circuit relay pubblico |
| `--gateway addr` | `""` | serve la web UI zero-install, es. `:8090` |
| `--bootstrap multiaddr` | — | peer DHT extra (ripetibile) |
| `--relays multiaddr` | — | relay noto per AutoRelay (ripetibile) |

### Altri comandi

| Comando | Cosa fa | Flag rilevanti |
|---|---|---|
| `swarmai peers` | Elenca le card dei peer scoperti (→ `/peers`) | `--port` |
| `swarmai status` | Mostra la propria card e indirizzi (→ `/status`) | `--port` |
| `swarmai run "prompt"` | Risponde/instrada un prompt; `--redundancy ≥2` → voto a maggioranza | `--port`, `--model`, `--redundancy int` (default 0) |
| `swarmai draft "prompt"` | Bozza locale, verifica/corregge su un peer più forte (M2) | `--port`, `--model` |
| `swarmai ask "domanda"` | Risposta ensemble ("combo") instradata e cross-checked | `--port` |
| `swarmai credits` | Stampa il ledger di reputazione locale (→ `/credits`) | `--port` |
| `swarmai invite` | Stampa un token d'invito per un altro device (→ `/invite`) | `--port` |
| `swarmai model share <path>` | Chunk + seed di un file di modello, stampa il manifest id | `--port`, `--name` (default: basename) |
| `swarmai model fetch <manifest-id>` | Streamma un intero modello da un peer in un file | `--port`, `-o output` (**obbligatorio**), `--from peer-id` |
| `swarmai model list` | Modelli seeded localmente e dai peer (→ `/models`) | `--port` |
| `swarmai model part <manifest-id> <part-name>` | Scarica solo i chunk di una parte (esperto/layer) | `--port`, `--from peer-id` |
| `swarmai model cache` | Uso della hot-expert cache (→ `/cache`) | `--port` |
| `swarmai cell prepare` | Prepara una cellula LAN (llama.cpp RPC) sui worker | `--port`, `--workers CSV` (**obbligatorio**) |
| `swarmai help` | Stampa l'uso | — |

> Non esiste alcun sottocomando CLI per `draft/verify` oltre a `swarmai draft`, né comandi `benchmark`/`cluster status`/`model add` (questi ultimi compaiono solo nel piano di prototipo come testo *aspirazionale*, non nel codice).

---

## 5. API HTTP

### 5.1 Control API (loopback, non autenticata)

Bind su `127.0.0.1:(porta_nodo+1)` — default `127.0.0.1:4780`. È l'API che la CLI interroga. **Nessun TLS, nessuna auth**: è mitigata solo dal binding su loopback, ed è raggiungibile da qualsiasi processo locale. La maggior parte degli handler legge i parametri dalla query string e **non impone il metodo HTTP**.

| Endpoint | Descrizione | Note verificate |
|---|---|---|
| `GET /status` | Card e indirizzi del nodo | |
| `GET /peers` | Card dei peer non-stale | |
| `GET /run?prompt=&model=&redundancy=N` | Inferenza instradata; `N≥2` → `RunRedundant` (`{result, agreeing_peers, redundancy}`), altrimenti `{result, served_by}`; `route_error` sull'errore | `N<2` (incluso negativo) cade su Run singolo |
| `GET /draft?prompt=&model=` | `RunSpeculative`; `400 {"error":"missing prompt"}` se vuoto; altrimenti `{verdict, verified_by}` e `route_error` solo se errore non-nil; timeout 10 min | |
| `GET /ask?prompt=` | `RunEnsemble`; `400` se prompt vuoto; timeout 10 min | nessun method guard |
| `GET /credits` | Snapshot del ledger come array di `trust.Rep` | |
| `GET /invite` | `{token, join, addrs}` | il nodo dev'essere già in esecuzione |
| `GET /share?path=&name=` | Chunk + seed di un file; se `.gguf`, attacca le parti dal layout tensor | |
| `GET /fetch?id=&out=&from=` | Fetch di modello intero (`window` hardcoded a 8); registra i chunk → il nodo ri-seed | |
| `GET /models` | `LocalSeeds` locali + `Seeds` dei peer | |
| `GET /part?id=&part=&from=` | Fetch della sola parte via `FetchPartAuto`; `{part, bytes, preview_hex, fetched_from, cache}` | **HTTP 200 anche su errore di fetch** (errore nel body); `400` solo se manca `id`/`part` |
| `GET /cache` | `{used, budget, chunks}` della hot-expert cache | |
| `GET /cell/prepare?workers=` | `{rpc, tensor_split, launch_hint}` | swarmai non lancia llama-server |

### 5.2 Gateway API (solo con `--gateway`)

`net/http` ServeMux con esattamente 4 route. `CheckOrigin` sempre `true`, nessuna auth, nessun rate limit; unico bound è il timeout di 10 min su `/api/ask`.

| Endpoint | Descrizione |
|---|---|
| `GET /` | Serve l'`index.html` embeddato (`//go:embed`); 404 per qualsiasi path diverso da `/` |
| `GET /api/status` | `{self, peers[]}` di `peerView{name, model, tier, tags, can_infer}` |
| `GET /api/ask?prompt=` | `RunEnsemble` (timeout 10 min); `{"error":"missing prompt"}` se vuoto |
| `GET /ws/worker` | Upgrade WebSocket (gorilla); protocollo `register` → `infer` → `result` |

### 5.3 Endpoint del backend llama-server (esterni, non definiti da swarmai)

Il backend `LlamaServer` parla con: `GET /v1/models` (auto-detect, timeout 3s, prende `data[0].id`), `GET /health` (readiness, timeout 2s, richiede 200), `POST /v1/chat/completions` (client timeout 5 min). L'inferenza usa un solo messaggio `user`, `max_tokens` default 1024, **`temperature` hardcoded a 0.7**, `chat_template_kwargs={"enable_thinking":false}`. Nessuno streaming: una POST, legge tutto il corpo.

---

## 6. Come funziona sotto il cofano

### 6.1 Streaming pesi: CDC + dedup (`internal/blob/`)

Un file di modello è spezzato con un **rolling hash GearHash/FastCDC**: i confini di chunk sono scelti dal contenuto (`h = (h<<1) + gear[byte]`), non a offset fissi, quindi inserire/modificare pochi byte rimodella solo i chunk attorno alla modifica e lascia **byte-identici** tutti gli altri (dedup tra checkpoint/quantizzazioni, verificato da `TestCDCDedupSurvivesInsertion`). La tabella di sostituzione `gear[256]` è riempita in modo deterministico con splitmix64 da un seme fisso (`0x1234567890abcdef`), così ogni nodo/piattaforma chunka in modo identico. `DefaultCDC = {Min: 256 KiB, Avg: 1 MiB, Max: 4 MiB}`. `shareFileCDC` streamma il file con un `bufio.Reader` (finestra Max, mai l'intero modello in RAM). Indirizzamento per contenuto: ogni `Chunk = {Hash sha256, Size}`; `Manifest.ID = sha256(name, total_size, chunks)` — le `Parts` sono **escluse** dall'ID, quindi `AttachParts` non lo cambia. Lo store non duplica i file grandi: serve i chunk leggendo slice dell'originale (`ReadChunk` con `os.Open`+`ReadAt`). Sul transport `/swarmai/blob/1.0.0`, `fetchChunk` **verifica l'integrità** ricalcolando lo SHA-256 e fallendo su mismatch. **Limite onesto**: il *manifest* non è verificato (`fetchManifest` si fida del JSON del peer; nessun controllo che `m.ID` == id richiesto). `FetchModel` scarica **tutto** il modello prima di ritornare — la `window` è solo un semaforo di concorrenza, non un buffer scorrevole che fa partire l'inferenza sui primi chunk.

### 6.2 Fetch per-esperto + cache (`internal/node/expert.go`, `internal/blob/cache.go`)

Per far girare modelli più grandi della RAM si scaricano solo le **parti** che l'inferenza tocca. `FetchPart`: risolve il manifest → `rng := m.Parts[part]` → `ChunksForRange(off, len)` restituisce gli indici dei chunk che coprono l'intervallo + l'offset del primo → `getChunk` di ciascuno → assembla e slicea i byte esatti. `getChunk` implementa la gerarchia **RAM → disco → rete**: (1) cache LRU calda `experts.Get(hash)`; (2) store locale `blobs.ReadChunk`; (3) `fetchChunk` dal seeder (verificato SHA-256). Ciò che arriva da disco/rete viene messo in cache calda. La **hot-expert cache** (`cache.go`) è una LRU a budget di byte, chiave = hash del chunk, default **512 MiB** (`NewCache(0)`); un chunk più grande dell'intero budget non viene messo in cache; l'eviction avviene solo su `Put`. `FetchPartAuto` sceglie i seeder (hint `from`, poi `reg.SeedersFor`). **Limiti onesti**: `Prefetch` esiste ma **non è cablato** ad alcun comando/endpoint (il "MoE router che predice i prossimi esperti" è solo un commento — il chiamante deve passare la lista esplicita di parti); i chunk fetchati per-parte finiscono **solo** nella cache RAM (nessun `RegisterChunk`), quindi il nodo **non li ri-seed** su disco; `FetchPart` carica in RAM tutti i chunk copertura interi prima di sliceare.

### 6.3 GGUF (`internal/gguf/gguf.go`)

`Parse` legge **solo la testa** del file (magic `GGUF`, versione, conteggi, metadata, directory dei tensori) con un reader bufferizzato: i GB di dati dei tensori non vengono mai letti. Le dimensioni dei tensori sono **derivate**, non lette da una type-table: si ordinano per offset e `Size = start del tensore successivo − proprio start`; l'ultimo arriva a fine file (`info.Size()`). `Start = DataStart + Offset`; `DataStart = align(fine-header, alignment)` con `alignment` = 32 salvo override da `general.alignment`. La mappa parte→range è costruita in `ShareModel` (`stream.go`), non in `gguf.go`: per un path `.gguf` chiama `gguf.Parse` e fa `AttachParts(t.Name → Range{t.Start, t.Size})`. **Limiti onesti**: un errore di parse è **silenziosamente ignorato** (il modello è condiviso senza parti, e `/part` non può servirlo); i `Size` includono il padding di allineamento e ogni byte finale non-tensor è attribuito all'ultimo tensore; gestito solo little-endian.

### 6.4 Cellula LAN + tunnel sicuro (`internal/cell/cell.go`)

Motivazione (dal doc del package e dal README): il `rpc-server` di llama.cpp **non ha auth né cifratura** e ha avuto una RCE non autenticata (**CVE-2026-34159**); la difesa di swarmai è **non esporre mai la porta** e tunnelare i byte su loopback + stream libp2p autenticato/cifrato. Due ruoli: **worker** = `RegisterWorker(h, addrLoopback)` installa l'handler `/swarmai/rpc/1.0.0` che fa `net.Dial` sul loopback e `pipe()` bidirezionale; **coordinatore** = `Coordinator.Setup()` apre un listener `127.0.0.1:0` per worker e ponte ciascuno su uno stream fresco. `TensorSplit(coordFreeMB)` restituisce le proporzioni (coordinatore primo, poi worker in ordine): la quota di ogni membro è la sua RAM libera (`coordFreeMB` per il coordinatore, `RAMFreeMB` della card per ogni worker) divisa per la somma delle RAM libere di tutti i membri — la RAM totale/installata non entra nel calcolo. `RPCArg()` è la stringa `--rpc` (indirizzi loopback dei tunnel), valida solo **dopo** `Setup()`. **Limiti onesti**: nessuna ACL su chi apre lo stream RPC; il claim sul CVE è la *motivazione dichiarata* dal codice, non verificata dal repo; `StartRPCServer` restituisce un `*exec.Cmd` che `node.go` scarta (`_`), quindi il processo non è health-checked; un nodo coordina **una** cellula per volta.

### 6.5 Draft → verify — M2 (`internal/verify/`, `internal/node/speculative.go`)

Alternativa WAN-friendly allo speculative decoding a livello di token: un nodo debole scrive una **bozza intera** locale, e un solo round-trip chiede a un peer più forte di **accettarla verbatim o correggerla**. `BuildVerifyPrompt(prompt, draft)` istruisce il modello grande a rispondere con esattamente la parola `ACCEPT` se corretto, altrimenti la sola risposta corretta. `Interpret(draft, out)`: se `out` (trimmato) è `ACCEPT` o inizia con `ACCEPT\n`/`ACCEPT `, → `Accepted=true, Answer=draft`; altrimenti `Accepted=false, Answer=correzione`. `RunSpeculative`: bozza locale (`MaxTokens:256`, **il modello non viene passato al draft locale**, serve solo per la selezione peer) → `BestInferPeer(model)` → se nessun peer o è se stesso, ritorna una verdict accettata "local draft only"; altrimenti `requestVerify` (`MaxTokens:512`); su errore di trasporto fa fallback a una verdict accettata *ma* ritorna anche l'errore non-nil (asimmetria voluta, facile da misinterpretare); su successo `credits.Earn(target, 1)`. **Limite onesto**: il match `ACCEPT` è prefix-based, quindi una correzione che *inizia* con "ACCEPT" verrebbe scambiata per accettazione (comportamento asserito dai test).

### 6.6 Ridondanza + fiducia (`internal/node/infer.go`, `internal/trust/ledger.go`)

`RunRedundant(ctx, req, want)`: se `want<2` o nessun peer eleggibile, delega a `Run`. Altrimenti seleziona fino a `want` peer con `inferPeers` (filtra `CanInfer` + match modello, ordina per **RAM libera** — **non** per reputazione), fanning-out concorrente della **stessa** richiesta, e conta la maggioranza per `strings.TrimSpace(Result.Text)`. Per ogni risposta: `credits.Agreement(id, agreed)`; ai peer di maggioranza anche `credits.Earn(id, 1)`. Il ledger `trust.Ledger` è una `map[peer.ID]*Rep` con `RWMutex`: `Rep` tiene `Credits, Jobs, Agreements, Disagreements`; `Earn(p,n)` aggiunge crediti e incrementa `Jobs`; `Replication(p)` calcola il fattore adattivo (sconosciuto→3, disaccordi non ancora recuperati→3, ≥10 accordi→1, ≥3→2, else 3). **Limiti onesti**: `Replication()` è testata ma **non chiamata** in produzione — `RunRedundant` usa il `want` del chiamante, quindi la "replica adattiva" è **orfana**; la selezione è per hardware, non per reputazione; il confronto di maggioranza è uguaglianza esatta di stringa (nessuna normalizzazione semantica) con **tie-break non deterministico** (iterazione mappa); i peer che vanno in timeout/errore sono **scartati in silenzio** (nessuna penalità reputazionale); il credito è **+1 fisso**; il ledger è **solo in memoria** (si azzera al riavvio) ed esplicitamente locale e non trasferibile.

### 6.7 Ensemble combo (`internal/route/route.go`, `internal/node/ensemble.go`)

`route.Classify(prompt)` restituisce `(difficoltà, dominio)` con **euristiche a substring** (auto-descritte nel package come "segnaposto deliberato per un gate addestrato su log reali di escalation"): dominio `Code` (cue come `func `, `def `, ` ``` `, `print(`) prima di `Math` (`prove`, `integral`, `=`, `+ `, ` - `) prima di `General`; difficoltà `Hard` se `len(prompt)>400` o cue come `why`, `prove`, `step by step`, `algorithm`. `RunEnsemble`: classifica → se nessun candidato, risponde sul backend locale (`Confidence="unchecked"`); altrimenti `rankForPrimary` (match tag dominio → tier adatto alla difficoltà → crediti → nome modello) sceglie il **primario**, poi `pickDifferent(preferCheap=true)` un **verifier** più economico. Se il verifier **accetta** → credita entrambi, `Confidence="verified"`, short-circuit. Su disaccordo → `pickDifferent(preferCheap=false)` un **arbitro** più forte; confronta i testi normalizzati (`TrimSpace+ToLower`), `Confidence="adjudicated"` (o `"disputed"` se non c'è arbitro). `Confidence` è uno di `unchecked | verified | disputed | adjudicated`. **Limiti onesti**: classificazione euristica facilmente ingannabile (il cue `=` marca molti prompt come `Math`; dominio order-sensitive: con cue misti vince `Code`); l'accordo in fase di arbitrato è uguaglianza esatta normalizzata, quindi risposte free-form "concordano" di rado; nessun sottocomando CLI — solo `/ask` e `/api/ask`.

---

## 7. Come testare e cosa è verificato dal vivo

### 7.1 Comando di test corretto

swarmai è un modulo Go **standalone e pure-Go** (`module github.com/slot-club/swarmai`): niente CGO, niente librerie native. Il `build.sh` della radice del repo RAGFlow e le sue regole (flag CGO, `office_oxide`, `pdfium-static`, `pdf_oxide`) **non si applicano** a swarmai. Si builda e si testa con la toolchain Go standard, dall'interno del modulo:

```bash
cd swarmai
go build -o swarmai .          # build del binario
go test ./...                  # tutta la suite
# oppure per singolo package:
go test ./internal/blob/...
go test ./internal/node/...
go test ./internal/gateway/...
go test ./internal/cell/...
go test ./internal/verify/...
go test ./internal/trust/...
go test ./internal/route/...
```

### 7.2 Cosa coprono i test presenti

- **blob** (`blob_test.go`): `TestShareChunkRoundtrip`, `TestManifestDeterministic`, `TestSeedsAndUnknownChunk`, `TestCDCDedupSurvivesInsertion`. (`cache_test.go`): `TestCacheLRUEviction`, `TestCacheRejectsOversize`, `TestCacheDuplicatePut`.
- **node**: `gguf_test.go` (`TestShareGGUFThenFetchExpert`: costruisce un GGUF con `WriteHeader`, condivide, fetcha un esperto), `expert_test.go` (`TestFetchPartOnDemand`: `expert.7` via `FetchPartAuto` su due nodi, re-fetch da cache), `integration_test.go` (`TestBlobStreamingRoundtrip`, `TestGossipAndComputeRouting`, `TestRedundantMajority`), `join_test.go` (`TestJoinViaInviteToken`: round-trip token → scoperta reciproca delle card, **solo** bootstrap, non il path relay).
- **gguf** (`gguf_test.go`): `TestParseLayout`.
- **cell** (`cell_test.go`): `TestSecureTunnel` (echo server come stand-in di `rpc-server`, due host TCP-only, prova che i byte fluiscono coordinatore→libp2p→worker→loopback e ritorno senza esporre la porta), `TestTensorSplitWeightedByRAM` (RAM 100/300, `TensorSplit(100)` → `"0.200,0.200,0.600"`).
- **verify** (`verify_test.go`): `TestBuildVerifyPrompt`, `TestInterpretAccept`, `TestInterpretAcceptWithWhitespaceAndSuffix`, `TestInterpretCorrection`.
- **trust** (`ledger_test.go`): `TestEarnAccumulates`, `TestReplicationTiers`, `TestDisagreementRaisesScrutiny`, `TestSnapshot`.
- **route** (`route_test.go`): `TestClassify`, `TestTierRank`.
- **backend** (`backend_test.go`): `TestLlamaAutoDetectModel`, `TestLlamaExplicitModelWins`, con `mockLlama` che espone `/health`, `/v1/models`, `/v1/chat/completions`.
- **gateway** (`gateway_test.go`, `worker_test.go`): esercitano `Handler()` e il ciclo `/ws/worker` register/infer/result su un vero WebSocket.

### 7.3 Buchi di copertura (onesti)

- **Nessun** test unitario diretto per `BestInferPeer` / `Run()` / `RunSpeculative` / `handleVerifyStream` / `requestVerify`: la logica di selezione routing in `capability.go`/`infer.go` e il path verify in `speculative.go` non hanno test dedicati tra i file osservati.
- `StartRPCServer` non è esercitato (i test usano un echo server); nessun test copre l'ordinamento multi-worker di `RPCArg` o i fallimenti di dial.
- Le misure di performance nei documenti (33 tok/s, 8.7 tok/s, 11.2–11.7 tok/s, working set 37.4%, 187s→21s) vengono da **una sola macchina** (Dell XPS 15 9500) — sono misure sul campo del proprietario, non benchmark generalizzati. Lo sciame vero (M2 remote-verify + cellula LAN) è dichiarato "il prossimo prototipo", non ancora dimostrato su scala.

---

## 8. Limiti onesti e roadmap

### 8.1 Limiti attuali (tutti verificati nel codice)

- **Routing povero**: `BestInferPeer` e `inferPeers` classificano **solo** per RAM libera (`RAMFreeMB`). Nessun punteggio per latenza, carico, tier o tag nel path `Run()`. `Tier`/`Tags` sono diffusi in gossip ma **non usati** da `BestInferPeer`/`Run`.
- **RAM solo su Linux**: `RAMFreeMB`/`RAMTotalMB` vengono solo da `/proc/meminfo`; `memInfoMB` ritorna `0,0` su macOS/Windows (il campo è "advisory"), quindi il routing basato su RAM è di fatto **Linux-only**.
- **Match modello lasco**: un `Model` locale o richiesto vuoto è trattato come match, quindi un peer non etichettato ma non corrispondente può essere scelto.
- **Fiducia leggera in `Run()`**: `credits.Earn(target,1)` su qualsiasi risposta riuscita, **senza verifica**. Solo `RunRedundant` (`want≥2`) fa cross-check per maggioranza.
- **Freschezza card**: pubblicate ogni 20s (+~1.2s dopo una connessione), TTL Registry 90s → un peer può apparire/servire fino a ~90s dopo essere andato in silenzio. Ricerca DHT WAN su ticker 60s, best-effort (errori di dial ingoiati).
- **`wireResponse.ServedBy`** è inviato dal server ma `Run()` riporta `target.String()` invece di leggerlo.
- **Inferenza one-shot** su `/swarmai/infer/1.0.0` (nessuno streaming di token); deadline stream 10 min.
- **Manifest fidato non verificato**; `FetchModel` scarica **l'intero** modello prima di ritornare e su errore lascia scritture parziali su disco.
- **Token d'invito** non autenticati/non cifrati/senza scadenza né revoca. Control API e gateway **senza auth** (loopback per la prima, origin-open per il secondo).
- **Cellula LAN** senza ACL sul chiamante dello stream RPC; swarmai non lancia llama-server (solo `launch_hint`).
- **Gate di difficoltà euristico** (substring), non un classificatore addestrato; misfire noti.
- **`Replication()` orfana**: definita e testata ma non cablata alla decisione di fan-out.
- **Browser = worker via WebSocket**, non peer libp2p; single-worker last-writer-wins; modello e CDN hardcoded.
- **`Prefetch` non cablato**; chunk per-parte non ri-seedati su disco.

### 8.2 Roadmap (dai commenti del codice e dai documenti di ricerca)

- **Peer browser js-libp2p diretto** (oggi c'è solo il worker forwardato via WebSocket; il peer libp2p nel browser è indicato come passo futuro nel doc del package gateway).
- **Relay pubblici gestiti** con capacità/allow-list configurabili (oggi `relayv2.New(h)` usa i limiti/ACL di default della libreria, senza superficie di configurazione).
- **Gate di difficoltà addestrato** su log reali di escalation (oggi è un segnaposto a keyword).
- **Verifica trustless** di un forward pass senza recompute: dichiarata irrisolta; TOPLOC riduce (non elimina) il recompute, zkML è troppo lento; l'inferenza GPU non è bit-reproducible neanche a temperatura 0.
- **Massa critica dello sciame**: modo storico di fallimento (Petals); mitigato solo rendendo ogni fase utile *da sola*.
- I **12 metodi M1–M12** del documento master restano in gran parte proposte non costruite; ciò che è implementato mappa su `internal/backend`, `internal/blob`, `internal/node`, `internal/cell`, `internal/trust`, `internal/gateway`, `internal/route`, `internal/verify`, `internal/gguf`, `internal/control`, `internal/invite`.

---

## 9. Mappa dei file/package

```
swarmai/
├── main.go                         CLI: node/peers/status/run/draft/ask/credits/invite/model/cell;
│                                    parse flag, wiring backend, gateway, --gateway/--join
├── internal/
│   ├── node/
│   │   ├── node.go                 New()/Run(): host + discovery (mDNS+DHT) + gossip + handler;
│   │   │                            setupMDNS/setupDHT/setupPubSub, capability loops, announce,
│   │   │                            SwapBackend/RestoreBackend, PrepareCell, InviteToken
│   │   ├── capability.go           CapabilityCard, Registry (TTL 90s), BestInferPeer, SeedersFor
│   │   ├── infer.go                InferProtocol, Run, requestRemote, handleInferStream,
│   │   │                            RunRedundant, inferPeers
│   │   ├── speculative.go          VerifyProtocol, RunSpeculative, requestVerify, handleVerifyStream
│   │   ├── stream.go               BlobProtocol, ShareModel (gguf.Parse→parts), fetchChunk/fetchManifest
│   │   ├── expert.go               getChunk (RAM→disco→rete), FetchPart, FetchPartAuto, Prefetch
│   │   ├── ensemble.go             RunEnsemble (primary/verifier/adjudicator)
│   │   └── *_test.go               join/integration/expert/gguf/relay/ensemble
│   ├── blob/
│   │   ├── blob.go                 CDC GearHash/FastCDC, Manifest, Parts, ChunksForRange, ShareFile
│   │   └── cache.go                LRU a budget di byte (default 512 MiB), hot-expert cache
│   ├── gguf/gguf.go                Parse (header+directory tensori), size da offset consecutivi
│   ├── cell/cell.go                RPCProtocol, RegisterWorker, Coordinator, TensorSplit, RPCArg,
│   │                               StartRPCServer (rpc-server -H 127.0.0.1)
│   ├── verify/verify.go            Protocol, BuildVerifyPrompt, Interpret (ACCEPT sentinel)
│   ├── trust/ledger.go             Rep, Ledger, Earn, Agreement, Replication (orfana), Snapshot
│   ├── route/route.go              Classify (difficoltà/dominio euristici), TierRank
│   ├── backend/
│   │   ├── backend.go              Backend iface, Stub, LlamaServer (auto-detect, /health, chat)
│   │   └── swappable.go            Swappable (Set/Restore) — base+inner sotto RWMutex
│   ├── gateway/
│   │   ├── gateway.go              Server, route /, /api/status, /api/ask, /ws/worker
│   │   ├── worker.go               browserWorker Backend, handleWorker (register/infer/result)
│   │   └── index.html              web UI zero-install (italiano), polling + ask + contribute WebGPU
│   ├── control/control.go          control API loopback 127.0.0.1:(port+1); tutti gli endpoint CLI
│   └── invite/invite.go            Encode/Decode token base64url (non firmato, non cifrato)
├── go.mod / go.sum                 modulo Go standalone github.com/slot-club/swarmai (pure Go)
└── README.md                       feature, uso, onboarding, architettura, roadmap
```

Build e test: `go build -o swarmai .` e `go test ./...` dalla radice del modulo (nessun `build.sh`, nessun CGO).

---

## 10. I documenti di ricerca

Tre documenti di pianificazione/letteratura (prosa, quasi tutta in italiano) sotto `research/`. Non contengono codice eseguibile né test; sono la motivazione e lo scope del progetto. Le citazioni bibliografiche (arXiv, progetti) vanno trattate come riferimenti di lavoro, non ri-verificati uno a uno contro le fonti.

- **`swarm-ai-model-sharing.md`** (52 KB) — Documento master. Quattro "muri fisici" (memoria, **latenza WAN** come killer del decode, churn, trust/privacy); tesi **out-of-core** ("non rimpicciolire il modello, eseguirlo in poca RAM streammando i pesi da SSD"); validazione empirica su una sola macchina (Dell XPS 15 9500); analisi di "cosa pesa in un modello" (conoscenza→RAG, profondità di ragionamento→inference-time, core linguistico incomprimibile); bibliografia ragionata (Petals, exo, prima.cpp, AI Horde, PowerInfer-2, FreeToken, SpecExec, TOPLOC…); i **12 metodi M1–M12** e la sintesi "SWARM v1" a tre livelli (dispositivo / cellula LAN / swarm pubblico, regola: solo batch e chunk attraversano la WAN).
- **`swarm-ai-prototype-plan.md`** (11 KB) — Due tracce costruibili: **Binario A** ("small core + system": ipotesi falsificabile che 4B+RAG con RAGFlow eguagli un 9B nudo, con metodologia di benchmark su 30–50 domande reali) e **Binario B** (layer P2P, il reframe "Stremio": un modello è un torrent da *streammare*, pezzi=esperti content-addressed, tracker=DHT Kademlia). La CLI stampata qui (`model add`, `cluster status`, `benchmark`) è **aspirazionale** e **non** coincide con quella reale in `main.go`.
- **`swarm-ai-tech-integration.md`** (13 KB) — Sintesi "cosa integrare vs reinventare": principio guida "swarmai è il **control plane mancante** (scoperta, auth, membership, routing, incentivi) sopra motori esistenti; non reimplementare la matematica dei tensori né i protocolli maturi". Cita anacrolix/torrent, iroh-blobs (BLAKE3), HF Xet GearHash, llama.cpp RPC (con l'**avvertenza CVE-2026-34159**: porta RPC senza auth/cifratura, da bindare su loopback + tunnel dentro stream libp2p), ridondanza N-of-3 + replica adattiva BOINC + kudos + tit-for-tat, e una roadmap di adozione mappata su `internal/backend`, `internal/blob`, `internal/node`, `internal/cell`, `internal/trust`.