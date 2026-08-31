package invite

import (
	"reflect"
	"testing"
)

func TestEncodeDecodeRoundtrip(t *testing.T) {
	addrs := []string{
		"/ip4/192.168.1.10/tcp/4779/p2p/12D3KooWAifQeaw9je5JGSw4XxLQFnAsX6ARyc2gFeSi2WSuacQJ",
		"/ip4/127.0.0.1/tcp/4779/p2p/12D3KooWAifQeaw9je5JGSw4XxLQFnAsX6ARyc2gFeSi2WSuacQJ",
	}
	got, err := Decode(Encode(addrs))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(got, addrs) {
		t.Fatalf("roundtrip mismatch: %v", got)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := Decode("!!!not-base64!!!"); err == nil {
		t.Fatal("garbage token should error")
	}
	if _, err := Decode(Encode(nil)); err == nil {
		t.Fatal("token with no peers should error")
	}
}
