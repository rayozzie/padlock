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
	Reader       *CollectionReader
	buffer       []byte
	offset       int
	ctx          context.Context
	currentChunk int // Track which chunk we're reading
}

// Path returns the path of the underlying collection
func (a *ChunkReaderAdapter) Path() string {
	return a.Reader.Collection.Path
}

// Name returns the name of the underlying collection
func (a *ChunkReaderAdapter) Name() string {
	return a.Reader.Collection.Name
}

// NewChunkReaderAdapter creates a new ChunkReaderAdapter from a CollectionReader
func NewChunkReaderAdapter(ctx context.Context, reader *CollectionReader) *ChunkReaderAdapter {
	return &ChunkReaderAdapter{
		Reader:       reader,
		ctx:          ctx,
		currentChunk: 1, // Start with chunk 1
	}
}

// SetCurrentChunk sets the current chunk index for the adapter
func (a *ChunkReaderAdapter) SetCurrentChunk(chunkIndex int) {
	a.currentChunk = chunkIndex
	// Reset buffer when changing chunks
	a.buffer = nil
	a.offset = 0
	
	// Also update the reader's chunk index to match
	a.Reader.ChunkIndex = chunkIndex
}

// Read implements io.Reader interface
func (a *ChunkReaderAdapter) Read(p []byte) (int, error) {
	log := trace.FromContext(a.ctx).WithPrefix("CHUNK-READER")

	// If buffer is empty or fully read, get next chunk
	if a.buffer == nil || a.offset >= len(a.buffer) {
		log.Debugf("Buffer empty or fully read, getting next chunk from collection %s (chunk %d)", 
			a.Reader.Collection.Name, a.currentChunk)
			
		// Make sure we reset the reader's chunk index to the one we want
		// This ensures we only read one chunk at a time
		a.Reader.ChunkIndex = a.currentChunk
		
		chunk, err := a.Reader.ReadNextChunk(a.ctx)
		if err != nil {
			if err == io.EOF {
				log.Debugf("Reached end of chunks (EOF) for collection %s", a.Reader.Collection.Name)
				
				// Signal that we've reached the end of this chunk
				return 0, io.EOF
			} else {
				log.Error(fmt.Errorf("error getting chunk %d from collection %s: %w", 
					a.currentChunk, a.Reader.Collection.Name, err))
				return 0, err
			}
		}
		
		// We've successfully read the chunk, but we don't increment the currentChunk counter here anymore
		// The caller (decode process) will control when to increment to the next chunk number
		
		log.Debugf("Got chunk %d (%d bytes) from collection %s", 
			a.currentChunk, len(chunk), a.Reader.Collection.Name)
		
		// DEBUG: Examine chunk content
		if len(chunk) > 0 {
			if len(chunk) < 50 {
				log.Tracef("Chunk content: %v", chunk)
			} else {
				log.Tracef("Chunk content (first 50 bytes): %v", chunk[:50])
			}
		}
		
		a.buffer = chunk
		a.offset = 0
	}

	// Copy data from buffer to p
	n := copy(p, a.buffer[a.offset:])
	a.offset += n
	log.Debugf("Read %d bytes from buffer (offset: %d, buffer size: %d)", n, a.offset, len(a.buffer))
	return n, nil
}
