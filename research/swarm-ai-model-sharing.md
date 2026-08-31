# SWARM — Modelli AI di frontiera su hardware povero, a costo zero

**Obiettivo del progetto**: rendere utilizzabili i migliori modelli AI in circolazione su dispositivi
poco potenti (telefoni inclusi), senza acquisto di hardware, senza workstation e senza costi di API,
sfruttando esclusivamente l'hardware già esistente e — dove serve — l'uso condiviso di dispositivi
interconnessi (swarm). Il documento valuta tutta la bibliografia rilevante (sistemi, paper, progetti
attivi al 2026) e propone un insieme di metodi nuovi, più avanzati dello stato dell'arte, con una
architettura integrata e una roadmap.

---

## 1. I vincoli fisici del problema (da cui non si scappa)

Qualunque proposta seria deve partire dai quattro muri reali. Tutti i metodi della sezione 4 sono
progettati attorno a questi numeri.

**1.1 Muro di memoria.** Un telefono medio ha 6–12 GB di RAM, di cui ~4–8 GB usabili da una app.
I migliori modelli open-weight del 2026 (classe DeepSeek/GLM/Qwen/Kimi) hanno 200B–1T parametri
totali: anche a 1,58 bit per peso sono 40–200 GB. **Nessuna tecnica di compressione nota fa entrare
un modello di frontiera intero in un telefono.** Conseguenza: o il modello è distribuito su più
dispositivi, o sul telefono ne gira solo una parte (cache di esperti, draft model, slice elastica).

**1.2 Muro di latenza WAN (non di banda).** Nel decode autoregressivo si genera un token alla volta.
Lo stato nascosto da spostare tra due nodi che si dividono i layer è piccolo (hidden 8–16k dim →
16–32 KB/token/confine: anche una ADSL regge 40–80 token/s di banda). Il killer è la **latenza**:
30–100 ms di RTT per ogni confine di pipeline → con 4–8 nodi in serie si scende a 1–3 token/s.
Conseguenza: su Internet pubblica vincono le architetture che fanno **pochi round-trip per molti
token** (verifica speculativa in batch, expert-fetch predittivo, batch asincroni), non le pipeline
per-token. Su LAN/WiFi domestica (RTT ~1–5 ms) la pipeline invece funziona (lo dimostrano prima.cpp
ed exo).

**1.3 Churn e affidabilità.** I telefoni entrano ed escono dalla rete di continuo (batteria, rete,
termica). Una pipeline a layer si spezza se cade un nodo. Conseguenza: servono ridondanza,
riassegnazione rapida e — meglio ancora — architetture in cui la perdita di un nodo degrada la
qualità invece di bloccare la generazione (i MoE lo permettono, le pipeline dense no).

**1.4 Fiducia e privacy.** In uno swarm pubblico i nodi possono restituire risultati sbagliati o
tentare di ricostruire i prompt dalle attivazioni (l'inversione delle attivazioni è dimostrata in
letteratura). Conseguenza: servono verifica probabilistica dei risultati (stile TOPLOC), reputazione,
e per i dati sensibili percorsi solo-locale o solo-cerchia-fidata.

**Nota di onestà**: "i migliori modelli in circolazione" in senso assoluto (modelli proprietari di
frontiera) non sono eseguibili localmente da nessuna tecnica, perché i pesi non sono pubblici. Il
target massimo raggiungibile è **il miglior open-weight disponibile**, che nel 2026 è vicino alla
frontiera (DeepSeek, GLM-5.2 753B, Qwen, Kimi, Llama 405B) ed è quasi tutto **MoE sparso** — un fatto
strutturale che, come si vedrà, è la più grande fortuna di questo progetto.

**1.5 Priorità: alta velocità *e* precisione massima (il vincolo che orienta tutto il progetto).**
Ci sono due modi di far girare un modello grande su hardware datato, e tirano in direzioni opposte
sulla precisione:

- **Quantizzazione** (1,58–4 bit): dà velocità e riduce la memoria, ma *sacrifica* precisione — la
  qualità del modello scende, tanto più quanto più si comprime.
