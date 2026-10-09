// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestDecodeCollectionOrder(t *testing.T) {
	ctx := context.Background()
	const chunkSize = 113
	// Exercise several full chunks and a shorter final chunk.
	want := make([]byte, 3*chunkSize+17)
	for i := range want {
		want[i] = byte(i*37 + i/7)
	}
	for total := 2; total <= 5; total++ {
		for required := 2; required <= total; required++ {
			t.Run(fmt.Sprintf("%d_of_%d", required, total), func(t *testing.T) {
				encoder, err := NewPadForEncode(ctx, total, required)
				if err != nil {
					t.Fatal(err)
				}
				collections := make(map[string]*bytes.Buffer, total)
				for _, name := range encoder.Collections {
					collections[name] = new(bytes.Buffer)
				}
				err = encoder.Encode(ctx, chunkSize*encoder.PermutationCount, bytes.NewReader(want), NewTestRNG(0x37),
					func(name string, _ int, _ string) (io.WriteCloser, error) {
						return &nopCloser{collections[name]}, nil
					}, "bin")
				if err != nil {
					t.Fatal(err)
				}

				// Visit every ordered subset with K through N distinct collections.
				// This also covers extra collections preceding those selected for decode.
				var visit func([]string)
				used := make(map[string]bool, total)
				visit = func(order []string) {
					if len(order) >= required {
						t.Run(strings.Join(order, ","), func(t *testing.T) {
							readers := make([]io.Reader, len(order))
							for i, name := range order {
								readers[i] = bytes.NewReader(collections[name].Bytes())
							}
							decoder, err := NewPadForDecode(ctx, len(readers))
							if err != nil {
								t.Fatal(err)
							}
							var got bytes.Buffer
							if err := decoder.Decode(ctx, readers, &got); err != nil {
								t.Fatal(err)
							}
							if !bytes.Equal(got.Bytes(), want) {
								t.Fatalf("restored %d bytes, want %d identical bytes", got.Len(), len(want))
							}
						})
					}
					for _, name := range encoder.Collections {
						if !used[name] {
							used[name] = true
							visit(append(order, name))
							used[name] = false
						}
					}
				}
				visit(nil)
			})
		}
	}
}
