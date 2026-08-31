package cell

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	tcp "github.com/libp2p/go-libp2p/p2p/transport/tcp"
)

// startEcho runs a loopback TCP echo server that prefixes replies with "ECHO:".
// It stands in for llama.cpp's rpc-server in tests.
func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						_, _ = c.Write(append([]byte("ECHO:"), buf[:n]...))
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

// testHost builds a TCP-only libp2p host (QUIC is disabled project-wide).
func testHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("libp2p host: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// TestSecureTunnel proves RPC bytes travel coordinator→libp2p→worker→loopback
// and back, without ever exposing the worker's raw port.
func TestSecureTunnel(t *testing.T) {
	echoAddr := startEcho(t)
	worker := testHost(t)
	coord := testHost(t)
	RegisterWorker(worker, echoAddr)

	if err := coord.Connect(context.Background(), peer.AddrInfo{ID: worker.ID(), Addrs: worker.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	c := NewCoordinator(coord, []Worker{{Peer: worker.ID(), Name: "w", RAMFreeMB: 100}})
	if err := c.Setup(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer c.Close()

	conn, err := net.Dial("tcp", c.RPCArg())
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(buf[:n]); got != "ECHO:hello" {
		t.Fatalf("tunnel returned %q, want %q", got, "ECHO:hello")
	}
}

func TestTensorSplitWeightedByRAM(t *testing.T) {
	c := NewCoordinator(nil, []Worker{{RAMFreeMB: 100}, {RAMFreeMB: 300}})
	// coordinator free=100, workers 100 and 300 → weights [100,100,300]/500.
	if got, want := c.TensorSplit(100), "0.200,0.200,0.600"; got != want {
		t.Fatalf("TensorSplit = %q, want %q", got, want)
	}
}