- **Distribuzione** (dividere il modello a piena precisione su più dispositivi, o far verificare
  l'output dal modello grande): *preserva* la precisione ma paga in latenza.

Poiché qui l'obiettivo è **precisione massima**, la spina dorsale del sistema è il percorso
**distribuito a piena (o quasi piena) precisione** — la verifica remota (M2), la gerarchia di esperti
MoE non degradati (M3) e la cellula LAN (§5). La quantizzazione spinta si usa **solo dove
misurabilmente non intacca la qualità**: il draft locale di M1 (tanto viene verificato dal modello
grande, quindi un draft impreciso costa velocità ma *non* precisione finale), il backbone e gli
esperti "freddi". **La velocità arriva dal nascondere la latenza** (batch, decoding speculativo,
predizione degli esperti), *non* dal degradare il modello. Questo è il principio che distingue questo
progetto dai runtime che inseguono solo i token/s comprimendo a 1,58 bit e accettando il calo di
qualità: qui il calo di qualità non è ammesso, e la velocità si conquista altrove.

---

## 2. Bibliografia valutata

Ogni voce riporta cosa fa, i numeri chiave e il **limite** che le impedisce, da sola, di risolvere il
problema. I link completi sono in fondo (§7).

### 2.1 Swarm e inferenza distribuita su dispositivi consumer

- **Petals** (BigScience) — Il capostipite: layer del modello distribuiti "stile BitTorrent" su GPU
  di volontari; Llama 2 70B a ~6 token/s, supporto fino a Llama 3.1 405B, fine-tuning incluso.
  *Limite*: pipeline per-token su WAN → latenza dominante; lo swarm pubblico è di fatto in declino
  per mancanza di incentivi; richiede GPU vere, i telefoni non partecipano.
- **exo (exo labs)** — Cluster p2p auto-configurante con dispositivi di casa (Mac, iPhone, iPad,
  Android): niente master, discovery automatica, partizionamento in base a RAM/topologia, API
  ChatGPT-compatibile. *Limite*: pensato per LAN; analisi tecniche (ott. 2025) segnalano lacune serie
  in sicurezza, fault-tolerance e tooling; non production-ready, niente incentivi per rete pubblica.
- **prima.cpp** (ICLR 2026) — Il riferimento attuale per i cluster domestici poveri: 30–70B su 4
  dispositivi di casa eterogenei (CPU/GPU misti, RAM insufficiente, WiFi, OS diversi).
  Pipelined-ring parallelism che sovrappone I/O disco, calcolo e rete; scheduler Halda
  heterogeneity-aware. 70B a 674 ms/token; 32B + decoding speculativo a 26 token/s; 5–17×
  meglio di llama.cpp/exo/dllama in TPOT. *Limite*: LAN only; non affronta lo swarm pubblico, né
  incentivi, né churn.
- **Parallax (Gradient)** — Serving decentralizzato p2p con stadi di pipeline mappati su nodi
  geograficamente sparsi, scambio diretto di hidden state, dimostrato su cluster di Mac consumer.
  *Limite*: di nuovo pipeline per-token su WAN; orientato a chi il modello lo serve, non a chi ha
  solo un telefono.
- **AI Horde** (ex Stable Horde) — L'unico swarm pubblico di volontari **davvero vivo da anni**:
  generazione testo e immagini gratuita, anche anonima; economia "kudos" (chi contribuisce ha
  priorità, i kudos non scadono, vietata la monetizzazione). *Limite*: ogni worker deve ospitare il
  modello intero → gira solo ciò che sta in una GPU consumer; è un dispatcher di code, non inferenza
  cooperativa. Però il suo modello di incentivi è la lezione sociale più preziosa in circolazione.
- **EdgeShard** — Partizionamento del modello su dispositivi edge + cloud con programmazione
  dinamica congiunta (selezione dispositivi + split): fino a −50% latenza, 2× throughput.
  **Galaxy** — parallelismo ibrido (tensor+sequence) su edge eterogenei. **LinguaLinked** —
  inferenza decentralizzata su più telefoni fidati con load-balancer runtime. **HALO** — inferenza
  distribuita semantica su reti edge con perdita. *Limite comune*: testbed piccoli e controllati,
  reti locali, nessun churn reale, nessuna economia.
- **Inferenza generativa a scala Internet (arXiv 2604.21072)** — ottimizzazione multi-dimensionale
  della comunicazione per inferenza distribuita su Internet: conferma che il collo di bottiglia è la
  comunicazione e che va attaccato su più assi insieme (quantizzazione, topologia, scheduling).

### 2.2 Inferenza on-device (il telefono da solo)

- **PowerInfer-2** — Primo sistema a servire un 47B su smartphone (11,68 token/s; fino a 27,8× sui
  framework precedenti): decompone le matrici in "neuron cluster", attivazioni dense su NPU e sparse
  su CPU, pipelining I/O-calcolo. *Lezione*: la sparsità di attivazione è ciò che rende il telefono
  capace di molto più della sua RAM. *Limite*: serve la variante "TurboSparse" del modello; top di
  gamma, non telefoni poveri.
- **llama.cpp / MLC-LLM** — Le fondamenta pratiche dell'inferenza locale (GGUF, Vulkan/Metal/OpenCL,
  Android/iOS). *Limite*: singolo dispositivo, modelli ≤ ~8–13B su telefoni reali.
- **T-MAC** (Microsoft, EuroSys) — mpGEMM via lookup-table senza dequantizzazione su CPU: fino a 4×
  il throughput di llama.cpp e −70% energia; BitNet-3B a 71 token/s su 8 core. **bitnet.cpp**
  (ACL 2025) — runtime ufficiale per LLM ternari su edge. *Lezione*: con pesi a 1–2 bit la CPU di un
  telefono torna competitiva: il decode è limitato dalla banda di memoria, e i pesi ternari la
  riducono di 8–10×.
