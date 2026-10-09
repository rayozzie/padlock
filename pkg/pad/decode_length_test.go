// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func chunkWithClaimedSize(collection, size string, payload []byte) []byte {
	header := collection + ":1:" + size
	frame := append([]byte{byte(len(header))}, header...)
	return append(frame, payload...)
}

func TestDecodeRejectsChunkLengthOverflow(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name, size, message string
		total               int
	}{
		{"negative_product", strconv.Itoa(maxInt), "invalid chunk size", 3},
		{"zero_product", strconv.Itoa(maxInt/2 + 1), "invalid chunk size", 5},
		{"positive_product", strconv.Itoa(maxInt/2 + 2), "invalid chunk size", 5},
		{"integer_out_of_range", strconv.FormatUint(uint64(maxInt)+1, 10), "invalid chunk header", 3},
		{"negative_size", "-1", "invalid chunk header", 3},
		{"zero_size", "0", "invalid chunk header", 3},
	} {
		for _, badIndex := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/position=%d", tc.name, badIndex), func(t *testing.T) {
				ctx := context.Background()
				decoder, err := NewPadForDecode(ctx, 2)
				if err != nil {
					t.Fatal(err)
				}
				readers := make([]io.Reader, 2)
				var badCollection string
				for i := range readers {
					collection := fmt.Sprintf("2%c%d", 'A'+i, tc.total)
					size, payload := "1", make([]byte, tc.total-1)
					if i == badIndex {
						badCollection, size, payload = collection, tc.size, nil
					}
					readers[i] = bytes.NewReader(chunkWithClaimedSize(collection, size, payload))
				}
				var output bytes.Buffer
				err = decoder.Decode(ctx, readers, &output)
				if err == nil || !strings.Contains(err.Error(), tc.message) || !strings.Contains(err.Error(), badCollection) {
					t.Fatalf("got %v, want %q identifying %s", err, tc.message, badCollection)
				}
				if output.Len() != 0 {
					t.Fatal("invalid chunk length produced output")
				}
			})
		}
	}
}

func TestDecodeLargestNonoverflowingLengths(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		collection string
		size       int
	}{
		{"2A2", maxInt},
		{"2A3", maxInt / 2},
		{"2A5", maxInt / 4},
	} {
		t.Run(tc.collection, func(t *testing.T) {
			ctx := context.Background()
			decoder, err := NewPadForDecode(ctx, 2)
			if err != nil {
				t.Fatal(err)
			}
			reader := bytes.NewReader(chunkWithClaimedSize(tc.collection, strconv.Itoa(tc.size), nil))
			err = decoder.Decode(ctx, []io.Reader{reader, bytes.NewReader(nil)}, io.Discard)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("got %v, want a missing-payload error without allocating the claimed size", err)
			}
		})
	}
}

type payloadReadProbe struct {
	io.Reader
	largestRequest int
}

func (r *payloadReadProbe) Read(p []byte) (int, error) {
	if len(p) > r.largestRequest {
		r.largestRequest = len(p)
	}
	return r.Reader.Read(p)
}

func TestDecodeAllocatesOnlyForAvailablePayload(t *testing.T) {
	for _, size := range []int{0, 17, 4097} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := context.Background()
			decoder, err := NewPadForDecode(ctx, 2)
			if err != nil {
				t.Fatal(err)
			}
			frame := chunkWithClaimedSize("2A2", strconv.Itoa(16*1024*1024), make([]byte, size))
			reader := &payloadReadProbe{Reader: bytes.NewReader(frame)}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			err = decoder.Decode(ctx, []io.Reader{reader, bytes.NewReader(nil)}, io.Discard)
			runtime.ReadMemStats(&after)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("got %v, want a missing-payload error", err)
			}
			// Leave ample room for logging/runtime overhead, while rejecting an
			// allocation or read request based on the forged 16 MiB header.
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4*1024*1024 {
				t.Fatalf("allocated %d bytes for only %d bytes of payload", allocated, size)
			}
			if reader.largestRequest > 4*1024*1024 {
				t.Fatalf("issued a %d-byte read based on the untrusted header", reader.largestRequest)
			}
		})
	}
}

func TestDecodeChecksPayloadBeforeAdoptingParameters(t *testing.T) {
	ctx := context.Background()
	decoder, err := NewPadForDecode(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	// A valid-looking threshold must not change the pad's parameters before
	// discovering that the very first chunk has no payload.
	readers := make([]io.Reader, 10)
	for i := range readers {
		name := fmt.Sprintf("10%c20", 'A'+i)
		readers[i] = bytes.NewReader(chunkWithClaimedSize(name, "1", nil))
	}
	err = decoder.Decode(ctx, readers, io.Discard)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("got %v, want a missing-payload error", err)
	}
	if decoder.TotalCopies != 10 || decoder.PermutationCount != 1 {
		t.Fatal("decoder adopted new parameters from a header without its payload")
	}
}

type payloadFailure struct{ err error }

func (r payloadFailure) Read([]byte) (int, error) { return 0, r.err }

func TestDecodePreservesPayloadReadErrors(t *testing.T) {
	failure := errors.New("collection read failed")
	ctx := context.Background()
	decoder, err := NewPadForDecode(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	reader := io.MultiReader(
		bytes.NewReader(chunkWithClaimedSize("2A2", "4096", []byte("partial data"))),
		payloadFailure{failure},
	)
	var output bytes.Buffer
	err = decoder.Decode(ctx, []io.Reader{reader, bytes.NewReader(nil)}, &output)
	if !errors.Is(err, failure) {
		t.Fatalf("got %v, want original collection read failure", err)
	}
	if output.Len() != 0 {
		t.Fatal("failed payload read produced output")
	}
}
