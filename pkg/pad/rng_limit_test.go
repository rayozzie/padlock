// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"
	"testing"

	"golang.org/x/crypto/chacha20"
)

func TestChaCha20RandContinuesAfterCounterLimit(t *testing.T) {
	rng := NewChaCha20Rand()
	// Advance directly to the final 64-byte block instead of generating 256 GiB.
	rng.stream.(*chacha20.Cipher).SetCounter(math.MaxUint32)
	rng.remaining = 64
	if err := rng.Read(context.Background(), make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	if err := rng.Read(context.Background(), make([]byte, 1)); err != nil {
		t.Fatalf("read after stream exhaustion failed: %v", err)
	}
}

func chaChaSeed(fill byte) []byte {
	return bytes.Repeat([]byte{fill}, chacha20.KeySize+chacha20.NonceSize)
}

func referenceChaChaBytes(t *testing.T, key, nonce []byte, counter uint32, size int) []byte {
	t.Helper()
	stream, err := chacha20.NewUnauthenticatedCipher(key, nonce)
	if err != nil {
		t.Fatal(err)
	}
	stream.SetCounter(counter)
	data := make([]byte, size)
	stream.XORKeyStream(data, data)
	return data
}

func advanceChaChaToFinalBlock(rng *ChaCha20Rand) {
	// Keep the provider's byte budget in sync with the advanced stream counter.
	rng.stream.(*chacha20.Cipher).SetCounter(math.MaxUint32)
	rng.remaining = 64
}

func TestChaCha20RandReseedBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reads []int
	}{
		{"empty", []int{0}},
		{"last_block", []int{64}},
		{"buffered_last_block", []int{1, 62, 1}},
		{"empty_at_boundary", []int{64, 0}},
		{"next_read", []int{64, 0, 1}},
		{"single_crossing_read", []int{65}},
		{"buffered_crossing_read", []int{1, 62, 2, 257}},
		{"continued_reads", []int{7, 56, 65, 17}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initialSeed := chaChaSeed(0x13)
			rng := newChaCha20Rand(bytes.NewReader(initialSeed))
			advanceChaChaToFinalBlock(rng)
			seed := chaChaSeed(0x37)
			seeds := bytes.NewReader(seed)
			rng.seedSource = seedReader{seeds}

			total := 0
			for _, size := range tc.reads {
				total += size
			}
			oldSize := total
			if oldSize > 64 {
				oldSize = 64
			}
			want := referenceChaChaBytes(t, initialSeed[:32], initialSeed[32:], math.MaxUint32, oldSize)
			if total > oldSize {
				want = append(want, referenceChaChaBytes(t, seed[:chacha20.KeySize], seed[chacha20.KeySize:], 0, total-oldSize)...)
			}
			var got []byte
			for _, size := range tc.reads {
				// Nonzero input verifies that Read replaces, rather than mixes in,
				// caller data on both sides of the reseed boundary.
				buf := bytes.Repeat([]byte{0xa5}, size)
				if err := rng.Read(context.Background(), buf); err != nil {
					t.Fatal(err)
				}
				got = append(got, buf...)
			}
			if !bytes.Equal(got, want) {
				t.Fatal("output differs from the old stream tail followed by the fresh stream")
			}
			wantUnused := len(seed)
			if total > 64 {
				wantUnused = 0
			}
			if seeds.Len() != wantUnused {
				t.Fatalf("unexpected reseeding: %d seed bytes unused, want %d", seeds.Len(), wantUnused)
			}
		})
	}
}

func TestChaCha20RandReseedsRepeatedly(t *testing.T) {
	currentSeed := chaChaSeed(0x13)
	rng := newChaCha20Rand(bytes.NewReader(currentSeed))
	seeds := [][]byte{chaChaSeed(0x37), chaChaSeed(0x59)}
	rng.seedSource = seedReader{io.MultiReader(bytes.NewReader(seeds[0]), bytes.NewReader(seeds[1]))}
	for _, seed := range seeds {
		advanceChaChaToFinalBlock(rng)
		want := referenceChaChaBytes(t, currentSeed[:32], currentSeed[32:], math.MaxUint32, 64)
		want = append(want, referenceChaChaBytes(t, seed[:chacha20.KeySize], seed[chacha20.KeySize:], 0, 65)...)
		got := make([]byte, len(want))
		if err := rng.Read(context.Background(), got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatal("a later reseed did not continue with the next fresh seed")
		}
		currentSeed = seed
	}
}

type chaChaSeedFailure struct{ err error }

func (r chaChaSeedFailure) Read([]byte) (int, error) { return 0, r.err }

