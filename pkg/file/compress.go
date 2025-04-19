package file

import (
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

	// Create a new gzip reader
	gzr, err := gzip.NewReader(r)
	if err != nil {
		log.Error(fmt.Errorf("failed to create gzip reader: %w", err))
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}

	log.Debugf("Decompression started successfully")
	return gzr, nil
}