- **BitNet b1.58 2B4T** (+ famiglia) — LLM **nativamente ternari** ({-1,0,+1}, 1,58 bit/peso)
  addestrati così da zero: a parità di dimensione eguagliano i modelli full-precision (≥3B).
  *Lezione strategica*: se i grandi laboratori open rilasceranno MoE ternari nativi, il costo
  di memoria/banda dell'intero progetto cala di ~10× di colpo. *Limite*: oggi esistono solo taglie
  piccole; la quantizzazione post-hoc a 1,58 bit dei modelli esistenti degrada.
- **Gemma 3n / Gemma 4 E-series (MatFormer)** — Architettura "matrioska": il modello E4B contiene
  un E2B funzionante; si possono estrarre taglie intermedie (Mix-n-Match) dallo stesso artefatto;
  Per-Layer Embeddings per ridurre la RAM. *Lezione*: un solo file può servire tutte le classi di
  dispositivo → è la chiave per la "condivisione rapida" (si scarica solo la fetta che serve).
- **WebLLM / WeInfer / AI Grid** — Inferenza in browser via WebGPU (WeInfer: +3,76× su WebLLM);
  AI Grid dimostra il browser-tab-come-nodo di un cluster. *Lezione*: il browser è il canale di
  partecipazione a costo di installazione zero. *Limite*: perf. inferiore al nativo, tab effimere.

### 2.3 MoE serving su hardware povero (la pista indicata da "FreeToken")

- **FreeToken** (UC Berkeley, ago. 2026, arXiv 2608.16157) — Motore MoE edge-native: **tutti i pesi
  stanno in RAM di sistema, sulla GPU solo una cache degli esperti usati di recente**; politica q★ di
  co-esecuzione CPU–GPU adattiva alla banda. Risultati: 35B MoE a 39 token/s su GPU da 8 GB; 284B su
  desktop gaming; **GLM-5.2 da 753B su una singola GPU workstation**; 2–4× più veloce di Ollama.
  *Lezione centrale*: la località di attivazione degli esperti è così alta che una piccola cache
  cattura la maggior parte del lavoro. *Limite*: gerarchia a un solo dispositivo (RAM→VRAM); se i
  pesi non stanno nemmeno in RAM (telefono!), FreeToken non ha risposta — ed è esattamente il buco
  che il nostro metodo M3 (§4) va a riempire estendendo la gerarchia alla rete.
- **Fiddler / HOBBIT / MoE-Lightning** — Offloading di esperti CPU↔GPU, precisione mista per
  esperto, pipeline di caricamento. **OD-MoE** — esperti caricati on-demand *senza cache* su nodi
  edge distribuiti, con un **predittore di attivazione degli esperti ultra-accurato** che maschera la
  latenza di caricamento. **WDMoE** — attention+router alla base station, esperti sui telefoni.
  **CoMoE** — ottimizzazione congiunta aggregazione/offloading di esperti all'edge.
  *Lezione cumulativa*: (a) gli esperti si possono predire in anticipo; (b) si possono servire da
  posti diversi; (c) saltare o sostituire un esperto degrada poco la qualità → tolleranza al churn
  incorporata. È il mattone architetturale perfetto per uno swarm di telefoni.

### 2.4 Nascondere la latenza: decoding speculativo e cascate

- **SpecExec** — Decoding speculativo massivamente parallelo per hardware consumer con offloading:
  70B a 4–6 token/s su GPU consumer (fino a 18,7× di speedup). **EAGLE-3** — draft head con fusione
  di feature multi-layer, 3–4× in produzione. **SuffixDecoding / RACER** — draft senza modello, da
  suffissi/retrieval. *Lezione*: il rapporto "molti token proposti, una verifica in batch" è
  l'inversione che serve per la WAN.
- **Split inference privacy-aware con decoding speculativo su WAN** (arXiv 2602.16760, feb. 2026) —
  Primo lavoro che usa esplicitamente la speculazione per ammortizzare la latenza WAN nella split
  inference, con attenzione alla privacy delle attivazioni. Convalida accademica diretta del nostro
  metodo M2.
- **Routing a cascata (RouteLLM e derivati)** — piccolo modello locale per la maggioranza delle
  richieste, escalation al modello grande solo quando serve. *Lezione*: il modo più economico di
  "avere" un modello di frontiera è non chiamarlo quasi mai.

### 2.5 Compressione della comunicazione

- **FourierCompress** — compressione spettrale layer-aware delle attivazioni per inferenza
  collaborativa (la comprimibilità varia molto per layer; gli split profondi comprimono male).
- **Flash Communication / Communication Compression for TP** — quantizzazione fine delle attivazioni:
  3,5–4,5× di compressione con degrado trascurabile, TTFT dimezzato. *Lezione*: il traffico tra nodi
  swarm si può ridurre di ~4× quasi gratis, e va deciso *dove* tagliare il modello in base alla
  comprimibilità del punto di taglio.

### 2.6 Incentivi, verifica, distribuzione dei pesi

- **AI Horde (kudos)** — economia del dono con priorità: funziona da anni senza denaro. *Il* modello
  sociale di riferimento.
