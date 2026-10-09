// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestDecodeRejectsMismatchedChunkSizes(t *testing.T) {
	ctx := context.Background()
	const chunkBytes = 64
	want := make([]byte, 2*chunkBytes+17)
	for i := range want {
		want[i] = byte(i*37 + i/7)
	}
	for _, tc := range []struct {
		name            string
		total, required int
		order           []string
	}{
		{"2_of_2", 2, 2, []string{"2A2", "2B2"}},
		{"2_of_3", 3, 2, []string{"2C3", "2A3"}},
		{"2_of_3_with_extra", 3, 2, []string{"2C3", "2B3", "2A3"}},
		{"3_of_5_with_extras", 5, 3, []string{"3E5", "3C5", "3A5", "3D5", "3B5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoder, err := NewPadForEncode(ctx, tc.total, tc.required)
			if err != nil {
				t.Fatal(err)
			}
			chunks := make(map[string][]*bytes.Buffer, tc.total)
			err = encoder.Encode(ctx, chunkBytes*encoder.PermutationCount, bytes.NewReader(want), NewTestRNG(0x37),
				func(name string, _ int, _ string) (io.WriteCloser, error) {
					chunk := new(bytes.Buffer)
					chunks[name] = append(chunks[name], chunk)
					return &nopCloser{chunk}, nil
				}, "bin")
			if err != nil {
				t.Fatal(err)
			}

			// Change the first, middle, or final chunk in each input position.
			// Keep its payload consistent with its own header to isolate the
			// disagreement between collections, including unselected extras.
			for badIndex, badCollection := range tc.order {
				for badChunk := 0; badChunk < 3; badChunk++ {
					for _, delta := range []int{-1, 1} {
						t.Run(fmt.Sprintf("collection=%s/chunk=%d/delta=%d", badCollection, badChunk+1, delta), func(t *testing.T) {
							expectedSize := chunkBytes
							if badChunk == 2 {
								expectedSize = 17
							}
							readers := make([]io.Reader, len(tc.order))
							for i, name := range tc.order {
								var stream bytes.Buffer
								for j, chunk := range chunks[name] {
									frame := chunk.Bytes()
									if i == badIndex && j == badChunk {
										payload := make([]byte, (expectedSize+delta)*encoder.PermutationCount)
										copy(payload, frame[1+int(frame[0]):])
										fields := strings.Split(string(frame[1:1+int(frame[0])]), ":")
										fields[2] = strconv.Itoa(expectedSize + delta)
										header := strings.Join(fields, ":")
										frame = append([]byte{byte(len(header))}, header...)
										frame = append(frame, payload...)
									}
									stream.Write(frame)
								}
								readers[i] = bytes.NewReader(stream.Bytes())
							}
							decoder, err := NewPadForDecode(ctx, len(readers))
							if err != nil {
								t.Fatal(err)
							}
							var restored bytes.Buffer
							err = decoder.Decode(ctx, readers, &restored)
							if err == nil {
								t.Fatalf("accepted inconsistent chunk sizes and restored %d bytes", restored.Len())
							}
							otherCollection := tc.order[0]
							if badIndex == 0 {
								otherCollection = tc.order[1]
							}
							for _, detail := range []string{
								fmt.Sprintf("chunk %d size mismatch", badChunk+1), badCollection, otherCollection,
								fmt.Sprintf("declares %d bytes", expectedSize), fmt.Sprintf("declares %d bytes", expectedSize+delta),
							} {
								if !strings.Contains(err.Error(), detail) {
									t.Errorf("error %q does not include %q", err, detail)
								}
							}
							if !bytes.Equal(restored.Bytes(), want[:badChunk*chunkBytes]) {
								t.Fatalf("wrote %d bytes; want only the %d-byte valid prefix", restored.Len(), badChunk*chunkBytes)
							}
						})
					}
				}
			}
		})
	}
}

func TestDecodeRejectsChunkSizeMismatchBeforePayload(t *testing.T) {
	ctx := context.Background()
	decoder, err := NewPadForDecode(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	readers := []io.Reader{
		bytes.NewReader(chunkWithClaimedSize("2A2", "1", []byte{0})),
		bytes.NewReader(chunkWithClaimedSize("2B2", "16777216", nil)),
	}
	var restored bytes.Buffer
	err = decoder.Decode(ctx, readers, &restored)
	if err == nil || !strings.Contains(err.Error(), "chunk 1 size mismatch") {
		t.Fatalf("got %v, want size mismatch before attempting to read the absent payload", err)
	}
	if restored.Len() != 0 {
		t.Fatal("inconsistent chunk produced output")
	}
}
