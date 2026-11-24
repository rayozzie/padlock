// Copyright 2026 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"testing"
	"testing/cryptotest"
)

func TestFullPadStreamValueAcrossReseed(t *testing.T) {
	ctx := context.Background()
	stream := make([]byte, 300)
	for i := range stream {
		stream[i] = byte(i*7 + 3)
	}
	sizes := []int{1, 99, 50, 150}
	for _, seeded := range []bool{false, true} {
		read := func(withStream bool) []byte {
			t.Helper()
			cryptotest.SetGlobalRandom(t, 11) // identical OS draws for both mixers
			var seeds []RNG
			if seeded {
				seed := bytes.Repeat([]byte{0x5a}, InitialEntropyBytes+len(sizes)*44)
				seeds = []RNG{&recordedEntropy{name: "seed", reader: bytes.NewReader(seed)}}
			}
			var rng RNG
			var err error
			switch {
			case withStream:
				rng, err = NewRandWithPadStream(ctx, &recordedEntropy{name: "stream", reader: bytes.NewReader(stream)}, seeds...)
			case seeded:
				rng, err = NewRandWithEntropy(ctx, seeds...)
			default:
				rng = NewDefaultRand(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			out := make([]byte, 0, len(stream))
			for _, n := range sizes {
				// Leave half of the request in the current ChaCha20 stream, so the
				// reseed falls at the start of the 1-byte read and inside the others.
				for _, s := range rng.(*MultiRNG).Sources {
					if c, ok := s.(*ChaCha20Rand); ok {
						c.remaining = uint64(n / 2)
					}
				}
				p := make([]byte, n)
				if err := rng.Read(ctx, p); err != nil {
					t.Fatal(err)
				}
				out = append(out, p...)
			}
			return out
		}
		local, mixed := read(false), read(true)
		if bytes.Equal(local, mixed) {
			t.Fatal("stream did not change the output")
		}
		for i, b := range stream {
			if mixed[i] != local[i]^b {
				t.Fatalf("seeded=%v: byte %d is not stream XOR normal mixer", seeded, i)
			}
		}
	}
}