- **Prime Intellect (TOPLOC, SHARDCAST, INTELLECT-2/3)** — verifica dell'inferenza tramite
  locality-sensitive hashing delle attivazioni (TOPLOC): un verificatore può controllare a campione
  che un nodo abbia eseguito davvero il modello dichiarato; distribuzione pesi via SHARDCAST;
  RL decentralizzato dimostrato a scala 32B. *Lezione*: la verifica probabilistica a basso costo
  esiste già; non serve blockchain per usarla (il layer token-economico è separabile).
- **BitTorrent/IPFS + Hugging Face Xet/dedupe** — distribuzione content-addressed a chunk: i
  fine-tune condividono la stragrande maggioranza dei tensori con il modello base → deduplicare a
  livello di chunk rende quasi gratuito distribuire N varianti. *Lezione per la "condivisione
  rapida"*: i pesi sono l'oggetto più cache-abile dell'universo software (immutabili, enormi,
  identici per tutti).

### 2.7 Sintesi critica: perché nessun sistema esistente basta

| Sistema | Modelli grandi | Telefoni poveri | WAN pubblica | Churn | Incentivi | Costo zero |
|---|---|---|---|---|---|---|
| Petals | ✅ (405B) | ❌ | ⚠️ lento | ⚠️ | ❌ | ✅ |
| exo / prima.cpp | ⚠️ (≤70B) | ⚠️ partecipano | ❌ LAN | ⚠️ | ❌ | ✅ |
| Parallax | ✅ | ❌ | ⚠️ | ⚠️ | ⚠️ | ⚠️ |
| AI Horde | ❌ (single-GPU) | ❌ worker | ✅ | ✅ | ✅ kudos | ✅ |
| PowerInfer-2 / T-MAC | ❌ (≤47B) | ✅ | — | — | — | ✅ |
| FreeToken | ✅ (753B) | ❌ (serve RAM grande) | — | — | — | ✅ |
| Cascate/speculativo | — (tecnica) | ✅ | ✅ | — | — | ✅ |

Nessuna riga ha tutte le colonne. **La tesi di questo documento è che le colonne si conquistano
combinando i mattoni in un modo che nessuno ha ancora assemblato**, più alcune idee genuinamente
nuove (§4). I tre fatti abilitanti del 2026:

1. I migliori open-weight sono **MoE sparsi**: per token si attivano ~3–6% dei parametri, e gli
   esperti sono predicibili, cache-abili, sostituibili e serviti da nodi diversi.
2. Il decode è limitato dalla **banda di memoria**, non dai FLOPs: pesi a 1,58–4 bit rendono
   competitive le CPU/NPU dei telefoni (T-MAC, bitnet.cpp, PowerInfer-2).
3. La latenza WAN si ammortizza con la **verifica speculativa in batch** e si aggira con la
   **predizione degli esperti**: entrambe trasformano "un RTT per token" in "un RTT per molti token".

---

## 3. Le risorse gratuite realmente disponibili

Il progetto vieta acquisti di hardware; l'inventario di ciò che esiste già è quindi il budget:

