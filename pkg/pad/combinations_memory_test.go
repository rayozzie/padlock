// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"testing"
)

func TestPadSetupMemory(t *testing.T) {
	ctx := context.Background()
	// The first case detects table allocation without requiring a multi-GB
	// allocation if the regression returns. Then check the largest layouts.
	for _, tc := range [][2]int{{16, 8}, {26, 13}, {26, 14}, {26, 26}} {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		p, err := NewPadForEncode(ctx, tc[0], tc[1])
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1024*1024 {
			t.Fatalf("%d-of-%d setup allocated %d bytes; must not build combination tables", tc[1], tc[0], allocated)
		}
		if p.PermutationCount != collectionPermutationCount(tc[0], tc[1]) || len(p.Collections) != tc[0] {
			t.Fatal("setup produced incorrect collection metadata")
		}
		runtime.KeepAlive(p)
	}
}

type discardChunkWriter struct{}

func (discardChunkWriter) Write(p []byte) (int, error) { return len(p), nil }
func (discardChunkWriter) Close() error                { return nil }

type countingPadSource struct{ bytes int }

func (s *countingPadSource) Name() string { return "counting test source" }
func (s *countingPadSource) Read(ctx context.Context, p []byte) error {
	for i := range p {
		p[i] = byte(s.bytes + i)
	}
	s.bytes += len(p)
	return ctx.Err()
}

func TestEncodeCombinationMemory(t *testing.T) {
	ctx := context.Background()
	const total, required = 20, 10
	encoder, err := NewPadForEncode(ctx, total, required)
	if err != nil {
		t.Fatal(err)
	}
	// Several million distinct pad pieces must not require separate slice
	// headers or allocations. Include a shorter final chunk.
	input := []byte{0xa5, 0x1f, 0x67}
	source := new(countingPadSource)
	writtenChunks := 0
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err = encoder.Encode(ctx, 2*encoder.PermutationCount, bytes.NewReader(input), source,
		func(string, int, string) (io.WriteCloser, error) {
			writtenChunks++
			return discardChunkWriter{}, nil
		}, "bin")
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	outputBytes := len(input) * total * encoder.PermutationCount
	// Allow allocator rounding, logging, and runtime overhead; the old
	// per-combination allocations exceeded this bound many times over.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > uint64(2*outputBytes+2*1024*1024) {
		t.Fatalf("allocated %d bytes for %d payload bytes", allocated, outputBytes)
	}
	if wanted := len(input) * (required - 1) * combinationCount(total, required); source.bytes != wanted {
		t.Fatalf("consumed %d random bytes, want %d distinct pad bytes", source.bytes, wanted)
	}
	if writtenChunks != total*2 {
		t.Fatalf("wrote %d chunks, want %d", writtenChunks, total*2)
	}
}

type zeroPayloadReader struct{}

func (zeroPayloadReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestDecodeLargeThresholdWithoutCombinationTables(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("next collection read failed")
	for _, tc := range [][2]int{{20, 10}, {26, 13}} {
		total, required := tc[0], tc[1]
		decoder, err := NewPadForDecode(ctx, required)
		if err != nil {
			t.Fatal(err)
		}
		payloadSize := collectionPermutationCount(total, required)
		readers := make([]io.Reader, required)
		for i := range readers {
			readers[i] = payloadFailure{failure}
		}
		// A complete first payload used to trigger the full combination table,
		// even when the very next reader failed. Supply payload bytes lazily.
		name := fmt.Sprintf("%dA%d", required, total)
		readers[0] = io.MultiReader(
			bytes.NewReader(chunkWithClaimedSize(name, "1", nil)),
			io.LimitReader(zeroPayloadReader{}, int64(payloadSize)),
		)
		var restored bytes.Buffer
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		err = decoder.Decode(ctx, readers, &restored)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, failure) || restored.Len() != 0 {
			t.Fatalf("expected the next reader's error without output; got %v, %d bytes", err, restored.Len())
		}
		// ReadAll may grow its payload buffer several times. This bound allows
		// that growth but excludes allocating tables for millions of combinations.
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > uint64(8*payloadSize+2*1024*1024) {
			t.Fatalf("%d-of-%d decoder allocated %d bytes for %d payload bytes", required, total, allocated, payloadSize)
		}
	}
}

func TestEncodeCancellationStopsLargeThreshold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	encoder, err := NewPadForEncode(ctx, 26, 13)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	input := bytes.NewReader([]byte{1})
	err = encoder.Encode(ctx, encoder.PermutationCount, input, nil,
		func(string, int, string) (io.WriteCloser, error) {
			t.Fatal("canceled encode created output")
			return nil, nil
		}, "bin")
	if !errors.Is(err, context.Canceled) || input.Len() != 1 {
		t.Fatalf("canceled encode consumed input or returned the wrong error: %v", err)
	}
}
