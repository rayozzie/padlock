// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/trace"
)

type compressionErrorReader struct {
	data          []byte
	err           error
	errorWithData bool
}

func (r *compressionErrorReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 && r.errorWithData {
		return n, r.err
	}
	return n, nil
}

func TestCompressionPreservesSourceErrors(t *testing.T) {
	failure := errors.New("injected source read failure")
	for _, tc := range []struct {
		name          string
		size          int
		err           error
		errorWithData bool
	}{
		{name: "before_data", err: failure},
		{name: "after_data", size: 100, err: failure},
		{name: "with_data", size: 100, err: failure, errorWithData: true},
		{name: "after_multiple_reads", size: 128 * 1024, err: failure},
		{name: "unexpected_eof", size: 100, err: io.ErrUnexpectedEOF},
		{name: "cancelled_source", size: 100, err: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &compressionErrorReader{
				data: bytes.Repeat([]byte("x"), tc.size), err: tc.err, errorWithData: tc.errorWithData,
			}
			compressed := CompressStreamToStream(context.Background(), source)
			_, err := io.ReadAll(compressed)
			if !errors.Is(err, tc.err) {
				t.Fatalf("compressed stream returned %v, want source error %v", err, tc.err)
			}
			// A source's unexpected EOF must not be confused with ReadFull's
			// normal short final chunk by the encoder.
			if err == io.ErrUnexpectedEOF {
				t.Fatal("source error lost its compression context")
			}
		})
	}
}

func TestCompressStreamToStream(t *testing.T) {
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create test data
	testData := strings.Repeat("The quick brown fox jumps over the lazy dog.", 100)
	reader := strings.NewReader(testData)

	// Compress the data
	compressedReader := CompressStreamToStream(ctx, reader)

	// Read all compressed data
	compressedData, err := io.ReadAll(compressedReader)
	if err != nil {
		t.Fatalf("Failed to read compressed data: %v", err)
	}

	// Make sure the compressed data is smaller than the original
	// (for this test, it should be much smaller)
	if len(compressedData) >= len(testData) {
		t.Errorf("Compressed data is not smaller than original: %d >= %d", len(compressedData), len(testData))
	}

	// Make sure the compressed data is not empty
	if len(compressedData) == 0 {
		t.Errorf("Compressed data is empty")
	}
}

func TestDecompressStreamToStream(t *testing.T) {
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create test data
	testData := strings.Repeat("The quick brown fox jumps over the lazy dog.", 100)
	reader := strings.NewReader(testData)

	// Compress the data
	compressedReader := CompressStreamToStream(ctx, reader)

	// Decompress the data
	decompressedReader, err := DecompressStreamToStream(ctx, compressedReader)
	if err != nil {
		t.Fatalf("DecompressStreamToStream failed: %v", err)
	}

	// Read all decompressed data
	decompressedData, err := io.ReadAll(decompressedReader)
	if err != nil {
		t.Fatalf("Failed to read decompressed data: %v", err)
	}

	// Verify the decompressed data matches the original
	if string(decompressedData) != testData {
		t.Errorf("Decompressed data does not match original")
		if len(decompressedData) < 100 && len(testData) >= 100 {
			t.Logf("First 100 bytes of decompressed: %q", decompressedData)
			t.Logf("First 100 bytes of original: %q", testData[:100])
		}
	}
}

func TestCompressDecompressRoundTripBinary(t *testing.T) {
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create binary test data
	testData := make([]byte, 1000)
	for i := range testData {
		testData[i] = byte(i % 256)
	}
	reader := bytes.NewBuffer(testData)

	// Compress the data
	compressedReader := CompressStreamToStream(ctx, reader)

	// Decompress the data
	decompressedReader, err := DecompressStreamToStream(ctx, compressedReader)
	if err != nil {
		t.Fatalf("DecompressStreamToStream failed: %v", err)
	}

	// Read all decompressed data
	decompressedData, err := io.ReadAll(decompressedReader)
	if err != nil {
		t.Fatalf("Failed to read decompressed data: %v", err)
	}

	// Verify the decompressed data matches the original
	if !bytes.Equal(decompressedData, testData) {
		t.Errorf("Decompressed data does not match original")
		if len(decompressedData) < 20 && len(testData) >= 20 {
			t.Logf("First 20 bytes of decompressed: %v", decompressedData)
			t.Logf("First 20 bytes of original: %v", testData[:20])
		}
	}
}

func TestDecompressInvalidInput(t *testing.T) {
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create invalid gzip data
	invalidData := []byte{0x1f, 0x8b, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00} // Invalid gzip header

	// Try to decompress it
	_, err := DecompressStreamToStream(ctx, bytes.NewReader(invalidData))

	// Should fail
	if err == nil {
		t.Errorf("Expected error when decompressing invalid data, got nil")
	}
}

func TestCompressEmptyInput(t *testing.T) {
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create empty input
	emptyReader := strings.NewReader("")

	// Compress it
	compressedReader := CompressStreamToStream(ctx, emptyReader)

	// Read all compressed data
	compressedData, err := io.ReadAll(compressedReader)
	if err != nil {
		t.Fatalf("Failed to read compressed data: %v", err)
	}

	// Make sure we got something (gzip header at least)
	if len(compressedData) == 0 {
		t.Errorf("Compressed empty input gave empty output")
	}

	// Decompress it
	decompressedReader, err := DecompressStreamToStream(ctx, bytes.NewReader(compressedData))
	if err != nil {
		t.Fatalf("DecompressStreamToStream failed: %v", err)
	}

	// Read all decompressed data
	decompressedData, err := io.ReadAll(decompressedReader)
	if err != nil {
		t.Fatalf("Failed to read decompressed data: %v", err)
	}

	// Should be empty
	if len(decompressedData) != 0 {
		t.Errorf("Decompressed empty input is not empty: %v", decompressedData)
	}
}
