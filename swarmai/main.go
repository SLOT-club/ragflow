// Command swarmai is a peer in a P2P network that shares hardware for LLM
// inference. A node discovers others (mDNS on the LAN, Kademlia DHT on the WAN),
// gossips its hardware capabilities, and can route a prompt to whichever peer is
// best able to serve it — so a machine with no model can borrow the compute of
// one that has it.
//
// Usage:
//
//	swarmai node start [--name N] [--port P] [--llama URL] [--model M] [--schedule S] [--bootstrap ADDR]
//	swarmai peers   [--port P]
//	swarmai status  [--port P]
//	swarmai run "prompt" [--port P] [--model M]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/slot-club/swarmai/internal/backend"
	"github.com/slot-club/swarmai/internal/control"
	"github.com/slot-club/swarmai/internal/invite"
	"github.com/slot-club/swarmai/internal/node"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "node":
		cmdNode(os.Args[2:])
	case "peers":
		cmdQuery("peers", os.Args[2:])
	case "status":
		cmdQuery("status", os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "draft":
		cmdDraft(os.Args[2:])
	case "ask":
		cmdAsk(os.Args[2:])
	case "credits":
		cmdQuery("credits", os.Args[2:])
	case "invite":
		cmdQuery("invite", os.Args[2:])
	case "model":
		cmdModel(os.Args[2:])
	case "cell":
		cmdCell(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `swarmai — P2P hardware-sharing network for LLM inference

Commands:
  node start        start a node daemon (discovery + gossip + inference)
  peers             list capability cards of discovered peers
  status            show this node's own card and addresses
  run "text"        answer a prompt (add --redundancy N for majority verification)
  ask "text"        combo: route by difficulty/domain, cross-check between models
  draft "text"      draft locally, verify/correct on a stronger peer (M2)
  credits           show the local reputation ledger (kudos + agreement)
  invite            print a join token for another device to connect quickly
  model share PATH  chunk + seed a model file, print its manifest id
  model fetch ID    stream a whole model from a seeding peer (-o OUT)
  model part ID N   fetch only the chunks of one named part (expert/layer)
  model list        show models seeded locally and by peers
  model cache       show the hot-expert cache usage
  cell prepare      set up a LAN cell (llama.cpp RPC) over worker peers

Run "swarmai node start -h" for node flags.
`)
}

