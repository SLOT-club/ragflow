# SWARM AI — Ricerca di integrazione (sintesi multi-agente)

Sintesi di cinque tracce di ricerca parallele su cosa **affiancare/integrare** in swarmai per migliorarlo
molto, invece di reinventarlo. Ogni voce riporta repo, licenza, maturità e — soprattutto — *come
swarmai lo usa* e il limite onesto. Alla fine: la roadmap di adozione unica e i problemi ancora aperti.

Principio guida: **non reimplementare la matematica dei tensori né i protocolli maturi.** swarmai è il
*control plane* mancante (scoperta, autenticazione, membership dinamica, routing, incentivi) sopra
motori esistenti.

---

## Traccia 1 — Distribuzione/streaming dei pesi (P2P)

Già costruito in `swarmai/internal/blob` + `/swarmai/blob/1.0.0`: chunk content-addressed, manifest,
fetch a finestra con verifica per chunk. Cosa adottare per portarlo a livello "avanzato":

- **anacrolix/torrent** (Go, MPL-2.0, ~10 anni in produzione 24/7) — **motore di streaming consigliato**.
  La sua API `Reader` + priorità dei pezzi (`PiecePriorityNow/Next/Readahead`) è quasi 1:1 con "streama
  gli esperti attivi, tieni una finestra di readahead, declassa gli esperti freddi". **BEP52 (BitTorrent
  v2)** dà merkle-tree per-file → content-addressing per singolo esperto. *Uso*: modellare ogni
  esperto/layer come un "file" v2; collegare il segnale di attivazione del router MoE a
  `SetPiecePriority`. La scoperta resta la nostra (Kademlia/GossipSub); torrent solo come motore di
  byte-serving verificato.
- **iroh-blobs** (Rust, MIT/Apache-2.0, iroh 1.0 giu 2026) — **primitiva di range-fetch verificato**.
  BLAKE3 + bao-tree permette richieste per **intervalli di byte arbitrari**, verificati per-range, senza
  minimo di chunk — più preciso dei pezzi a taglia fissa se gli esperti non si allineano ai confini.
  *Uso*: come transport QUIC via `rustonbsd/libp2p-iroh`, oppure il port Go `tmc/go-iroh` (pre-1.0).
  Costo: FFI o port immaturo.
- **Hugging Face Xet — chunking content-defined (GearHash)** (algoritmo, Apache-2.0) — **da adottare
  come schema di chunking** al posto della taglia fissa (che è quello che ho scritto ora nel MVP): i
  confini basati sul contenuto fanno sì che, cambiando il 5% di un tensore fra due quantizzazioni, i
  chunk invariati restino identici e deduplicabili. Fondamentale per distribuire N varianti di un
  modello quasi al costo di una.
- **boxo/Bitswap** (Go, IPFS) — ripiego "tutto-Go, zero CGO" se si vuole un solo binario; ma il fetch è
  a granularità di blocco, meno adatto allo streaming fine per-esperto.
- Da studiare, non integrare: **colibri** (streaming esperti da disco locale con cache LRU — ottimo
  modello per la cache locale davanti al fetch P2P); paper **Harvest** (memoria GPU dei peer come tier
  di cache).

**Prossimo passo pratico**: sostituire il chunking a taglia fissa del MVP con GearHash CDC, e valutare
`anacrolix/torrent` come motore di trasporto sotto il nostro protocollo.

---

## Traccia 2 — Motori di inferenza distribuita (cellula LAN)