- **Miliardi di telefoni** con 4–12 GB RAM, NPU sempre più capaci, e ~8–10 ore/giorno in carica su
  WiFi (finestra notturna: calcolo gratis, energia trascurabile, zero impatto sull'utente).
- **PC e laptop dormienti** con 8–32 GB RAM e iGPU/dGPU: i "super-nodi" naturali dello swarm.
- **La LAN di casa**: RTT ~1–5 ms tra i dispositivi di una famiglia → una "cellula" prima.cpp-style
  che vale più della somma dei suoi membri.
- **Il browser**: partecipazione a installazione zero via WebGPU/WebRTC.
- **I modelli open-weight** stessi: l'unico ingrediente di frontiera che è già gratis.
- **Il tempo**: per i lavori asincroni (agenti batch, indicizzazione RAG, distillazione) la latenza
  non conta → si può usare il calcolo peggiore/lontano/notturno, che è quello abbondante.

---

## 4. I metodi proposti

Dodici metodi, ordinati per leva. M1–M4 danno valore da soli; M5–M12 compongono lo swarm completo.
Per ciascuno: idea, cosa aggiunge allo stato dell'arte, come costruirlo, rischio principale.

### M1 — Cascata locale-prima con escalation a incertezza (il moltiplicatore di tutto)

**Idea.** Sul telefono gira sempre un modello piccolo ma moderno (classe Gemma-4-E2B/Qwen3-4B,
ternario dove possibile). Ogni richiesta parte locale; un **gate di incertezza** (entropia dei
logit, self-consistency a 2 campioni, classificatore di difficoltà addestrato per distillazione)
decide se la risposta locale basta o se scalare allo swarm/al modello grande. Con i piccoli modelli
del 2026, il 70–90% delle richieste quotidiane non ha bisogno di altro.

**Oltre lo stato dell'arte.** RouteLLM instrada *prima* di generare; qui il piccolo modello genera
comunque (costo ~zero, locale) e l'escalation porta con sé la bozza già prodotta, che diventa il
draft del metodo M2 → il lavoro locale non è mai sprecato.

**Costruzione.** llama.cpp/MLC + un gate leggero; log delle escalation per riaddestrare il gate.
**Rischio.** Gate mal calibrato → risposte scadenti non escalate. Mitigazione: soglia conservativa
iniziale + feedback utente ("rigenera con il modello grande") come etichetta gratuita.

### M2 — Genera locale, verifica remota (speculazione rovesciata sulla WAN)

**Idea.** Inversione del decoding speculativo classico: **il draft model è il telefono, il
verificatore è lo swarm**. Il telefono genera 16–64 token di bozza; lo swarm (che ospita il modello
grande) li verifica in **un solo forward batch** e restituisce il punto di divergenza + la
correzione. Un RTT ogni N token invece di N RTT: con accept-rate realistici (60–85% con draft
moderni) e RTT 100 ms si ottengono 5–15 token/s *effettivi dal modello grande* su WAN — dieci volte
Petals.

**Oltre lo stato dell'arte.** SpecExec lavora su un solo host; arXiv 2602.16760 valida la
speculazione su WAN ma con split inference. Qui: (a) il draft è il *già presente* modello di M1;
(b) la verifica è **stateless per lo swarm** (niente KV persistente per sessione → qualunque nodo
può verificare qualunque richiesta → churn-proof e load-balancing banale); (c) l'accettazione è
**graduata**: sotto una soglia di importanza si accettano anche token "quasi giusti" (verifica
lossy), risparmiando round-trip.

**Costruzione.** Endpoint di verifica batch sui super-nodi (FreeToken/vLLM); protocollo
draft→verify→patch; integrazione con M1. **Rischio.** Accept-rate basso su compiti difficili →
degrada a ~1 RTT/token. Mitigazione: il gate di M1 manda quei casi direttamente in modalità remota.

### M3 — Lo swarm come gerarchia di memoria per esperti MoE ("FreeToken sopra la rete")

**Idea — il cuore del progetto.** FreeToken dimostra la catena RAM→VRAM-cache per i MoE. Noi
estendiamo la gerarchia: **flash del telefono → RAM del telefono → cellula LAN → super-nodi WAN**.
Il telefono tiene stabilmente: il backbone del MoE (attention, router, shared expert — nei MoE
moderni è la parte piccola: a 2–4 bit sta in 1–3 GB) più una **cache personale degli esperti caldi**.
I fatti che lo rendono possibile: l'attivazione degli esperti è fortemente zipfiana e
*personale* (il tuo uso attiva un sottoinsieme stabile); i predittori di attivazione (OD-MoE)
anticipano gli esperti dei layer successivi mentre si calcolano i precedenti; un esperto mancante
si può **sostituire con lo shared expert o saltare** con degrado lieve → il churn diventa perdita di
qualità marginale, non blocco.

**Oltre lo stato dell'arte.** Nessun sistema pubblicato tratta *la rete di volontari* come livello
della gerarchia di memoria MoE con cache personale on-device. WDMoE mette gli esperti sui telefoni
(direzione opposta, serve base station); OD-MoE distribuisce su edge fidato; FreeToken si ferma alla
RAM locale. La sintesi — backbone locale + expert-CDN di swarm + predizione + sostituzione tollerante
— è nuova, e ha una proprietà unica: **la qualità cresce con l'hit-rate della cache**, quindi il
sistema migliora con l'uso (la cache si adatta a te) e degrada dolcemente offline (funziona comunque,
con meno esperti).

**Costruzione in 3 stadi.** (1) Fork di un runtime MoE con cache di esperti su flash + telemetria
hit-rate per stimare le curve reali su tracce d'uso; (2) fetch asincrono degli esperti mancanti
dalla cellula LAN; (3) expert-CDN pubblico con replica proporzionale alla popolarità.
**Rischio.** Hit-rate reale insufficiente sui modelli di frontiera → gli stadi 1–2 lo misurano
prima di investire nello stadio 3; in caso negativo, M3 resta valido dentro la cellula LAN.

### M4 — Sovranità ternaria: puntare tutto su 1,58 bit + LUT

**Idea.** Trattare i modelli ternari nativi (BitNet-class) + kernel LUT (T-MAC/bitnet.cpp) come la
piattaforma di riferimento del progetto, non come curiosità: a 1,58 bit un 13B sta in ~2,6 GB (regge
su un telefono da 6 GB), la CPU basta per 10–30 token/s, l'energia cala del 70%. Dove i ternari
nativi non esistono ancora (i MoE grandi), usare 2–4 bit (AQLM/QTIP-class) per gli esperti in M3.

**Oltre lo stato dell'arte.** Non la tecnica (esiste) ma la scommessa di piattaforma: costruire
runtime, formato di distribuzione e swarm già dimensionati per pesi a 1,58–4 bit significa che ogni
release ternaria di taglia superiore si "accende" nel sistema il giorno stesso.
**Rischio.** I laboratori potrebbero non rilasciare MoE ternari grandi. Mitigazione: M4 è comunque
utile per draft (M2), backbone (M3) e cascata (M1); e la distillazione M9 può produrre ternari
propri di taglia media.

### M5 — Pesi elastici e download progressivo ("progressive JPEG dei modelli")