// cmdCell prepares a LAN cell: it asks the local daemon to open secure tunnels
// to worker peers and prints the llama.cpp flags to launch a coordinator.
func cmdCell(args []string) {
	if len(args) == 0 || args[0] != "prepare" {
		fmt.Fprintln(os.Stderr, "usage: swarmai cell prepare --workers <peer1,peer2> [--port P]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("cell prepare", flag.ExitOnError)
	port := fs.Int("port", 4779, "node port whose control api to use")
	workers := fs.String("workers", "", "comma-separated worker peer ids")
	_ = fs.Parse(args[1:])
	if *workers == "" {
		fmt.Fprintln(os.Stderr, "usage: swarmai cell prepare --workers <peer1,peer2>")
		os.Exit(2)
	}
	u := fmt.Sprintf("http://%s/cell/prepare?workers=%s",
		control.DefaultControlAddr(*port), url.QueryEscape(*workers))
	fmt.Println(prettyJSON(get(u)))
}

func cmdModel(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: swarmai model [share|fetch|list] ...")
		os.Exit(2)
	}
	switch args[0] {
	case "share":
		fs := flag.NewFlagSet("model share", flag.ExitOnError)
		port := fs.Int("port", 4779, "node port whose control api to use")
		name := fs.String("name", "", "advertised model name (default: file basename)")
		_ = fs.Parse(args[1:])
		if fs.NArg() == 0 {
			fmt.Fprintln(os.Stderr, "usage: swarmai model share <path> [--name N]")
			os.Exit(2)
		}
		abs, _ := filepath.Abs(fs.Arg(0))
		u := fmt.Sprintf("http://%s/share?path=%s&name=%s",
			control.DefaultControlAddr(*port), url.QueryEscape(abs), url.QueryEscape(*name))
		fmt.Println(prettyJSON(get(u)))
	case "fetch":
		fs := flag.NewFlagSet("model fetch", flag.ExitOnError)
		port := fs.Int("port", 4779, "node port whose control api to use")
		out := fs.String("o", "", "output file path (required)")
		from := fs.String("from", "", "peer id to fetch from (optional; else any seeder)")
		_ = fs.Parse(args[1:])
		if fs.NArg() == 0 || *out == "" {
			fmt.Fprintln(os.Stderr, "usage: swarmai model fetch <manifest-id> -o <out> [--from <peer>]")
			os.Exit(2)
		}
		u := fmt.Sprintf("http://%s/fetch?id=%s&out=%s&from=%s",
			control.DefaultControlAddr(*port), url.QueryEscape(fs.Arg(0)), url.QueryEscape(*out), url.QueryEscape(*from))
		fmt.Println(prettyJSON(get(u)))
	case "list":
		fs := flag.NewFlagSet("model list", flag.ExitOnError)
		port := fs.Int("port", 4779, "node port whose control api to use")
		_ = fs.Parse(args[1:])
		fmt.Println(prettyJSON(get(fmt.Sprintf("http://%s/models", control.DefaultControlAddr(*port)))))
	case "part":
		fs := flag.NewFlagSet("model part", flag.ExitOnError)
		port := fs.Int("port", 4779, "node port whose control api to use")
		from := fs.String("from", "", "peer id to fetch from (optional)")
		_ = fs.Parse(args[1:])
		if fs.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "usage: swarmai model part <manifest-id> <part-name> [--from <peer>]")
			os.Exit(2)
		}
		u := fmt.Sprintf("http://%s/part?id=%s&part=%s&from=%s",
			control.DefaultControlAddr(*port), url.QueryEscape(fs.Arg(0)), url.QueryEscape(fs.Arg(1)), url.QueryEscape(*from))
		fmt.Println(prettyJSON(get(u)))
	case "cache":
		fs := flag.NewFlagSet("model cache", flag.ExitOnError)
		port := fs.Int("port", 4779, "node port whose control api to use")
		_ = fs.Parse(args[1:])
		fmt.Println(prettyJSON(get(fmt.Sprintf("http://%s/cache", control.DefaultControlAddr(*port)))))
	default:
		fmt.Fprintf(os.Stderr, "unknown model subcommand %q\n", args[0])
		os.Exit(2)
	}
}