func TestChaCha20RandReseedFailure(t *testing.T) {
	failure := errors.New("seed source unavailable")
	for _, tc := range []struct {
		name        string
		bytesBefore int
		sourceError error
		wantError   error
	}{
		{"key", 0, failure, failure},
		{"partial_key", 7, failure, failure},
		{"nonce", chacha20.KeySize, failure, failure},
		{"partial_nonce", chacha20.KeySize + 7, failure, failure},
		{"short_seed", chacha20.KeySize + 7, io.EOF, io.ErrUnexpectedEOF},
	} {
		for _, crossingRead := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/crossing=%t", tc.name, crossingRead), func(t *testing.T) {
				rng := NewChaCha20Rand()
				advanceChaChaToFinalBlock(rng)
				seed := chaChaSeed(0x59)
				rng.seedSource = seedReader{io.MultiReader(bytes.NewReader(seed[:tc.bytesBefore]), chaChaSeedFailure{tc.sourceError})}
				oldStream := rng.stream
				size := 65
				if !crossingRead {
					if err := rng.Read(context.Background(), make([]byte, 64)); err != nil {
						t.Fatal(err)
					}
					size = 1
				}
				if err := rng.Read(context.Background(), make([]byte, size)); !errors.Is(err, tc.wantError) {
					t.Fatalf("Read returned %v, want seed failure %v", err, tc.wantError)
				}
				if rng.stream != oldStream || rng.remaining != 0 {
					t.Fatal("failed reseeding changed the exhausted stream's state")
				}
				// A retry must obtain a complete fresh seed, never restart the old stream.
				rng.seedSource = seedReader{bytes.NewReader(seed)}
				got := make([]byte, 97)
				if err := rng.Read(context.Background(), got); err != nil {
					t.Fatal(err)
				}
				want := referenceChaChaBytes(t, seed[:chacha20.KeySize], seed[chacha20.KeySize:], 0, len(got))
				if !bytes.Equal(got, want) {
					t.Fatal("retry did not use the fresh stream")
				}
			})
		}
	}
}

func TestChaCha20RandConcurrentReseed(t *testing.T) {
	const workers, size = 12, 16
	initialSeed := chaChaSeed(0x13)
	rng := newChaCha20Rand(bytes.NewReader(initialSeed))
	advanceChaChaToFinalBlock(rng)
	seed := chaChaSeed(0x37)
	rng.seedSource = seedReader{bytes.NewReader(seed)}
	wantBytes := referenceChaChaBytes(t, initialSeed[:32], initialSeed[32:], math.MaxUint32, 64)
	wantBytes = append(wantBytes, referenceChaChaBytes(t, seed[:chacha20.KeySize], seed[chacha20.KeySize:], 0, workers*size-64)...)
	want := make([]string, workers)
	for i := range want {
		want[i] = string(wantBytes[i*size : (i+1)*size])
	}

	var wg sync.WaitGroup
	got := make([]string, workers)
	errs := make([]error, workers)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			buf := make([]byte, size)
			errs[i] = rng.Read(context.Background(), buf)
			got[i] = string(buf)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Scheduling may reorder calls, but no segment may be repeated or omitted.
	sort.Strings(got)
	sort.Strings(want)
	for i := range want {
		if got[i] != want[i] {
			t.Fatal("concurrent reads lost or repeated stream data across reseeding")
		}
	}
}

func TestPadEncodeAcrossChaChaReseed(t *testing.T) {
	ctx := context.Background()
	failure := errors.New("seed source unavailable")
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("seed_failure=%t", fail), func(t *testing.T) {
			rng := NewDefaultRand(ctx).(*MultiRNG)
			found := false
			for _, source := range rng.Sources {
				if source, ok := source.(*ChaCha20Rand); ok {
					found = true
					advanceChaChaToFinalBlock(source)
					if fail {
						source.seedSource = seedReader{chaChaSeedFailure{failure}}
					}
				}
			}
			if !found {
				t.Fatal("default RNG has no ChaCha20 source to exercise")
			}
			encoder, err := NewPadForEncode(ctx, 5, 3)
			if err != nil {
				t.Fatal(err)
			}
			data := bytes.Repeat([]byte("data spanning a random-source reseed\x00\xff"), 100)
			collections := make(map[string]*bytes.Buffer)
			newChunk := func(name string, _ int, _ string) (io.WriteCloser, error) {
				if collections[name] == nil {
					collections[name] = new(bytes.Buffer)
				}
				return &nopCloser{collections[name]}, nil
			}
			err = encoder.Encode(ctx, 4096, bytes.NewReader(data), rng, newChunk, "bin")
			if fail {
				if !errors.Is(err, failure) {
					t.Fatalf("encode returned %v, want original seed failure", err)
				}
				if len(collections) != 0 {
					t.Fatal("encoder wrote chunks despite the first chunk's reseed failure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			decoder, err := NewPadForDecode(ctx, 3)
			if err != nil {
				t.Fatal(err)
			}
			readers := []io.Reader{collections["3A5"], collections["3C5"], collections["3E5"]}
			var restored bytes.Buffer
			if err := decoder.Decode(ctx, readers, &restored); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(restored.Bytes(), data) {
				t.Fatal("data changed when encoding crossed the reseed boundary")
			}
		})
	}
}
