// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"context"
	"fmt"
	"io"

	"github.com/rayozzie/padlock/pkg/trace"
)

// ChunkWriter accumulates one encoded chunk and writes it on close.
type ChunkWriter struct {
	ctx       context.Context
	formatter Formatter
	collPath  string
	collIndex int
	chunkNum  int
	chunkData []byte
}

// NamedChunkWriter is like ChunkWriter but allows specifying a collection name
// that is different from the directory basename
type NamedChunkWriter struct {
	Ctx       context.Context
	Formatter Formatter
	CollPath  string
	CollName  string // Use this name for the files instead of basename
	ChunkNum  int
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

// Write implements io.Writer interface for NamedChunkWriter
func (cw *NamedChunkWriter) Write(p []byte) (n int, err error) {
	if cw.chunkData == nil {
		cw.chunkData = make([]byte, 0)
	}
	cw.chunkData = append(cw.chunkData, p...)
	return len(p), nil
}

// Close implements io.Closer interface for NamedChunkWriter
func (cw *NamedChunkWriter) Close() error {

	// Call the custom write function that uses Collection name instead of path basename
	return WriteNamedChunk(cw.Ctx, cw.Formatter, cw.CollPath, cw.CollName, cw.ChunkNum, cw.chunkData)
}

// ChunkReaderAdapter adapts a CollectionReader to io.Reader
type ChunkReaderAdapter struct {
	Reader       *CollectionReader // Standard directory/tar-based reader
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

// BufferedChunk returns the unread bytes of the current encoded chunk without
// advancing the stream. The caller must not modify them; they remain valid only
// until the next Read or SetCurrentChunk call. Preflight can compare duplicate
// first-chunk payloads after reading their headers without another allocation.
func (a *ChunkReaderAdapter) BufferedChunk() []byte {
	return a.buffer[a.offset:]
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
		collName := a.Reader.Collection.Name

		log.Debugf("Getting next chunk from collection %s (chunk %d)", collName, a.currentChunk)

		// Make sure we reset the reader's chunk index to the one we want
		// This ensures we only read one chunk at a time
		a.Reader.ChunkIndex = a.currentChunk
		chunk, err := a.Reader.ReadNextChunk(a.ctx)

		if err != nil {
			if err == io.EOF {
				log.Debugf("Reached end of chunks (EOF) for collection %s", collName)

				// Increment currentChunk even on EOF so we're ready for the next call
				a.currentChunk++
				// Signal that we've reached the end of this chunk
				return 0, io.EOF
			} else {
				log.Error(fmt.Errorf("error getting chunk %d from collection %s: %w",
					a.currentChunk, collName, err))
				return 0, err
			}
		}

		// We've successfully read the chunk, increment the chunk number for next time
		a.currentChunk++

		// Get the current chunk index for logging (subtract 1 because it was already incremented)
		chunkIndex := a.Reader.ChunkIndex - 1

		log.Debugf("Got chunk %d (%d bytes) from collection %s",
			chunkIndex, len(chunk), collName)

		a.buffer = chunk
		a.offset = 0
	}

	// Copy data from buffer to p
	n := copy(p, a.buffer[a.offset:])
	a.offset += n
	log.Debugf("Read %d bytes from buffer (offset: %d, buffer size: %d)", n, a.offset, len(a.buffer))
	return n, nil
}
