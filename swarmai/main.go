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
	"syscall"

	"github.com/slot-club/swarmai/internal/backend"
	"github.com/slot-club/swarmai/internal/control"
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
	case "model":
		cmdModel(os.Args[2:])
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
  run "text"        answer a prompt using the best compute in the swarm
  model share PATH  chunk + seed a model file, print its manifest id
  model fetch ID    stream a model from a seeding peer (-o OUT)
  model list        show models seeded locally and by peers

Run "swarmai node start -h" for node flags.
`)
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
	var bootstrap multiFlag
	fs.Var(&bootstrap, "bootstrap", "extra bootstrap peer multiaddr (repeatable)")
	_ = fs.Parse(args[1:])

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
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, `usage: swarmai run "your prompt"`)
		os.Exit(2)
	}
	prompt := rest[0]
	u := fmt.Sprintf("http://%s/run?prompt=%s&model=%s",
		control.DefaultControlAddr(*port), url.QueryEscape(prompt), url.QueryEscape(*model))
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
