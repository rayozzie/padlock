// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
)

type recordedEntropy struct {
	name   string
	reader io.Reader
	reads  []int
	leaked bool
}

func (s *recordedEntropy) Name() string { return s.name }
func (s *recordedEntropy) Read(ctx context.Context, p []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.reads = append(s.reads, len(p))
	for _, b := range p {
		if b != 0 {
			s.leaked = true // No other source's contribution may reach this one.
		}
	}
	_, err := io.ReadFull(s.reader, p)
	return err
}

func TestExternalEntropySeedsEveryGeneratorAndReseed(t *testing.T) {
	ctx := context.Background()
	const seedCount = InitialEntropyBytes + 2*44
	wantSeeds := make([]byte, seedCount)
	inputs := make([]*recordedEntropy, 3)
	for i := range inputs {
		data := make([]byte, seedCount)
		for j := range data {
			data[j] = byte((i+1)*(j+17) + i*7)
			wantSeeds[j] ^= data[j]
		}
		inputs[i] = &recordedEntropy{name: fmt.Sprintf("source%d", i), reader: bytes.NewReader(data)}
	}
	rng, err := newRandWithEntropy(ctx, inputs[0], inputs[1], inputs[2])
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rng.Sources[0].(*CryptoRand); !ok {
		t.Fatal("external seeding removed the direct OS output provider")
	}
	offset := 0
	var chacha *ChaCha20Rand
	for i, size := range []int{8, 44, 16, 8} {
		provider := rng.Sources[i+1]
		want := referenceSeededBytes(t, provider.Name(), wantSeeds[offset:offset+size])
		got := make([]byte, len(want))
		if err := provider.Read(ctx, got); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s did not mix every contribution into its own seed: %v", provider.Name(), err)
		}
		if c, ok := provider.(*ChaCha20Rand); ok {
			chacha = c
		}
		offset += size
	}
	for range 2 {
		chacha.remaining = 0 // Exercise the boundary without generating 256 GiB.
		want := referenceSeededBytes(t, "chacha20", wantSeeds[offset:offset+44])
		got := make([]byte, len(want))
		if err := chacha.Read(ctx, got); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("reseed omitted a source or reused seed bytes: %v", err)
		}
		offset += 44
	}
	for _, source := range inputs {
		if source.leaked || !reflect.DeepEqual(source.reads, []int{InitialEntropyBytes, 44, 44}) {
			t.Fatalf("%s: reads=%v leaked other contribution=%t", source.name, source.reads, source.leaked)
		}
	}
}

func TestExternalEntropyInitializationFailures(t *testing.T) {
	failure := errors.New("external entropy unavailable")
	for _, size := range []int{0, 1, 7, 8, 32, 44, InitialEntropyBytes - 1} {
		for failed := 0; failed < 2; failed++ {
			t.Run(fmt.Sprintf("size=%d/source=%d", size, failed), func(t *testing.T) {
				var sources []RNG
				for i := 0; i < 2; i++ {
					var r io.Reader = bytes.NewReader(make([]byte, InitialEntropyBytes))
					if i == failed {
						r = io.MultiReader(bytes.NewReader(make([]byte, size)), chaChaSeedFailure{failure})
					}
					sources = append(sources, &recordedEntropy{name: fmt.Sprint(i), reader: r})
				}
				rng, err := NewRandWithEntropy(context.Background(), sources...)
				if rng != nil || !errors.Is(err, failure) {
					t.Fatalf("got RNG %v, error %v; want no generator and original failure", rng, err)
				}
			})
		}
	}
	if rng, err := NewRandWithEntropy(context.Background(), nil); rng != nil || err == nil {
		t.Fatal("nil entropy source accepted")
	}
}

func TestExternalEntropyReseedFailurePreventsEncode(t *testing.T) {
	ctx := context.Background()
	source := &recordedEntropy{name: "finite source", reader: bytes.NewReader(make([]byte, InitialEntropyBytes))}
	rng, err := NewRandWithEntropy(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range rng.(*MultiRNG).Sources {
		if c, ok := provider.(*ChaCha20Rand); ok {
			c.remaining = 0
		}
	}
	want := bytes.Repeat([]byte{0xa5}, 128)
	got := bytes.Clone(want)
	if err := rng.Read(ctx, got); !errors.Is(err, io.EOF) || !bytes.Equal(got, want) {
		t.Fatalf("exhausted external source published output or was ignored: %v", err)
	}
	encoder, err := NewPadForEncode(ctx, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	err = encoder.Encode(ctx, 128, bytes.NewReader([]byte("secret data")), rng, func(string, int, string) (io.WriteCloser, error) {
		t.Fatal("a failed entropy source allowed collection output")
		return nil, nil
	}, "bin")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("encode returned %v, want entropy exhaustion", err)
	}
}

func TestExternalEntropyCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &recordedEntropy{name: "source", reader: bytes.NewReader(make([]byte, InitialEntropyBytes))}
	if rng, err := NewRandWithEntropy(ctx, source); rng != nil || !errors.Is(err, context.Canceled) || len(source.reads) != 0 {
		t.Fatalf("canceled initialization consumed entropy: %v", err)
	}
	rng, err := NewRandWithEntropy(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Repeat([]byte{0x5a}, 16)
	got := bytes.Clone(want)
	if err := rng.Read(ctx, got); !errors.Is(err, context.Canceled) || !bytes.Equal(got, want) || len(source.reads) != 1 {
		t.Fatalf("canceled read consumed entropy or returned output: %v", err)
	}
}
