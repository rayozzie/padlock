// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

// Test instrumentation deliberately keeps the supplied buffer observable after
// Read returns. This checks cleanup of these buffers, not erasure of all runtime
// copies or the generator's active internal state.
type observedSeedReader struct {
	seed, observed []byte
	failure        error
}

func (r *observedSeedReader) Read(p []byte) (int, error) {
	r.observed = p
	return copy(p, r.seed), r.failure
}

func TestSeedScratchIsCleared(t *testing.T) {
	for _, tc := range []struct {
		name string
		size int
		new  func(io.Reader) RNG
	}{
		{"math", 8, func(r io.Reader) RNG { return newMathRand(r) }},
		{"chacha20", 44, func(r io.Reader) RNG { return newChaCha20Rand(r) }},
		{"pcg64", 16, func(r io.Reader) RNG { return newPCG64Rand(r) }},
		{"mt19937", 8, func(r io.Reader) RNG { return newMT19937Rand(r) }},
	} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%t", tc.name, fail), func(t *testing.T) {
				seed := bytes.Repeat([]byte{0x93}, tc.size)
				source := &observedSeedReader{seed: seed}
				if fail {
					source.seed = seed[:3]
					source.failure = errors.New("seed read failed")
					func() {
						defer func() {
							value := recover()
							if err, ok := value.(error); !ok || !errors.Is(err, source.failure) {
								t.Fatalf("expected original seed failure, got %v", value)
							}
						}()
						tc.new(source)
					}()
				} else {
					rng := tc.new(source)
					want := referenceSeededBytes(t, tc.name, seed)
					got := make([]byte, len(want))
					if err := rng.Read(context.Background(), got); err != nil || !bytes.Equal(got, want) {
						t.Fatalf("cleanup changed generator output: %v", err)
					}
				}
				if len(source.observed) != tc.size || !bytes.Equal(source.observed, make([]byte, tc.size)) {
					t.Fatal("constructor retained seed bytes in temporary buffer")
				}
				if !bytes.Equal(seed, bytes.Repeat([]byte{0x93}, tc.size)) {
					t.Fatal("constructor changed caller-owned seed input")
				}
			})
		}
	}
}