func cmdNode(args []string) {
	if len(args) == 0 || args[0] != "start" {
		fmt.Fprintln(os.Stderr, "usage: swarmai node start [flags]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("node start", flag.ExitOnError)
	name := fs.String("name", hostname(), "human-readable node name")
	port := fs.Int("port", 4779, "TCP+QUIC listen port")
	llama := fs.String("llama", os.Getenv("SWARMAI_LLAMA_URL"), "llama-server OpenAI base URL (e.g. http://127.0.0.1:8080); empty = no local model")
	model := fs.String("model", os.Getenv("SWARMAI_MODEL"), "name of the served model, advertised to peers")
	schedule := fs.String("schedule", "always", "contribution schedule: idle|night|always|manual")
	identity := fs.String("identity", "", "path to persistent identity key (default ~/.swarmai/identity.key)")
	rpcWorker := fs.Bool("rpc-worker", false, "act as a llama.cpp RPC worker for LAN cells")
	rpcBin := fs.String("rpc-bin", "", "path to llama.cpp rpc-server binary (optional)")
	rpcPort := fs.Int("rpc-port", 50052, "loopback port for the RPC worker")
	tier := fs.String("tier", "", "model tier for ensemble routing: small|medium|large")
	tags := fs.String("tags", "", "comma-separated domains this model is good at (e.g. code,math)")
	join := fs.String("join", "", "join token from `swarmai invite` on an existing node")
	var bootstrap multiFlag
	fs.Var(&bootstrap, "bootstrap", "extra bootstrap peer multiaddr (repeatable)")
	_ = fs.Parse(args[1:])

	if *join != "" {
		peers, err := invite.Decode(*join)
		if err != nil {
			log.Fatalf("--join: %v", err)
		}
		bootstrap = append(bootstrap, peers...)
	}

	var be backend.Backend = backend.Stub{}
	if *llama != "" {
		be = backend.NewLlamaServer(*llama, *model)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	n, err := node.New(ctx, node.Config{
		Name:         *name,
		IdentityPath: *identity,
		ListenPort:   *port,
		Backend:      be,
		Bootstrap:    bootstrap,
		Schedule:     *schedule,
		RPCWorker:    *rpcWorker,
		RPCServerBin: *rpcBin,
		RPCPort:      *rpcPort,
		Tier:         *tier,
		Tags:         splitCSV(*tags),
	})
	if err != nil {
		log.Fatalf("start node: %v", err)
	}
	defer n.Close()

	fmt.Printf("swarmai node %q up\n  peer id: %s\n  backend: %s (model=%q)\n", *name, n.Host.ID(), be.Name(), be.Model())
	for _, a := range n.Addrs() {
		fmt.Printf("  addr: %s\n", a)
	}

	ctrlAddr := control.DefaultControlAddr(*port)
	srv := control.NewServer(n, ctrlAddr)
	go func() {
		fmt.Printf("  control api: http://%s\n", ctrlAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("control api: %v", err)
		}
	}()

	<-ctx.Done()
	fmt.Println("\nshutting down…")
	sctx, cancel := context.WithTimeout(context.Background(), 5e9)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

func cmdQuery(path string, args []string) {
	fs := flag.NewFlagSet(path, flag.ExitOnError)
	port := fs.Int("port", 4779, "node port whose control api to query")
	_ = fs.Parse(args)
	body := get(fmt.Sprintf("http://%s/%s", control.DefaultControlAddr(*port), path))
	fmt.Println(prettyJSON(body))
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	port := fs.Int("port", 4779, "node port whose control api to query")
	model := fs.String("model", "", "requested model")
	redundancy := fs.Int("redundancy", 0, "run on N peers and take the majority (>=2 enables verification)")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, `usage: swarmai run "your prompt" [--redundancy N]`)
		os.Exit(2)
	}
	prompt := rest[0]
	u := fmt.Sprintf("http://%s/run?prompt=%s&model=%s&redundancy=%d",
		control.DefaultControlAddr(*port), url.QueryEscape(prompt), url.QueryEscape(*model), *redundancy)
	fmt.Println(prettyJSON(get(u)))
}

// cmdAsk answers as a routed, cross-checked ensemble ("combo").
func cmdAsk(args []string) {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	port := fs.Int("port", 4779, "node port whose control api to query")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, `usage: swarmai ask "your question"`)
		os.Exit(2)
	}
	u := fmt.Sprintf("http://%s/ask?prompt=%s",
		control.DefaultControlAddr(*port), url.QueryEscape(rest[0]))
	fmt.Println(prettyJSON(get(u)))
}

// splitCSV splits a comma-separated flag value into a trimmed, non-empty list.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cmdDraft answers a prompt with draft->verify (M2): draft locally, verify on a peer.
func cmdDraft(args []string) {
	fs := flag.NewFlagSet("draft", flag.ExitOnError)
	port := fs.Int("port", 4779, "node port whose control api to query")
	model := fs.String("model", "", "model the verifier should use")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, `usage: swarmai draft "your prompt"`)
		os.Exit(2)
	}
	u := fmt.Sprintf("http://%s/draft?prompt=%s&model=%s",
		control.DefaultControlAddr(*port), url.QueryEscape(rest[0]), url.QueryEscape(*model))
	fmt.Println(prettyJSON(get(u)))
}

func get(u string) []byte {
	resp, err := http.Get(u)
	if err != nil {
		log.Fatalf("cannot reach node control api (%v)\nis a node running? try: swarmai node start", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b
}

func prettyJSON(b []byte) string {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	return string(out)
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "swarmai-node"
	}
	return h
}

// multiFlag collects repeated string flags.
type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
