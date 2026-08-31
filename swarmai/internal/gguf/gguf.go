// Package gguf reads the layout of a GGUF model file: the byte range of every
// tensor. swarmai turns that into a part→range map so peers can fetch
// individual experts/layers on demand instead of the whole model.
//
// Only the header, metadata, and tensor directory are read (the front of the
// file); the multi-gigabyte tensor data is never loaded. Tensor byte sizes are
// derived from the consecutive tensor offsets, so no ggml type-size table is
// needed and quantized types work unchanged.
package gguf

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
)

const magic = "GGUF"

// GGUF metadata value type ids.
const (
	typeUint8   = 0
	typeInt8    = 1
	typeUint16  = 2
	typeInt16   = 3
	typeUint32  = 4
	typeInt32   = 5
	typeFloat32 = 6
	typeBool    = 7
	typeString  = 8
	typeArray   = 9
	typeUint64  = 10
	typeInt64   = 11
	typeFloat64 = 12
)

// Tensor is one tensor's identity and byte range in the file.
type Tensor struct {
	Name   string
	Offset uint64 // offset within the tensor-data section
	Dims   []uint64
	Type   uint32
	Start  int64 // absolute byte offset in the file
	Size   int64 // byte length (includes trailing alignment padding)
}

// Layout is the parsed front of a GGUF file.
type Layout struct {
	Version   uint32
	Alignment uint64
	DataStart int64
	FileSize  int64
	Tensors   []Tensor
}

type reader struct {
	r   *bufio.Reader
	pos int64
}

func (rd *reader) readFull(p []byte) error {
	n, err := io.ReadFull(rd.r, p)
	rd.pos += int64(n)
	return err
}