**Idea.** Un solo artefatto per modello, strutturato alla MatFormer + any-precision: il telefono
scarica prima la slice E2B a bassa precisione (**utilizzabile dopo pochi minuti**), poi — in
background, di notte, dalla LAN — i bit e i layer aggiuntivi che lo promuovono a E4B/8B a precisione
piena. Le taglie non sono file diversi ma **prefissi dello stesso file**.

**Oltre lo stato dell'arte.** MatFormer dà l'elasticità in RAM; qui la si porta nel **formato di
distribuzione**: ordinamento dei tensori per "valore marginale per byte", così ogni byte scaricato
migliora il modello corrente. Questo, più la dedupe di M6, è la risposta tecnica alla richiesta di
"condivisione rapida". **Rischio.** Richiede modelli addestrati matrioska (oggi solo Gemma-class) —
per gli altri si ripiega su progressive-precision (prima 2 bit, poi i bit mancanti come delta).

### M6 — Distribuzione content-addressed con dedupe e sciame locale

**Idea.** Pesi spezzati in chunk content-addressed (stile BitTorrent/IPFS/Xet): i fine-tune
condividono i chunk del base → distribuire 10 varianti costa poco più di una; aggiornamenti come
delta; **i dispositivi vicini si scambiano i chunk via LAN/WiFi-Direct senza toccare Internet**
(la famiglia scarica il modello una volta sola). Il seeding dei chunk è il modo più facile di
guadagnare crediti (M11) anche per i dispositivi troppo deboli per fare inferenza.

**Oltre lo stato dell'arte.** Le parti esistono (torrent, Xet); la novità è il pacchetto: chunking
allineato ai tensori + ordinamento progressivo di M5 + trasporto misto (HTTP-seed, p2p WAN, p2p LAN)
+ crediti per il seeding. **Rischio.** Basso: è la parte più matura; attenzione solo alle licenze
dei modelli ridistribuiti.

### M7 — Compressione delle attivazioni e tagli comprimibili

**Idea.** Dove lo swarm deve spostare hidden state (M2, M3, cellule LAN federate), applicare
quantizzazione fine + compressione spettrale layer-aware (FourierCompress-style) e scegliere i punti
di taglio del modello **in base alla comprimibilità del punto** (varia molto per layer): 4× di banda
in meno quasi gratis, e i tagli giusti riducono anche l'invertibilità delle attivazioni (privacy).
**Rischio.** Basso; degrado da monitorare per punto di taglio.

### M8 — La flotta notturna (BOINC per l'inferenza)

**Idea.** Tutto ciò che non è interattivo — lavori agentici batch, indicizzazione ed embedding RAG,
generazione di dati per M9, pre-fetch degli esperti per M3, verifica di code M2 non urgenti — va su
una coda globale eseguita dai dispositivi **in carica + su WiFi + schermo spento**. Miliardi di
telefoni-ora al giorno, costo marginale ~zero, zero fastidio per l'utente.

**Oltre lo stato dell'arte.** BOINC esiste per il calcolo scientifico; nessuno lo ha costruito per
l'inferenza LLM con code a priorità, crediti e risultati verificati (TOPLOC-style a campione).
È anche il "motore economico" che genera i crediti che pagano l'interattività diurna: di notte doni,
di giorno spendi. **Rischio.** Policy degli app store su background compute → distribuire anche
fuori store (APK, F-Droid) e via browser (M12).

### M9 — Distillazione federata continua (il costo marginale tende a zero)

**Idea.** Lo swarm usa la flotta notturna per generare, con il modello grande, dati di
addestramento mirati sugli errori reali dei modelli locali (le escalation di M1 sono
*esattamente* il curriculum giusto: sono i casi in cui il piccolo ha fallito). Con quei dati si
distillano periodicamente i modelli locali (LoRA/QLoRA su super-nodi, aggregazione federata).
**Il piccolo modello di ognuno migliora con l'uso collettivo** → la frazione di richieste che
richiede lo swarm cala nel tempo → il sistema diventa più economico man mano che viene usato.

**Oltre lo stato dell'arte.** La distillazione è nota; il loop chiuso
"gate di cascata → curriculum delle escalation → distillazione notturna federata → gate aggiornato"
su uno swarm di volontari non è mai stato costruito. È l'unico metodo della lista che *riduce
strutturalmente* la domanda di calcolo invece di soddisfarla. **Rischio.** Privacy dei prompt nel
curriculum → solo opt-in, anonimizzazione, o distillazione su parafrasi sintetiche generate
localmente.

### M10 — Cache semantica e prefissi KV condivisi

