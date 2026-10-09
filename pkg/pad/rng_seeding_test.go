// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	mrand "math/rand"
	rand2 "math/rand/v2"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/seehuhn/mt19937"
)

// Reference streams verify which seed bytes actually reach each algorithm.
// These are deterministic behavior checks, not tests of entropy independence.
func referenceSeededBytes(t *testing.T, name string, seed []byte) []byte {
	t.Helper()
	const size = 128
	var nextByte func() byte
	switch name {
	case "math":
		r := mrand.New(mrand.NewSource(int64(binary.BigEndian.Uint64(seed))))
		nextByte = func() byte { return byte(r.Intn(256)) }
	case "chacha20":
		return referenceChaChaBytes(t, seed[:32], seed[32:], 0, size)
	case "pcg64":
		r := rand2.New(rand2.NewPCG(binary.LittleEndian.Uint64(seed[:8]), binary.LittleEndian.Uint64(seed[8:])))
		nextByte = func() byte { return byte(r.IntN(256)) }
	case "mt19937":
		mt := mt19937.New()
		mt.Seed(int64(binary.LittleEndian.Uint64(seed)))
		r := mrand.New(mt)
		nextByte = func() byte { return byte(r.Intn(256)) }
	default:
		t.Fatalf("unknown generator %q", name)
	}
	result := make([]byte, size)
	for i := range result {
		result[i] = nextByte()
	}
	return result
}

func TestDefaultRNGConsumesSeparateSeeds(t *testing.T) {
	const seedBytes = 8 + 32 + 12 + 16 + 8
	// Distinct bytes reveal reused or overlapping seed ranges. Exercise two
	// successive initializations to ensure neither restarts the seed stream.
	input := make([]byte, 2*seedBytes+1)
	for i := range input {
		input[i] = byte(i + 1)
	}
	for _, fragmented := range []bool{false, true} {
		t.Run(fmt.Sprintf("fragmented=%t", fragmented), func(t *testing.T) {
			reader := bytes.NewReader(input)
			var source io.Reader = reader
			if fragmented {
				source = iotest.OneByteReader(source)
			}
			for instance := 0; instance < 2; instance++ {
				rng := newDefaultRand(context.Background(), source)
				if len(rng.Sources) != 5 {
					t.Fatalf("got %d generators, want 5", len(rng.Sources))
				}
				if _, ok := rng.Sources[0].(*CryptoRand); !ok {
					t.Fatal("default mixture lost its direct OS RNG provider")
				}
				offset := instance * seedBytes
				for i, tc := range []struct {
					name string
					size int
				}{{"math", 8}, {"chacha20", 44}, {"pcg64", 16}, {"mt19937", 8}} {
					provider := rng.Sources[i+1]
					if provider.Name() != tc.name {
						t.Fatalf("generator %d is %q, want %q", i+1, provider.Name(), tc.name)
					}
					want := referenceSeededBytes(t, tc.name, input[offset:offset+tc.size])
					got := bytes.Repeat([]byte{0xa5}, len(want))
					if err := provider.Read(context.Background(), got); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got, want) {
						t.Fatalf("instance %d %s did not use its own complete seed range", instance, tc.name)
					}
					offset += tc.size
				}
			}
			if reader.Len() != 1 {
				t.Fatalf("seed reader has %d bytes left, want 1", reader.Len())
			}
		})
	}
}

func requireSeedPanic(t *testing.T, want error, provider string, create func()) {
	t.Helper()
	defer func() {
		value := recover()
		err, ok := value.(error)
		if !ok || !errors.Is(err, want) || !strings.Contains(err.Error(), provider) {
			t.Fatalf("initialization panic = %v, want %s seed failure wrapping %v", value, provider, want)
		}
	}()
	create()
	t.Fatal("initialization returned a generator after a seed failure")
}

func TestDefaultRNGRejectsIncompleteSeeds(t *testing.T) {
	failure := errors.New("seed source unavailable")
	seed := bytes.Repeat([]byte{0x91}, 76)
	offset := 0
	// Include the ChaCha20 key/nonce boundary and both PCG64 words.
	for _, part := range []struct {
		name string
		size int
	}{{"math/rand", 8}, {"ChaCha20", 44}, {"PCG64", 16}, {"MT19937", 8}} {
		for n := 0; n < part.size; n++ {
			for _, cause := range []error{io.EOF, failure} {
				t.Run(fmt.Sprintf("%s/available=%d/cause=%v", part.name, offset+n, cause), func(t *testing.T) {
					source := io.MultiReader(bytes.NewReader(seed[:offset+n]), iotest.ErrReader(cause))
					want := cause
					if cause == io.EOF && n != 0 {
						want = io.ErrUnexpectedEOF
					}
					requireSeedPanic(t, want, part.name, func() {
						newDefaultRand(context.Background(), source)
					})
				})
			}
		}
		offset += part.size
	}
}

func TestPCG64UsesBothRandomSeedWords(t *testing.T) {
	seed := make([]byte, 16)
	binary.LittleEndian.PutUint64(seed[:8], 0x0123456789abcdef)
	for _, second := range []uint64{0, 1, 0xfedcba9876543210} {
		binary.LittleEndian.PutUint64(seed[8:], second)
		for repeat := 0; repeat < 2; repeat++ {
			rng := newPCG64Rand(iotest.OneByteReader(bytes.NewReader(seed)))
			want := referenceSeededBytes(t, "pcg64", seed)
			got := make([]byte, len(want))
			if err := rng.Read(context.Background(), got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("PCG64 output used a timestamp or incorrect seed word; second word=%x", second)
			}
		}
	}
}

type failingMixSource struct {
	name  string
	err   error
	calls int
}

func (s *failingMixSource) Name() string { return s.name }

func (s *failingMixSource) Read(_ context.Context, p []byte) error {
	s.calls++
	// Even a failed source can write partial data before returning an error.
	for i := range p {
		p[i] = byte(i + 1)
	}
	return s.err
}

func TestMultiRNGDoesNotDropFailedSources(t *testing.T) {
	failure := errors.New("generator failed")
	for failed := 0; failed < 5; failed++ {
		t.Run(fmt.Sprintf("source=%d", failed), func(t *testing.T) {
			rng := &MultiRNG{}
			sources := make([]*failingMixSource, 5)
			for i := range sources {
				sources[i] = &failingMixSource{name: fmt.Sprintf("source-%d", i)}
				if i == failed {
					sources[i].err = failure
				}
				rng.Sources = append(rng.Sources, sources[i])
			}
			want := bytes.Repeat([]byte{0xa5}, 128)
			got := bytes.Clone(want)
			err := rng.Read(context.Background(), got)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), sources[failed].name) {
				t.Fatalf("Read returned %v, want named source failure", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatal("failed mix published partial output")
			}
			for i, source := range sources {
				wantCalls := 1
				if i > failed {
					wantCalls = 0
				}
				if source.calls != wantCalls {
					t.Fatalf("source %d called %d times, want %d", i, source.calls, wantCalls)
				}
			}
		})
	}
}
