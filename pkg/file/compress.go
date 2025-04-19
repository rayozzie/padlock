package file

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"

	"github.com/rayozzie/padlock/pkg/trace"
)

// CompressStreamToStream takes an io.Reader that it can read from and returns an io.Reader
// where it writes a compressed form of the stream using gzip.
func CompressStreamToStream(ctx context.Context, r io.Reader) io.Reader {
	log := trace.FromContext(ctx).WithPrefix("COMPRESS")
	log.Debugf("Starting compression of stream")
	pr, pw := io.Pipe()

	go func() {
		log.Debugf("Creating gzip writer")
		gzw := gzip.NewWriter(pw)
		log.Debugf("Copying input stream to gzip writer")
		written, err := io.Copy(gzw, r)

		if err != nil {
			log.Error(fmt.Errorf("error during compression: %w", err))
		} else {
			log.Debugf("Successfully copied %d bytes to gzip writer", written)
		}

		// Close gzip writer and pipe writer
		if err := gzw.Close(); err != nil {
			log.Error(fmt.Errorf("error closing gzip writer: %w", err))
			pw.CloseWithError(fmt.Errorf("error closing gzip writer: %w", err))
			return
		}

		log.Debugf("Compression completed successfully")
		pw.Close()
	}()

	return pr
}

// DecompressStreamToStream takes a compressed io.Reader that it can read from and returns an io.Reader
// where it writes the decompressed form of the stream.
func DecompressStreamToStream(ctx context.Context, r io.Reader) (io.Reader, error) {
	log := trace.FromContext(ctx).WithPrefix("DECOMPRESS")
	log.Debugf("Starting decompression of stream")

	// Read a small buffer to check if it's actually gzip data
	peekBuf := make([]byte, 512)
	n, err := r.Read(peekBuf)
	if err != nil && err != io.EOF {
		log.Error(fmt.Errorf("failed to read from input stream: %w", err))
		return nil, fmt.Errorf("failed to read from input stream: %w", err)
	}
	
	peekBuf = peekBuf[:n] // Truncate to actual bytes read
	
	// Create a new reader that first returns our peeked data, then the rest
	combinedReader := io.MultiReader(bytes.NewReader(peekBuf), r)
	
	// Check if the data has a valid gzip header
	if n < 2 || peekBuf[0] != 0x1f || peekBuf[1] != 0x8b {
		log.Debugf("Data does not appear to be gzip compressed, skipping decompression")
		// Return the combined reader without decompression
		return combinedReader, nil
	}
	
	// Create a new gzip reader
	gzr, err := gzip.NewReader(combinedReader)
	if err != nil {
		log.Error(fmt.Errorf("failed to create gzip reader: %w", err))
		// If we can't create a gzip reader but detected gzip header, something is wrong with the data
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}

	log.Debugf("Decompression started successfully")
	return gzr, nil
}
