// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
)

// ChunkPadder extends a serialized directory stream so that its length is a
// whole number of encoder input chunks. Collection sizes and chunk headers are
// public, and the exact length of a compressed archive depends on its
// contents. After padding they reveal only the chunk count.
//
// The padding follows the archive's end-of-archive blocks, which restore and
// dry-run validation drain without creating files, so existing decoders accept
// padded streams. A gzip stream receives one more gzip member made of stored
// (uncompressed) blocks, whose size is exact; an uncompressed TAR receives zero
// bytes. Library callers that use pad.Encode directly should align their input
// the same way.
type ChunkPadder struct {
	source  io.Reader
	chunk   int64
	gzipped bool
	total   int64
	trailer io.Reader // nil until the source reports EOF
	padding int64
}

// NewChunkPadder pads source to a multiple of chunkBytes. A chunk size below 1
// passes the stream through unchanged.
func NewChunkPadder(source io.Reader, chunkBytes int, gzipped bool) *ChunkPadder {
	return &ChunkPadder{source: source, chunk: int64(chunkBytes), gzipped: gzipped}
}

// Padding returns the number of appended bytes. It is final once Read has
// returned io.EOF.
func (p *ChunkPadder) Padding() int64 { return p.padding }

// Read passes the source through, then serves the padding. A source error
// other than io.EOF is returned as is, without any padding.
func (p *ChunkPadder) Read(buf []byte) (int, error) {
	if p.trailer == nil {
		n, err := p.source.Read(buf)
		p.total += int64(n)
		if err != io.EOF {
			return n, err
		}
		p.trailer = p.newTrailer()
		if n > 0 {
			return n, nil
		}
	}
	return p.trailer.Read(buf)
}

func (p *ChunkPadder) newTrailer() io.Reader {
	if p.chunk < 1 {
		return bytes.NewReader(nil)
	}
	shortfall := (p.chunk - p.total%p.chunk) % p.chunk
	if shortfall == 0 {
		return bytes.NewReader(nil)
	}
	if !p.gzipped {
		p.padding = shortfall
		return io.LimitReader(zeroReader{}, shortfall)
	}
	// A stored gzip member cannot be smaller than its framing. Add whole
	// chunks until it fits; the chunk count is public either way.
	for shortfall < gzipStoredMinBytes {
		shortfall += p.chunk
	}
	p.padding = shortfall
	return gzipStoredZeros(shortfall)
}

const (
	gzipHeaderBytes     = 10
	gzipTrailerBytes    = 8
	storedBlockHeader   = 5
	storedBlockMaxBytes = 65535
	// gzipStoredMinBytes is the size of a member holding one empty stored block.
	gzipStoredMinBytes = gzipHeaderBytes + storedBlockHeader + gzipTrailerBytes
)

// gzipStoredZeros returns a gzip member of exactly size bytes (at least
// gzipStoredMinBytes) that decompresses to zero bytes. Stored blocks have a
// fixed five-byte header, so any size from the minimum up is reachable. The
// member is generated as it is read; only the block framing is buffered.
func gzipStoredZeros(size int64) io.Reader {
	if size < gzipStoredMinBytes {
		size = gzipStoredMinBytes
	}
	// size = header + (5 + payload_i for each block) + trailer, payload_i <= 65535.
	body := size - gzipHeaderBytes - gzipTrailerBytes
	blocks := (body + storedBlockMaxBytes + storedBlockHeader - 1) / (storedBlockMaxBytes + storedBlockHeader)
	if blocks < 1 {
		blocks = 1
	}
	payload := body - storedBlockHeader*blocks
	readers := []io.Reader{bytes.NewReader([]byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 0xff})}
	remaining := payload
	for i := int64(0); i < blocks; i++ {
		length := remaining
		if length > storedBlockMaxBytes {
			length = storedBlockMaxBytes
		}
		remaining -= length
		header := []byte{0, byte(length), byte(length >> 8), ^byte(length), ^byte(length >> 8)}
		if i == blocks-1 {
			header[0] = 1 // BFINAL, BTYPE 00 (stored)
		}
		readers = append(readers, bytes.NewReader(header), io.LimitReader(zeroReader{}, length))
	}
	var trailer [gzipTrailerBytes]byte
	binary.LittleEndian.PutUint32(trailer[:4], crc32OfZeros(payload))
	binary.LittleEndian.PutUint32(trailer[4:], uint32(payload))
	readers = append(readers, bytes.NewReader(trailer[:]))
	return io.MultiReader(readers...)
}

func crc32OfZeros(n int64) uint32 {
	var zeros [32 * 1024]byte
	crc := uint32(0)
	for n > 0 {
		m := int64(len(zeros))
		if n < m {
			m = n
		}
		crc = crc32.Update(crc, crc32.IEEETable, zeros[:m])
		n -= m
	}
	return crc
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
