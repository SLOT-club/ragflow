# SWARM AI — Piano tecnico eseguibile (Prototipi 1 e 2)

Documento operativo che accompagna `swarm-ai-model-sharing.md`. Due binari costruibili in parallelo:

- **Binario A — "Nucleo piccolo + sistema"**: la prova falsificabile che un modello compatto + un
  sistema (RAG, strumenti, gate di difficoltà) eguaglia o batte un modello molto più grande, sul tuo
  hardware, coi tuoi numeri. Costruibile su una sola macchina, subito.
- **Binario B — Layer P2P per la condivisione rapida di hardware tra nodi**: il "sistema nervoso" dello
  swarm — scoperta dei nodi, streaming dei modelli su richiesta (modello Stremio/BitTorrent),
  distribuzione del calcolo. Parte dalla LAN di casa e cresce verso Internet.

Regola che li tiene insieme (dal documento di ricerca): **sulla WAN viaggiano solo batch e chunk, mai il
singolo token**; il per-token vive sul dispositivo o nella cellula LAN.

Tutti i numeri di baseline vengono dai benchmark reali già misurati (Dell XPS 15 9500, 16 GB, GTX 1650
Ti): 4B Q6_K → 33 tok/s, 9B Q4_K_M → 8,7 tok/s, cache di prefisso 187 s → 21 s, MoE 35B-A3B da 20 GB
utilizzabile con working set del 37,4%.

---

## BINARIO A — Prototipo "Nucleo piccolo + sistema"

### A.0 Ipotesi da falsificare
> Un **4B + RAG (RAGFlow) + gate di difficoltà** raggiunge o supera un **9B da solo** sulle *tue*
> domande reali, a velocità maggiore (a cache calda), spostando la conoscenza dai pesi al retrieval.

Se è vero, hai la prova in mano che "nucleo piccolo + sistema > monolite" sul tuo PC. Se è falso, sai
esattamente dove (e i log ti dicono quali domande scala il gate).

### A.1 Stack
Tre componenti, ognuno con un confine netto (mai fondere runtime e guscio):

1. **Motore di inferenza** — `llama-server` (OpenAI-compatible su `:8080`). Due `compat` switch dai tuoi
   guasti misurati: `supportsDeveloperRole: false` e `maxTokensField: max_tokens`. Reasoning off dove
   non serve: `chat_template_kwargs: {enable_thinking: false}`.
2. **Sistema di conoscenza** — RAGFlow (questo repo): ingestion dei tuoi documenti → chunk + embedding →
   endpoint di retrieval. Espone API OpenAI-compatibili, quindi si aggancia senza modifiche al core.
3. **Router / gate** — un componente sottile (Python) che: (a) decide se una domanda ha bisogno di
   conoscenza → chiama RAGFlow e inietta i chunk nel prompt; (b) misura l'incertezza della risposta
   locale → se alta, *scala* (a un 9B locale o, in fase swarm, al Binario B).

### A.2 Passi
1. **Baseline.** Avvia il 9B da solo e misura il set di domande (sotto). Config dai tuoi appunti:
   `llama-server -m qwen3.5-9b-q4_k_m.gguf -c 32768 -ngl 20 --host 0.0.0.0 --port 8080`.
   **Scalda la cache** con una chiamata a vuoto prima di cronometrare (misura il *secondo* turno).
2. **Nucleo piccolo.** Avvia il 4B: `... -m qwen3.5-4b-q6_k.gguf -ngl 99 ...`. Stessa procedura di warmup.
3. **Aggancia RAG.** In RAGFlow: crea un knowledge base, carica i documenti bersaglio, verifica il
   retrieval. Nel gate, per le domande "di conoscenza", recupera i top-k chunk e mettili nel system/user
   prompt del 4B.
4. **Gate di difficoltà.** Prima versione semplice e onesta:
   - *segnale di conoscenza*: la domanda cita entità/fatti fuori dal parametrico → attiva RAG;
   - *segnale di incertezza*: entropia dei primi token / disaccordo fra 2 campioni a bassa temperatura →
     se alto, marca "scala".
   - Logga ogni decisione: sono il curriculum per migliorarlo dopo.
5. **Confronto.** Tre condizioni sullo stesso set: **9B nudo**, **4B nudo**, **4B + RAG + gate**.

