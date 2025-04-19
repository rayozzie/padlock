package file

import (
	"context"
	"fmt"
	"io"

	"github.com/rayozzie/padlock/pkg/trace"
)

// ChunkWriter is a simple io.WriteCloser that accumulates data and writes it on close
type ChunkWriter struct {
	ctx       context.Context
	formatter Formatter
	collPath  string
	collIndex int
	chunkNum  int
	chunkData []byte
}

// NewChunkWriter creates a new ChunkWriter for a specific collection and chunk
func NewChunkWriter(ctx context.Context, formatter Formatter, collPath string, collIndex int, chunkNum int) *ChunkWriter {
	return &ChunkWriter{
		ctx:       ctx,
		formatter: formatter,
		collPath:  collPath,
		collIndex: collIndex,
		chunkNum:  chunkNum,
		chunkData: make([]byte, 0),
	}
}

// Write implements io.Writer interface
func (cw *ChunkWriter) Write(p []byte) (n int, err error) {
	cw.chunkData = append(cw.chunkData, p...)
	return len(p), nil
}

// Close implements io.Closer interface
func (cw *ChunkWriter) Close() error {
	return cw.formatter.WriteChunk(cw.ctx, cw.collPath, cw.collIndex, cw.chunkNum, cw.chunkData)
}

// ChunkReaderAdapter adapts CollectionReader to io.Reader
type ChunkReaderAdapter struct {
	Reader *CollectionReader
	buffer []byte
	offset int
	ctx    context.Context
}

// NewChunkReaderAdapter creates a new ChunkReaderAdapter from a CollectionReader
func NewChunkReaderAdapter(ctx context.Context, reader *CollectionReader) *ChunkReaderAdapter {
	return &ChunkReaderAdapter{
		Reader: reader,
		ctx:    ctx,
	}
}

// Read implements io.Reader interface
func (a *ChunkReaderAdapter) Read(p []byte) (int, error) {
	log := trace.FromContext(a.ctx).WithPrefix("CHUNK-READER")

	// If buffer is empty or fully read, get next chunk
	if a.buffer == nil || a.offset >= len(a.buffer) {
		log.Debugf("Buffer empty or fully read, getting next chunk from collection %s", a.Reader.Collection.Name)
		chunk, err := a.Reader.ReadNextChunk(a.ctx)
		if err != nil {
			if err == io.EOF {
				log.Debugf("Reached end of chunks (EOF)")
			} else {
				log.Error(fmt.Errorf("error getting next chunk: %w", err))
			}
			return 0, err
		}
		log.Debugf("Got next chunk (%d bytes)", len(chunk))
		a.buffer = chunk
		a.offset = 0
	}

	// Copy data from buffer to p
	n := copy(p, a.buffer[a.offset:])
	a.offset += n
	log.Debugf("Read %d bytes from buffer (offset: %d, buffer size: %d)", n, a.offset, len(a.buffer))
	return n, nil
}
