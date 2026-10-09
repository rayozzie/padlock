// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestDecodeRejectsMissingTrailingChunks(t *testing.T) {
	ctx := context.Background()
	const chunkBytes = 128
	want := make([]byte, 3*chunkBytes+17)
	for i := range want {
		want[i] = byte(i*37 + i/7)
	}
	for _, tc := range []struct {
		name            string
		total, required int
		order           []string
	}{
		{"2_of_2", 2, 2, []string{"2B2", "2A2"}},
		{"3_of_5", 5, 3, []string{"3A5", "3E5", "3C5"}},
		{"3_of_5_with_extras", 5, 3, []string{"3E5", "3A5", "3D5", "3B5", "3C5"}},
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

			// Remove either all but the first chunk or only the final chunk.
			// Every nonempty proper subset of inputs ends early, exercising each
			// position and cases where multiple collections are incomplete.
			for _, keep := range []int{1, 3} {
				for mask := 1; mask < (1<<len(tc.order))-1; mask++ {
					t.Run(fmt.Sprintf("keep=%d/missing=%0*b", keep, len(tc.order), mask), func(t *testing.T) {
						readers := make([]io.Reader, len(tc.order))
						var incomplete []string
						for i, name := range tc.order {
							selected := chunks[name]
							if mask&(1<<i) != 0 {
								selected = selected[:keep]
								incomplete = append(incomplete, name)
							}
							var stream bytes.Buffer
							for _, chunk := range selected {
								stream.Write(chunk.Bytes())
							}
							readers[i] = bytes.NewReader(stream.Bytes())
						}
						decoder, err := NewPadForDecode(ctx, len(readers))
						if err != nil {
							t.Fatal(err)
						}
						var restored bytes.Buffer
						err = decoder.Decode(ctx, readers, &restored)
						if !errors.Is(err, io.ErrUnexpectedEOF) {
							t.Fatalf("got %v after restoring %d of %d bytes, want an incomplete-collection error", err, restored.Len(), len(want))
						}
						for _, name := range incomplete {
							if !strings.Contains(err.Error(), name) {
								t.Errorf("error does not identify incomplete collection %s: %v", name, err)
							}
						}
						if !strings.Contains(err.Error(), fmt.Sprintf("missing chunk %d", keep+1)) {
							t.Errorf("error does not identify the missing chunk: %v", err)
						}
						if !bytes.Equal(restored.Bytes(), want[:keep*chunkBytes]) {
							t.Fatal("decoder wrote incorrect data or continued past the incomplete chunk")
						}
					})
				}
			}
		})
	}
}