### A.3 Il benchmark che decide
- **Set**: 30–50 domande reali tue, etichettate per tipo (conoscenza / ragionamento / strumenti).
- **Metriche**: correttezza (giudice LLM + verifica manuale a campione), **tok/s a cache calda**, TTFT
  a caldo, % di domande in cui il retrieval è scattato, % scalate dal gate.
- **Criterio di accettazione**: sulle domande di *conoscenza*, **4B+RAG ≥ 9B nudo** in correttezza, a
  velocità ≥ del 9B. Sulle domande di *ragionamento*, il gate deve riconoscere e scalare invece di
  sbagliare in silenzio.

### A.4 Rischi locali (già noti dai tuoi benchmark)
- **Cache di prefisso**: senza warmup il primo turno inganna (fino a 340 s dopo una modifica al system
  prompt). Warmup obbligatorio prima di ogni misura.
- **Contesto**: `-c` del server e `defaultContextWindow` del framework devono coincidere; il system
  prompt di un harness pesa ~8k token da solo.
- **Il file da paginare va su NVMe interno**, mai USB (39× più lento agli accessi casuali 64 KB).

---

## BINARIO B — Layer P2P: hardware condiviso tra nodi

### B.0 La riformulazione Stremio: "il modello è un torrent che streami, non che scarichi"
Stremio non scarica il film: **streama dal swarm le sole parti che servono adesso**, in ordine, con un
buffer, prese da molti peer; gli addon risolvono *cosa* streammare. Trasporta questo sull'inferenza:

| Stremio / BitTorrent | Swarm AI |
|---|---|
| il film | il modello (anche da 2 TB) |
| i pezzi (piece) con hash | i chunk di pesi / gli esperti / i layer, content-addressed (CID/Merkle) |
| ordine di riproduzione | ordine di attivazione (layer dopo layer, esperto dopo esperto) — **prevedibile** |
| buffer "avanti" | **prefetch predittivo** (il predittore di M3) |
| tracker / DHT | DHT Kademlia per trovare chi ha quale pezzo |
| i pezzi popolari hanno più seed | gli **esperti caldi** si replicano di più (expert-CDN, M3/M6) |
| seeding | contribuire pezzi = guadagnare crediti (M11), anche da dispositivi deboli |

Conseguenza: **non scarichi mai i 2 TB.** Streami gli esperti/layer attivi su richiesta dai peer,
precaricando i prossimi mentre calcoli i correnti. È l'out-of-core (§1-ter del documento) portato dalla
gerarchia *disco locale* alla gerarchia *swarm*.

### B.1 Due piani da condividere in P2P
1. **Piano DATI** — pezzi di modello (streaming Stremio-style) + prefissi KV precalcolati (M10) +
   adattatori LoRA (i "plug-in", pochi MB). Immutabili e content-addressed → cache-abili all'infinito.
2. **Piano CALCOLO** — draft→verify (M2), pipeline della cellula LAN, code della flotta notturna (M8).

Separali sempre (control plane leggero vs data/compute plane), come chiede il prompt-madre.

### B.2 Stack tecnico (scelte concrete, non reinventare)
- **libp2p (in Go)** — si incastra col modulo Go già presente in questo repo. Fornisce in un colpo:
  - trasporti **QUIC** e **WebRTC** (WebRTC = i nodi browser, a installazione zero);
  - **DHT Kademlia** per la scoperta di chi-ha-cosa (stesso algoritmo di BitTorrent Mainline);
  - **GossipSub** per l'annuncio delle capacità dei nodi;
  - **Circuit Relay v2** + **DCUtR** per il NAT traversal (bucare i router di casa senza server centrali).
- **Scoperta LAN** — **mDNS**: i dispositivi di casa si trovano in millisecondi, zero configurazione
  (la "cellula" prima.cpp-style). Questo è il primo traguardo, ed è dove la latenza è così bassa che
  anche la pipeline per-token funziona.
- **Pesi content-addressed** — chunk allineati ai tensori, pubblicati come **CID firmati** legati al
  commit git del modello; seedabili insieme su HTTP, IPFS e BitTorrent (un contributore debole fa da
  seed HTTP, uno forte da peer). Dedup automatica: i fine-tune condividono i chunk del base (M6).
