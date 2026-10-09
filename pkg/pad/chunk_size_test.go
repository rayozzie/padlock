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

func TestEncodeChunkSizeBounds(t *testing.T) {
	ctx := context.Background()
	want := []byte{0, 1, 127, 128, 255, 0x55, 0xaa}
	for _, tc := range []struct {
		total, required, minimum int
	}{
		{2, 2, 1}, {3, 2, 2}, {5, 3, 6}, {3, 3, 1},
	} {
		t.Run(fmt.Sprintf("%d_of_%d", tc.required, tc.total), func(t *testing.T) {
			seen := make(map[int]bool)
			for _, size := range []int{-tc.minimum - 1, -1, 0, tc.minimum - 1, tc.minimum, tc.minimum + 1} {
				if seen[size] {
					continue
				}
				seen[size] = true
				t.Run(fmt.Sprintf("chunk=%d", size), func(t *testing.T) {
					encoder, err := NewPadForEncode(ctx, tc.total, tc.required)
					if err != nil {
						t.Fatal(err)
					}
					input := bytes.NewReader(want)
					collections := make(map[string]*bytes.Buffer)
					err = encoder.Encode(ctx, size, input, NewTestRNG(0),
						func(name string, _ int, _ string) (io.WriteCloser, error) {
							if collections[name] == nil {
								collections[name] = new(bytes.Buffer)
							}
							return &nopCloser{collections[name]}, nil
						}, "bin")
					if size < tc.minimum {
						if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("chunk size must be at least %d", tc.minimum)) {
							t.Fatalf("got %v, want a chunk size error with minimum %d", err, tc.minimum)
						}
						if input.Len() != len(want) || len(collections) != 0 {
							t.Fatal("invalid chunk size consumed input or created output")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					readers := make([]io.Reader, tc.required)
					for i := range readers {
						name := encoder.Collections[tc.total-1-i]
						readers[i] = bytes.NewReader(collections[name].Bytes())
					}
					decoder, err := NewPadForDecode(ctx, len(readers))
					if err != nil {
						t.Fatal(err)
					}
					var restored bytes.Buffer
					if err := decoder.Decode(ctx, readers, &restored); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(restored.Bytes(), want) {
						t.Fatal("valid chunk size did not restore the exact input")
					}
				})
			}
		})
	}
}

func TestEncodeRejectsInvalidPermutationCount(t *testing.T) {
	for _, count := range []int{0, -1} {
		p := &Pad{PermutationCount: count}
		if err := p.Encode(context.Background(), 1024, nil, nil, nil, "bin"); err == nil {
			t.Fatalf("accepted invalid permutation count %d", count)
		}
	}
}

func TestValidateChunkSize(t *testing.T) {
	for _, tc := range []struct {
		total, required, minimum int
	}{
		{2, 2, 1}, {3, 2, 2}, {5, 3, 6}, {8, 5, 35},
		{25, 12, 2496144}, {26, 13, 5200300}, {26, 14, 5200300},
		{26, 2, 25}, {26, 25, 25}, {26, 26, 1},
	} {
		t.Run(fmt.Sprintf("%d_of_%d", tc.required, tc.total), func(t *testing.T) {
			err := ValidateChunkSize(tc.total, tc.required, tc.minimum-1)
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at least %d", tc.minimum)) {
				t.Fatalf("got %v, want minimum chunk size %d", err, tc.minimum)
			}
			for _, size := range []int{tc.minimum, tc.minimum + 1} {
				if err := ValidateChunkSize(tc.total, tc.required, size); err != nil {
					t.Errorf("valid chunk size %d rejected: %v", size, err)
				}
			}
		})
	}
	for _, tc := range []struct{ total, required int }{
		{0, 0}, {1, 1}, {27, 2}, {3, 0}, {3, 1}, {3, 4},
	} {
		if err := ValidateChunkSize(tc.total, tc.required, 1024); err == nil {
			t.Errorf("accepted invalid threshold %d-of-%d", tc.required, tc.total)
		}
	}
}