**Idea.** Nello swarm, moltissimo lavoro è ripetuto: stessi system prompt, stessi documenti RAG,
domande semanticamente identiche. Tre livelli di riuso: (a) **KV-cache dei prefissi comuni**
precalcolata e distribuita come chunk M6 (il super-nodo non ri-prefilla mai il system prompt di
un'app popolare); (b) cache semantica delle risposte a domande equivalenti (con embedding locale);
(c) per il RAG, embedding e chunk dei documenti pubblici calcolati una volta per tutto lo swarm.
**Rischio.** Cache poisoning → le voci di cache portano la firma del nodo che le ha prodotte e la
reputazione M11 pesa la fiducia; verifica a campione.

### M11 — Il contratto sociale: kudos + verifica probabilistica + tit-for-tat

**Idea.** Economia alla AI Horde (crediti per: inferenza, verifica M2, seeding M6, flotta M8;
priorità proporzionale ai crediti; utilizzabile comunque da chiunque a bassa priorità) con in più:
(a) **verifica TOPLOC-style a campione** — l'1–5% dei job è ricontrollato da un secondo nodo;
hash LSH delle attivazioni come impegno; chi bara perde reputazione e crediti; (b) reciprocità
diretta tit-for-tat tra cellule (alla BitTorrent) per la banda; (c) **niente moneta speculativa**:
crediti non trasferibili fuori dal sistema, per tenere fuori il farming e dentro lo spirito Horde.

**Oltre lo stato dell'arte.** AI Horde ha i kudos ma nessuna verifica del calcolo; Prime Intellect
ha la verifica ma un'economia token finanziaria. La combinazione "dono + verifica, senza
speculazione" è il punto di equilibrio che nessuno dei due occupa. **Rischio.** Sybil attack →
costo d'ingresso in lavoro utile (proof-of-inference iniziale), rate-limit per identità nuove.

### M12 — Lo swarm nel browser (porta d'ingresso a costo zero)

**Idea.** Un client WebGPU+WebRTC (WebLLM-class): chi apre una pagina può (a) usare il proprio
hardware per la cascata M1 senza installare nulla, (b) donare cicli alla flotta M8 mentre la tab è
aperta, (c) fare da relay/seeder M6. Il browser è anche il fallback dove gli app store bloccano M8.
**Rischio.** Perf. e affidabilità inferiori al nativo → usarlo per onboarding e lavori piccoli, non
come spina dorsale.

---

## 5. Architettura integrata — "SWARM v1"

```
┌─ DISPOSITIVO (telefono/PC/browser) ─────────────────────────────┐
│ M1 cascata: modello locale ternario (M4) + gate di incertezza   │
│ M3 backbone MoE + cache personale esperti (flash/RAM)           │
│ M5/M6 pesi progressivi content-addressed, scambio LAN           │
└──────────────┬──────────────────────────────────────────────────┘
               │ escalation (M2 draft→verify, batch, attivazioni compresse M7)
┌──────────────▼─ CELLULA LAN (casa/famiglia/ufficio) ────────────┐
│ pipeline prima.cpp-style su WiFi (RTT ~ms)                      │
│ cache esperti condivisa + seeding chunk + KV prefissi (M10)     │
└──────────────┬──────────────────────────────────────────────────┘
               │ WAN (solo batch: verifiche M2, expert-fetch M3, code M8)
┌──────────────▼─ SWARM PUBBLICO ─────────────────────────────────┐
│ super-nodi FreeToken-class (MoE grandi, verifica speculativa)   │
│ expert-CDN con replica zipfiana (M3) · coda notturna (M8)       │
│ distillazione federata (M9) · crediti+verifica+reputazione (M11)│
└─────────────────────────────────────────────────────────────────┘
```

Il principio unificante: **sulla WAN viaggiano solo batch e chunk, mai il singolo token**; il
per-token vive sul dispositivo o nella cellula LAN.

### Roadmap

| Fase | Contenuto | Metrica di successo |
|---|---|---|
| 0 (sett.) | Cascata M1 + runtime ternario M4 su telefoni reali | ≥70% richieste risolte in locale; ≥8 tok/s su telefono da 4 GB |
| 1 (1–2 mesi) | M2 draft→verify contro un super-nodo FreeToken; distribuzione M6 | ≥5 tok/s effettivi dal modello grande con RTT 100 ms |
| 2 (3–6 mesi) | Cellula LAN (prima.cpp-style) + cache esperti M3 stadio 1–2; misura hit-rate | 70B utilizzabile in casa; hit-rate cache esperti ≥80% |
| 3 (6–12 mesi) | Swarm pubblico: M8+M11 (flotta+kudos+verifica), poi expert-CDN M3 stadio 3, M9, M10, M12 | rete che si autosostiene senza denaro |

### Integrazione con RAGFlow

Tutti gli endpoint del sistema (super-nodi, cellula, perfino il dispositivo) espongono API
OpenAI-compatibili — la convenzione già adottata da exo, Parallax, prima.cpp e FreeToken — quindi
RAGFlow li può usare da subito come provider LLM/embedding senza modifiche al core. I punti di
aggancio naturali in questo repo: il registry dei provider LLM lato `api/` (endpoint
OpenAI-compatible custom), i worker di ingestion (`internal/ingestion/`) come consumatori ideali
della flotta notturna M8 (embedding e parsing batch non interattivi), e la cache semantica M10 come
estensione del retrieval.

---

## 6. Rischi trasversali e domande aperte

1. **Hit-rate reale degli esperti** sui MoE di frontiera con uso personale: è la variabile che decide
   quanto M3 scala oltre la LAN. Va misurata subito (fase 2), su tracce d'uso vere.
2. **Accept-rate del draft locale** su compiti difficili (decide il throughput M2). Mitigato dal
   gate M1 e dai draft distillati M9 (che migliorano proprio dove l'accept-rate è basso).
3. **App store e background compute** (condiziona M8 su iOS in particolare) → APK/F-Droid, browser,
   e su iOS limitarsi a esecuzione in foreground/carica.
4. **Licenze dei modelli** per la ridistribuzione p2p → preferire licenze permissive (Apache/MIT);
   verificare caso per caso le licenze "community".
5. **Privacy delle attivazioni e dei prompt** in escalation → M7 (tagli poco invertibili), cerchie
   fidate, opt-in espliciti per M9; i dati sensibili restano in M1 locale per costruzione.
6. **Massa critica dello swarm** — il rischio storico (Petals si è spento così). Contromisura di
   progetto: **ogni fase è utile anche da sola** (M1/M4 senza rete; cellula LAN senza swarm; swarm
   come bonus), quindi il valore per il singolo utente non dipende mai dall'esistenza della rete.

---

## 7. Riferimenti

**Swarm / distribuito**: [Petals](https://github.com/bigscience-workshop/petals) ·
[petals.dev](https://petals.dev/) ·
[exo](https://github.com/exo-explore/ex-exo) ·
[prima.cpp (arXiv 2504.08791, ICLR 2026)](https://arxiv.org/abs/2504.08791) ·
[Parallax (GradientHQ)](https://github.com/GradientHQ/parallax) ·
[Parallax paper (arXiv 2509.26182)](https://arxiv.org/html/2509.26182v1) ·
[AI Horde](https://github.com/Haidra-Org/AI-Horde) · [aihorde.net](https://aihorde.net/contribute/joining/) ·
[EdgeShard (arXiv 2405.14371)](https://arxiv.org/abs/2405.14371) ·
[HALO (arXiv 2601.11676)](https://arxiv.org/html/2601.11676) ·
[Inferenza a scala Internet (arXiv 2604.21072)](https://arxiv.org/pdf/2604.21072)

**On-device**: [PowerInfer-2 (arXiv 2406.06282)](https://arxiv.org/html/2406.06282v2) ·
[T-MAC (arXiv 2407.00088, EuroSys)](https://arxiv.org/abs/2407.00088) ·
[bitnet.cpp (ACL 2025)](https://aclanthology.org/2025.acl-long.457.pdf) ·
[BitNet b1.58 2B4T (arXiv 2504.12285)](https://arxiv.org/pdf/2504.12285) ·
[The Era of 1-bit LLMs (arXiv 2402.17764)](https://arxiv.org/pdf/2402.17764) ·
[Gemma 3n developer guide](https://developers.googleblog.com/en/introducing-gemma-3n-developer-guide/) ·
[Gemma 3n docs](https://ai.google.dev/gemma/docs/gemma-3n) ·
[WebLLM (arXiv 2412.15803)](https://arxiv.org/pdf/2412.15803) ·
[WeInfer (WWW 2025)](https://dl.acm.org/doi/10.1145/3696410.3714553)

**MoE serving**: [FreeToken (arXiv 2608.16157)](https://arxiv.org/abs/2608.16157) ·
[OD-MoE (arXiv 2512.03927)](https://arxiv.org/pdf/2512.03927) ·
[WDMoE (arXiv 2411.06681)](https://arxiv.org/pdf/2411.06681) ·
[HOBBIT (arXiv 2411.01433)](https://arxiv.org/html/2411.01433) ·
[CoMoE (arXiv 2508.09208)](https://arxiv.org/pdf/2508.09208) ·
[Survey MoE inference (arXiv 2412.14219)](https://arxiv.org/html/2412.14219v2)

**Decoding / cascate**: [SpecExec (arXiv 2406.02532)](https://arxiv.org/abs/2406.02532) ·
[Split inference speculativa su WAN (arXiv 2602.16760)](https://arxiv.org/pdf/2602.16760) ·
[SuffixDecoding (arXiv 2411.04975)](https://arxiv.org/pdf/2411.04975)

**Comunicazione**: [FourierCompress (arXiv 2510.16418)](https://arxiv.org/pdf/2510.16418) ·
[Flash Communication (arXiv 2412.04964)](https://arxiv.org/html/2412.04964v1) ·
[Communication Compression for TP (arXiv 2411.09510)](https://arxiv.org/abs/2411.09510)

**Incentivi / verifica**: [Prime Intellect — Planetary-Scale Inference](https://www.primeintellect.ai/blog/inference) ·
[INTELLECT-2 (arXiv 2505.07291)](https://arxiv.org/html/2505.07291v1) ·
[Distributed Deep Learning in Open Collaborations (arXiv 2106.10207)](https://arxiv.org/pdf/2106.10207) ·
[Folding@home / BOINC — volunteer computing a scala exaFLOPS](https://www.techspot.com/community/topics/folding-home-now-has-access-to-over-470-petaflops-of-distributed-compute-power-to-research.261417/)