- **Verifica** — campionamento TOPLOC-style (M11): l'1–5% dei job ricontrollato da un secondo nodo,
  hash LSH delle attivazioni come impegno; chi bara perde reputazione. Nessun blockchain necessario.

### B.3 Il protocollo di "condivisione rapida" (MVP-0, dal prompt-madre)
Ogni nodo, all'avvio, su GossipSub annuncia una **scheda capacità**: RAM/VRAM liberi, tok/s misurati,
quali pezzi di modello/adattatori seeda, la **politica di risorse** (CPU 10/25/50/100%, tetto di banda,
schedule: solo-idle / solo-notte / sempre / manuale) e il tasto di stop immediato. Un nodo che chiede
inferenza:
1. cerca nel DHT chi ha i pezzi/gli esperti che gli servono;
2. apre canali (QUIC in LAN, WebRTC+relay in WAN) e **streama i pezzi in ordine di attivazione**;
3. per il modello grande, manda la **bozza locale** a un super-nodo che la **verifica in batch** (M2);
4. logga banda, latenza, hit-rate → alimenta il predittore di prefetch.

CLI target (dal prompt-madre): `swarmai node start|status`, `swarmai model add`, `swarmai cluster
status`, `swarmai benchmark`, `swarmai run "prompt"`.

### B.4 Ordine di costruzione (non partire dalla visione grande)
1. **Cellula LAN prima.** Due tue macchine, mDNS, streaming dei pesi via QUIC in LAN, benchmark
   1-nodo vs 2-nodi. *Criterio*: un modello che sta al limite su una macchina diventa fluido sulla
   cellula, e il 70B (che su una sola non entra) gira sulla cellula.
2. **NAT traversal.** Due macchine su reti diverse (LAN + hotspot telefono) che si connettono via
   Circuit Relay v2 + DCUtR. *Criterio*: connessione P2P stabile attraverso due NAT.
3. **Streaming Stremio-style su WAN.** Un super-nodo seeda gli esperti; un nodo povero streama solo gli
   attivi, con prefetch. *Criterio*: tok/s utile su WAN grazie al prefetch, non crollo per-token.
4. **Nodo browser.** Trasporto WebRTC, un tab che partecipa a installazione zero (M12).

### B.5 Limiti onesti
- **NAT traversal fallisce a volte** → servono relay (Circuit Relay v2, resource-constrained by design);
  è un costo noto, non un difetto.
- **Streaming su WAN** paga latenza: vive sulla località/predizione (§1-ter). Se il predittore sbaglia,
  si va a banda-di-rete.
- **Fiducia**: nodi lenti/maligni/offline sono l'assunzione di base (prompt-madre §7). Sandbox forte +
  verifica a campione + reputazione, sempre.
- **App store** possono bloccare il compute in background sui telefoni → APK/F-Droid e browser (M12).

---

## Come A e B si compongono
- A dimostra che **serve meno modello** (nucleo piccolo + RAG + gate).
- B fornisce **ciò che resta**, streammato dai peer invece che scaricato o comprato in RAM.
- Insieme: un telefono/PC povero fa girare la cascata locale (A); quando serve il modello grande, ne
  **streama gli esperti attivi** dalla cellula/swarm (B, modello Stremio) e/o manda la **bozza da
  verificare** (M2). Nessun data center, nessun acquisto di hardware, piena precisione.

## Integrazione con questo repo e col repo `swarm-ai`
- **RAGFlow**: usato come sistema di conoscenza del Binario A; i suoi endpoint OpenAI-compatibili e i
  worker di ingestion (`internal/ingestion/`) sono i consumatori naturali della flotta notturna (M8).
- **Modulo Go**: libp2p-go si incastra col Go già presente (`internal/`, `cmd/`), quindi il Binario B
  può nascere qui invece che come progetto separato.
- **Repo `D:\swarm-ai`** (sulla tua macchina): questo piano è allineato al prompt-madre; i documenti
  `docs/` locali (es. `GATE-LOCALITA-ESPERTI.md`) vanno portati qui o linkati come fonte dei benchmark.

## Primo passo concreto (questa settimana)
Binario A, passi A.1–A.3: monta 4B e 9B con warmup, aggancia RAGFlow, misura il set di domande. È
falsificabile in giorni e non richiede nulla che tu non abbia già.
