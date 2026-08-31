// Package cell forms a "LAN cell": several peers that jointly run one model
// bigger than any of them can hold, using llama.cpp's RPC backend.
//
// The security-critical design decision: llama.cpp's rpc-server has NO
// authentication or encryption and has had an unauthenticated RCE
// (CVE-2026-34159). So swarmai NEVER exposes the rpc-server port to the
// network. Instead:
//
//   - a worker runs rpc-server bound to loopback only, and serves a libp2p
//     stream protocol that proxies bytes to that loopback port;
//   - the coordinator opens a loopback TCP listener per worker that proxies to
//     the worker over an authenticated, encrypted libp2p stream, and points
//     llama's --rpc at those local loopback addresses.
//
// The raw RPC bytes thus travel only over loopback and inside the libp2p
// secure channel — never over an open TCP port.
package cell

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// RPCProtocol tunnels llama.cpp RPC traffic inside an authenticated libp2p stream.
const RPCProtocol protocol.ID = "/swarmai/rpc/1.0.0"

// pipe copies bytes in both directions between a and b until either side ends,
// then closes both so the other copy unblocks.
func pipe(a, b io.ReadWriteCloser) {
	var once sync.Once
	closeBoth := func() { _ = a.Close(); _ = b.Close() }
	go func() {
		_, _ = io.Copy(a, b)
		once.Do(closeBoth)
	}()
	_, _ = io.Copy(b, a)
	once.Do(closeBoth)
}

// RegisterWorker makes this host serve as an RPC worker: incoming RPC streams
// are proxied to a local rpc-server listening on loopback at localRPCAddr
// (e.g. "127.0.0.1:50052"). Registering the handler is what makes a node a
// worker; nodes that do not call this reject the protocol.
func RegisterWorker(h host.Host, localRPCAddr string) {
	h.SetStreamHandler(RPCProtocol, func(s network.Stream) {
		conn, err := net.Dial("tcp", localRPCAddr)
		if err != nil {
			_ = s.Reset()
			return
		}
		pipe(s, conn)
	})
}

// StartRPCServer spawns llama.cpp's rpc-server bound to loopback. binPath is the
// path to the rpc-server binary; if it is empty or missing, no process is
// started (the worker tunnel can still target an already-running server or a
// mock on the same port). Returns the started command (may be nil).
func StartRPCServer(ctx context.Context, binPath string, port int) (*exec.Cmd, error) {
	if binPath == "" {
		return nil, nil
	}
	if _, err := exec.LookPath(binPath); err != nil {
		return nil, fmt.Errorf("rpc-server binary %q not found: %w", binPath, err)
	}
	// -H 127.0.0.1 keeps it loopback-only; swarmai provides the network reach.
	cmd := exec.CommandContext(ctx, binPath, "-H", "127.0.0.1", "-p", strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// Worker describes a cell member the coordinator will use, with the free RAM
// used to weight the tensor split.
type Worker struct {
	Peer      peer.ID
	Name      string
	RAMFreeMB uint64
}

// Coordinator sets up secure loopback→libp2p tunnels to each worker and derives
// the llama.cpp launch flags (--rpc, --tensor-split) for a cell.
type Coordinator struct {
	host      host.Host
	workers   []Worker
	ctx       context.Context
	cancel    context.CancelFunc
	listeners []net.Listener
	localAddr []string
	mu        sync.Mutex
}

// NewCoordinator builds a coordinator for the given workers. Its tunnels live
// until Close, independent of any request context.
func NewCoordinator(h host.Host, workers []Worker) *Coordinator {
	ctx, cancel := context.WithCancel(context.Background())
	return &Coordinator{host: h, workers: workers, ctx: ctx, cancel: cancel}
}

// Setup opens one loopback listener per worker, each proxying accepted
// connections to that worker over the RPC libp2p protocol. Must be called
// before RPCArg. The tunnels stay open until Close.
func (c *Coordinator) Setup() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.workers {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("listen for worker %s: %w", w.Name, err)
		}
		c.listeners = append(c.listeners, ln)
		c.localAddr = append(c.localAddr, ln.Addr().String())
		go c.acceptLoop(ln, w.Peer)
	}
	return nil
}

// acceptLoop bridges each local connection to a fresh libp2p stream to the worker.
func (c *Coordinator) acceptLoop(ln net.Listener, worker peer.ID) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed
		}
		go func() {
			s, err := c.host.NewStream(c.ctx, worker, RPCProtocol)
			if err != nil {
				_ = conn.Close()
				return
			}
			pipe(conn, s)
		}()
	}
}

// RPCArg returns the value for llama.cpp's --rpc flag: the loopback addresses of
// the per-worker tunnels, in worker order.
func (c *Coordinator) RPCArg() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.localAddr, ",")
}

// TensorSplit returns llama.cpp's --tensor-split value: proportions across
// devices in the order llama sees them — local (coordinator) first, then each
// worker — weighted by free RAM. coordFreeMB is the coordinator's own free RAM.
func (c *Coordinator) TensorSplit(coordFreeMB uint64) string {
	weights := make([]uint64, 0, len(c.workers)+1)
	weights = append(weights, coordFreeMB)
	for _, w := range c.workers {
		weights = append(weights, w.RAMFreeMB)
	}
	var total uint64
	for _, x := range weights {
		total += x
	}
	if total == 0 {
		total = 1
	}
	parts := make([]string, len(weights))
	for i, x := range weights {
		parts[i] = strconv.FormatFloat(float64(x)/float64(total), 'f', 3, 64)
	}
	return strings.Join(parts, ",")
}

// Close tears down all tunnels.
func (c *Coordinator) Close() {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ln := range c.listeners {
		_ = ln.Close()
	}
	c.listeners = nil
}
