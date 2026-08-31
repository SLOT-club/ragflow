// Package invite encodes a compact "join token" so a new device can join the
// swarm by pasting one string instead of copying multiaddrs by hand.
package invite

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type payload struct {
	Peers []string `json:"peers"` // dialable /p2p/ multiaddrs
}

// Encode turns a node's dialable multiaddrs into a join token.
func Encode(addrs []string) string {
	b, _ := json.Marshal(payload{Peers: addrs})
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode parses a join token back into the list of bootstrap multiaddrs.
func Decode(token string) ([]string, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("invalid join token: %w", err)
	}
	var p payload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("invalid join token: %w", err)
	}
	if len(p.Peers) == 0 {
		return nil, fmt.Errorf("join token has no peers")
	}
	return p.Peers, nil
}
