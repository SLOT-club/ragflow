package gguf

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writeGGUF builds a tiny valid GGUF file: three tensors laid out consecutively
// at offsets 0/64/128 in a 192-byte data section whose bytes are 0..191.
func writeGGUF(t *testing.T) (string, int64, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model.gguf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	tensors := []TensorInfo{
		{Name: "a", Offset: 0, Dims: []uint64{16}, Type: 0},
		{Name: "b", Offset: 64, Dims: []uint64{16}, Type: 0},
		{Name: "c", Offset: 128, Dims: []uint64{16}, Type: 0},
	}
	dataStart, err := WriteHeader(f, tensors, 32)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 192)
	for i := range data {
		data[i] = byte(i)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	return path, dataStart, data
}

func TestParseLayout(t *testing.T) {
	path, dataStart, data := writeGGUF(t)

	l, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if l.Alignment != 32 {
		t.Fatalf("alignment = %d, want 32", l.Alignment)
	}
	if l.DataStart != dataStart {
		t.Fatalf("DataStart = %d, want %d", l.DataStart, dataStart)
	}
	if len(l.Tensors) != 3 {
		t.Fatalf("tensors = %d, want 3", len(l.Tensors))
	}

	byName := map[string]Tensor{}
	for _, tn := range l.Tensors {
		byName[tn.Name] = tn
	}
	for _, tc := range []struct {
		name        string
		start, size int64
	}{
		{"a", dataStart, 64},
		{"b", dataStart + 64, 64},
		{"c", dataStart + 128, 64},
	} {
		got := byName[tc.name]
		if got.Start != tc.start || got.Size != tc.size {
			t.Fatalf("tensor %q = start %d size %d, want start %d size %d", tc.name, got.Start, got.Size, tc.start, tc.size)
		}
	}

	// The computed range for "b" must isolate exactly its data bytes.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b := byName["b"]
	if !bytes.Equal(raw[b.Start:b.Start+b.Size], data[64:128]) {
		t.Fatal("tensor b's byte range does not match its data")
	}
}

func TestParseRejectsNonGGUF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.bin")
	if err := os.WriteFile(path, []byte("not a gguf file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(path); err == nil {
		t.Fatal("Parse should reject a non-GGUF file")
	}
}