func (rd *reader) u32() (uint32, error) {
	var b [4]byte
	if err := rd.readFull(b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func (rd *reader) u64() (uint64, error) {
	var b [8]byte
	if err := rd.readFull(b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

func (rd *reader) str() (string, error) {
	n, err := rd.u64()
	if err != nil {
		return "", err
	}
	if n > 1<<20 {
		return "", fmt.Errorf("implausible string length %d", n)
	}
	b := make([]byte, n)
	if err := rd.readFull(b); err != nil {
		return "", err
	}
	return string(b), nil
}

func (rd *reader) skip(n int64) error {
	m, err := io.CopyN(io.Discard, rd.r, n)
	rd.pos += m
	return err
}

// skipValue consumes a metadata value of the given type.
func (rd *reader) skipValue(vt uint32) error {
	switch vt {
	case typeUint8, typeInt8, typeBool:
		return rd.skip(1)
	case typeUint16, typeInt16:
		return rd.skip(2)
	case typeUint32, typeInt32, typeFloat32:
		return rd.skip(4)
	case typeUint64, typeInt64, typeFloat64:
		return rd.skip(8)
	case typeString:
		_, err := rd.str()
		return err
	case typeArray:
		at, err := rd.u32()
		if err != nil {
			return err
		}
		n, err := rd.u64()
		if err != nil {
			return err
		}
		for i := uint64(0); i < n; i++ {
			if err := rd.skipValue(at); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown metadata value type %d", vt)
	}
}

func align(x, a int64) int64 {
	if a <= 1 {
		return x
	}
	return (x + a - 1) / a * a
}

// Parse reads a GGUF file's layout.
func Parse(path string) (*Layout, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	rd := &reader{r: bufio.NewReaderSize(f, 1<<16)}
	var m [4]byte
	if err := rd.readFull(m[:]); err != nil {
		return nil, err
	}
	if string(m[:]) != magic {
		return nil, fmt.Errorf("not a GGUF file (magic %q)", m)
	}
	version, err := rd.u32()
	if err != nil {
		return nil, err
	}
	tensorCount, err := rd.u64()
	if err != nil {
		return nil, err
	}
	kvCount, err := rd.u64()
	if err != nil {
		return nil, err
	}

	alignment := uint64(32) // GGUF default
	for i := uint64(0); i < kvCount; i++ {
		key, err := rd.str()
		if err != nil {
			return nil, err
		}
		vt, err := rd.u32()
		if err != nil {
			return nil, err
		}
		if key == "general.alignment" && vt == typeUint32 {
			a, err := rd.u32()
			if err != nil {
				return nil, err
			}
			if a > 0 {
				alignment = uint64(a)
			}
			continue
		}
		if err := rd.skipValue(vt); err != nil {
			return nil, fmt.Errorf("metadata %q: %w", key, err)
		}
	}

	tensors := make([]Tensor, 0, tensorCount)
	for i := uint64(0); i < tensorCount; i++ {
		name, err := rd.str()
		if err != nil {
			return nil, err
		}
		ndims, err := rd.u32()
		if err != nil {
			return nil, err
		}
		dims := make([]uint64, ndims)
		for j := uint32(0); j < ndims; j++ {
			if dims[j], err = rd.u64(); err != nil {
				return nil, err
			}
		}
		ttype, err := rd.u32()
		if err != nil {
			return nil, err
		}
		off, err := rd.u64()
		if err != nil {
			return nil, err
		}
		tensors = append(tensors, Tensor{Name: name, Offset: off, Dims: dims, Type: ttype})
	}

	dataStart := align(rd.pos, int64(alignment))

	// Tensor byte sizes come from consecutive offsets in the data section.
	order := make([]int, len(tensors))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return tensors[order[a]].Offset < tensors[order[b]].Offset })
	for rank, i := range order {
		start := dataStart + int64(tensors[i].Offset)
		var end int64
		if rank+1 < len(order) {
			end = dataStart + int64(tensors[order[rank+1]].Offset)
		} else {
			end = info.Size()
		}
		tensors[i].Start = start
		tensors[i].Size = end - start
	}

	return &Layout{
		Version:   version,
		Alignment: alignment,
		DataStart: dataStart,
		FileSize:  info.Size(),
		Tensors:   tensors,
	}, nil
}

// TensorInfo describes a tensor to write in a GGUF header.
type TensorInfo struct {
	Name   string
	Offset uint64
	Dims   []uint64
	Type   uint32
}

// WriteHeader writes a minimal valid GGUF header (magic, version 3, a
// general.alignment metadata key, and the tensor directory) padded to the
// alignment, and returns the byte offset at which tensor data must follow. It
// backs test fixtures and layout tooling.
func WriteHeader(w io.Writer, tensors []TensorInfo, alignment uint64) (int64, error) {
	if alignment == 0 {
		alignment = 32
	}
	var b bytes.Buffer
	b.WriteString(magic)
	putU32(&b, 3)
	putU64(&b, uint64(len(tensors)))
	putU64(&b, 1) // one metadata key: general.alignment
	putStr(&b, "general.alignment")
	putU32(&b, typeUint32)
	putU32(&b, uint32(alignment))
	for _, t := range tensors {
		putStr(&b, t.Name)
		putU32(&b, uint32(len(t.Dims)))
		for _, d := range t.Dims {
			putU64(&b, d)
		}
		putU32(&b, t.Type)
		putU64(&b, t.Offset)
	}
	dataStart := align(int64(b.Len()), int64(alignment))
	b.Write(make([]byte, dataStart-int64(b.Len())))
	if _, err := w.Write(b.Bytes()); err != nil {
		return 0, err
	}
	return dataStart, nil
}

func putU32(b *bytes.Buffer, v uint32) {
	var p [4]byte
	binary.LittleEndian.PutUint32(p[:], v)
	b.Write(p[:])
}

func putU64(b *bytes.Buffer, v uint64) {
	var p [8]byte
	binary.LittleEndian.PutUint64(p[:], v)
	b.Write(p[:])
}

func putStr(b *bytes.Buffer, s string) {
	putU64(b, uint64(len(s)))
	b.WriteString(s)
}