- **llama.cpp RPC backend** (`rpc-server` / `--rpc`, C++, MIT) — **il primo da integrare, netto.**
  Stessa famiglia del `llama-server` che i nodi già eseguono: nessun nuovo formato, nessun nuovo motore.
  Divide *pipeline/layer* fra macchine (fa entrare un modello che non ci sta su una sola; **non** è un
  moltiplicatore di velocità — decode sequenziale fra hop, ~6 tok/s per un 70B su 2 macchine a 2.5GbE).
  swarmai fornisce ciò che l'RPC *non* ha: scoperta, `--tensor-split` calcolato dalle capability
  gossippate, membership per RTT.
  - ⚠️ **Sicurezza non negoziabile**: la porta RPC ha **zero autenticazione e zero cifratura**, e ha già
    avuto una **RCE non autenticata (CVE-2026-34159)**. swarmai deve legarla a loopback e **tunnelarla
    dentro lo stream libp2p autenticato**, mai esporla in chiaro; e verificare che i peer della cellula
    abbiano build ggml-rpc identiche (l'handshake si impianta se divergono).
- **prima.cpp** (C++, MIT, fork di llama.cpp) — parallelismo "piped-ring" con prefetch, pensato per
  **LAN di casa eterogenee su Wi-Fi**. Miglior match letterale al target. Essendo un fork, o lo si offre
  come backend alternativo per celle Wi-Fi deboli, o se ne reimplementa lo scheduler sopra l'RPC vanilla.
- **distributed-llama** (b4rtaz, C++, MIT) — vero **tensor-parallel** (l'unico che dà speedup reale su
  LAN veloce e omogenea), ma richiede Ethernet pulita e `2^n−1` worker → attrito per uno swarm organico.
  Percorso accelerato opzionale, non default.
- **Parallax** (GradientHQ, Apache-2.0, Python/vLLM/SGLang/MLX) — scheduler a due fasi (allocazione
  statica + selezione dinamica della pipeline per richiesta), più intelligente dello split proporzionale
  dell'RPC. Ha un proprio layer P2P (Lattica) che duplica il nostro → borrow del design dello scheduler,
  o backend "tier GPU vera" per peer multi-GPU.
- **exo** (MLX/tinygrad, MIT) e **cake** (Rust) — pari filosofici. ⚠️ **cake ha licenza FAIR (non
  open-source): uso commerciale a pagamento** → solo fonte di idee (mDNS zero-config), mai da includere.
- **FreeToken** (Apache-2.0) — non è multi-macchina (offload MoE su una sola macchina), ma la sua
  **politica di cache LRU degli esperti** è oro: applicarla nel router di swarmai ed **estenderla ai
  peer** (un esperto freddo si streama da un vicino invece che dal disco) — è proprio la direzione del
  nostro layer blob. Opportunità originale, non ancora rivendicata da nessuno.
- **llama.cpp PR #25294** (`--moe-stream-cache`) — streaming degli esperti da disco per-peer; comporrà
  con l'RPC (due assi per far entrare un MoE grande). Ancora PR, da tracciare.

---

## Traccia 3 — Runtime on-device e sparsità ("bigger than RAM")

- **Backend nodo (anche telefono): llama.cpp / `llama-server`** (MIT) — **da targettizzare per primo.**
  Gira su Android/iOS/desktop, espone nativamente l'API OpenAI-compatibile che il nodo Go già chiama
  (zero protocollo custom). Integrazione Go: subprocess+HTTP (semplice, sandboxabile) o FFI
  (`gollama.cpp` via purego) per l'embedding in-app sul telefono. È esattamente il mio backend attuale.
- **"Bigger than RAM" pronto oggi: `llama.cpp --n-cpu-moe` + mmap** — nessun lock-in di modello (ogni
  GGUF MoE), stesso binario dell'inferenza. Da standardizzare per i nodi PC deboli. Per nodi desktop con
  molta RAM e una GPU: **ktransformers** (Apache-2.0, SOSP 2025, ha fatto girare 671B su un 4090 +
  ~512GB RAM).
- **Basso-bit CPU**: **T-MAC** (MIT, kernel LUT, 11 tok/s su Raspberry Pi 5) e **bitnet.cpp** (MIT,
  ternario lossless) — abilitano i telefoni, ma bitnet.cpp dà i grandi guadagni solo su modelli
  *nativamente ternari* (catalogo ancora piccolo).
- Altri backend mobile: **Cactus** (Apache-2.0, mobile-first, OpenAI-compatibile — startup giovane,
  rischio), **MLX** (MIT, solo Apple, `mlx_lm.server` OpenAI-compatibile — win per i Mac). ⚠️ **Evitare**
  MediaPipe LLM (EOL da Google) e trattare **MLC-LLM** come sotto-manutenuto.
- **PowerInfer-2** e **Apple LLM-in-a-Flash**: idee (predizione neuroni attivi, windowing, letture
  contigue da flash) da rubare nel nostro layer di prefetch; codice non ancora dipendibile (PowerInfer-2
  richiede varianti "TurboSparse"; LLM-in-a-Flash è solo un algoritmo).

---

## Traccia 4 — Fiducia, verifica, incentivi (senza blockchain)

- **Fatto scomodo abilitante**: l'inferenza GPU **non è bit-riproducibile** nemmeno a temperatura 0
  (kernel non batch-invarianti). "Rieseguo e confronto i byte" **non funziona** di default. I kernel
  batch-invarianti di Thinking Machines (in vLLM/SGLang) lo risolvono a +10–30%. Questo condiziona
  tutta la verifica.
- **Default v1 pratico: esecuzione ridondante N-di-3** con decoding deterministico (temp 0, digest del
  modello pinnato) o consenso semantico; su disaccordo, terzo nodo "wingman" (BOINC).
- **Replica adattiva (BOINC)** — il pattern "copia questo": nodi nuovi → ridondanza 3×; nodi provati →
  spot-check 1-su-N; chi fallisce → quarantena. È una curva di costo-verifica modulata dalla reputazione.
- **Kudos (AI-Horde)** — incentivo non-monetario: crediti guadagnati lavorando, spesi per priorità, non
  vendibili. Copiare il *design* (licenza AGPL: non vendorizzare il codice). **Tit-for-tat** (choking di
  BitTorrent) come difesa locale che non dipende dal ledger globale.
- **TOPLOC** (MIT) — commit LSH delle attivazioni (~258 byte/32 token, verifica a 100×): riduce *quanto
  spesso* serve rieseguire, non elimina il bisogno di ricomputo. Stadio pubblico, sopra la ridondanza.
- **Sandbox**: **wasmtime-go** (Apache-2.0) per il codice di orchestrazione/tool non fidato (epoch
  timeout, fuel) — *non* per i kernel GPU; **seccomp-bpf + Landlock** (Go-nativi, Linux) attorno al
  subprocess d'inferenza; **gVisor** opt-in; **Firecracker** solo per nodi "professionali" con KVM.
- **libp2p già pronto**: PeerID (identità crittografica gratis), **Noise** (default), **PSK/pnet** (per
  la cella di amici, ma senza revoca e in via di deprecazione → non per lo swarm pubblico), **Circuit
  Relay v2** con reservation/limiti (configurare da subito per non fare da amplificatore d'abuso).
- **Verità dura**: una reputazione pura **non può** essere insieme massimamente cooperativa e
  Sybil-proof (risultato provato) → lo stadio pubblico serve un **costo d'identità** non monetario
  (proof-of-unique-hardware, invito/web-of-trust, onboarding lento).

---

## Traccia 5 — RAG, adattatori, routing (il track "nucleo piccolo + sistema")

- **Plug-in = LoRA hot-swap di llama.cpp** (`POST /lora-adapters`, MIT) — **adottare ora.** Base
  residente, adattatori da pochi MB commutati per-richiesta senza reload; annunciare gli adattatori
  caricati nella capability card così i peer instradano "serve la LoRA di coding". È il sistema di plugin
  di `§1-quater` reso reale, sull'esatto runtime che swarmai già avvolge.
- **Gate di difficoltà** — genera in locale, misura entropia/self-consistency dei primi token, scala se
  incerto (è M1). **RouteLLM** è architettura di riferimento, non dipendenza (instrada *prima* di
  generare, buttando via la bozza che a noi serve per M2).
- **RAGFlow (questo repo) — nessuna modifica al core** per il primo prototipo; solo default tarati per il
  modello piccolo: `top_n`≈5, reranker **`bge-reranker-v2-m3`** (568M, CPU-friendly, già cablato),
  budget del contesto via `kb_prompt` (`rag/prompts/generator.py`), `similarity_threshold` più alta per
  non dare chunk marginali a un 4B, e **KV-prefix cache** (M10) scaldata una volta per knowledge base.
- **Decoding speculativo — onestà**: quello *same-host* è la scommessa **meno affidabile** su hardware
  debole (test 2026: nessuna modalità llama.cpp batte la baseline su 35B-A3B; CPU 3B → 1.7–2×). I due
  guadagni *misurati* su questo hardware sono (1) **non chiamare il modello grande** (il gate M1) e (2)
  la **KV-prefix cache** (187 s→21 s). Riservare lo speculativo a **M2 draft-locale/verify-remoto**
  (dove il guadagno è strutturale: ammortizza l'RTT), ed **EAGLE-3** ai super-nodi vLLM/SGLang.

---

## Roadmap di adozione unica (cosa integrare, in ordine)

| Fase | Integrazione | Modulo swarmai |
|---|---|---|
| **Ora** | Backend `llama-server` reale + `--n-cpu-moe` per bigger-than-RAM; gate M1 + RAGFlow (bge-reranker, `kb_prompt`) | `internal/backend`, gate Python |
| **Breve** | Chunking **GearHash CDC** al posto della taglia fissa; LoRA hot-swap annunciato in card | `internal/blob`, `internal/node` |
| **Breve** | **Cellula LAN via llama.cpp RPC**, tunnellata in libp2p (mai porta grezza), split da capability | nuovo `internal/cell` |
| **Medio** | Fetch **on-demand per-esperto** (policy LRU stile FreeToken, esteso ai peer) + prefetch predittivo | `internal/blob`, router |
| **Medio** | **M2 draft→verify** su super-nodo; fiducia stadio 1 (N-di-3 + replica adattiva + kudos gossippati + tit-for-tat) | `internal/node`, `internal/trust` |
| **Lungo** | TOPLOC + costo d'identità per lo swarm pubblico; sandbox seccomp/wasmtime; nodo browser WebRTC | `internal/trust`, client web |

## Problemi onestamente aperti
- **Verifica trustless di un forward pass senza ricomputo**: non risolta (TOPLOC riduce, non elimina;
  zkML troppo lento). → ridondanza + reputazione, non "prova crittografica".
- **Determinismo bit-esatto**: richiede kernel batch-invarianti; "rieseguo e confronto" non è gratis.
- **Sybil**: nessuna reputazione pura basta da sola nello swarm aperto → serve un costo d'identità.
- **Speculativo same-host**: guadagno fragile su hardware debole → non è la leva; lo è il gate + KV cache.
- **Licenze**: evitare **cake** (FAIR, non-OSS); AI-Horde/BOINC sono AGPL/LGPL → copiare il design, non il
  codice, se si vuole una licenza permissiva.

## Fonti
Le cinque tracce hanno raccolto decine di fonti primarie (repo, paper, CVE). Le principali per traccia:
anacrolix/torrent, n0-computer/iroh(-blobs), huggingface/xet-core, ipfs/boxo · ggml-org/llama.cpp (RPC,
PR #25294, CVE-2026-34159), Lizonghang/prima.cpp, b4rtaz/distributed-llama, GradientHQ/parallax,
FlashML-org/FreeToken · mlc-ai/mlc-llm, cactus-compute/cactus, ml-explore/mlx, microsoft/T-MAC,
microsoft/BitNet, kvcache-ai/ktransformers · PrimeIntellect-ai/toploc, Haidra-Org/AI-Horde, BOINC/boinc,
bytecodealliance/wasmtime-go, google/gvisor, Thinking Machines (determinism) · llama.cpp `/lora-adapters`,
predibase/lorax, lm-sys/RouteLLM, SafeAILab/EAGLE, BAAI/bge-reranker-v2-m3.
