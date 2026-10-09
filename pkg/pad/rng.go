// Copyright 2025 Ray Ozzie. All rights reserved.

// This file contains the core random number generation functionality
// for the padlock system.

package pad

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/rayozzie/padlock/pkg/trace"
)

// RNG fills byte buffers for pad generation. Implementing this interface does
// not establish a generator's suitability for protecting data: some providers
// are statistical PRNGs or deterministic test generators.
//
// Callers must discard the requested output on error. A nil error means that
// the buffer was filled, not that its unpredictability has been verified.
type RNG interface {
	// Name returns the name of the RNG implementation.
	Name() string
	// Read fills p completely or returns an error. Providers may use ctx for logging.
	Read(ctx context.Context, p []byte) (err error)
}

// MultiRNG XORs the output of its configured generators. It serializes reads
// and returns an error if any generator fails, leaving the caller's buffer
// unchanged. Sources must not be modified while reads are in progress.
//
// If one input is uniform and independent of all other inputs and the attacker's
// knowledge, the XOR result remains secret and uniform. XOR does not create
// independence or entropy: correlated inputs can cancel, as X XOR X equals zero.
// Distinct algorithms and separate seeds alone do not prove this independence.
// See NewDefaultRand for the default generators' shared dependency.
type MultiRNG struct {
	// Sources is a slice of RNG implementations to combine
	Sources []RNG
	// lock protects against concurrent access
	lock sync.Mutex
}

// Name
func (m *MultiRNG) Name() string {
	return "multi"
}

// Read implements the RNG interface by combining multiple random sources.
// It XORs the output of all sources to produce the final random bytes.
func (m *MultiRNG) Read(ctx context.Context, p []byte) error {
	log := trace.FromContext(ctx).WithPrefix("MULTI-RNG")

	m.lock.Lock()
	defer m.lock.Unlock()

	// Initialize accumulator
	acc := make([]byte, len(p))
	defer clear(acc)
	tmp := make([]byte, len(p))
	defer clear(tmp)

	// Read from each source and XOR outputs
	sourceNames := []string{}
	for _, s := range m.Sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Each source sees a fresh buffer, never another source's contribution.
		clear(tmp)

		// Determine source type for better logging
		sourceType := s.Name()
		sourceNames = append(sourceNames, sourceType)

		// If any source fails, log and propagate the error
		err := s.Read(ctx, tmp)
		if err != nil {
			log.Error(fmt.Errorf("%s random source failed: %w", sourceType, err))
			return fmt.Errorf("%s random source failed: %w", sourceType, err)
		}

		// XOR this source's output into the accumulator
		for j := 0; j < len(p); j++ {
			acc[j] ^= tmp[j]
		}
	}

	// Ensure we had at least one successful source
	if len(sourceNames) == 0 {
		return fmt.Errorf("no random sources were able to provide entropy")
	}

	// Copy final result to output buffer
	if err := ctx.Err(); err != nil {
		return err
	}
	copy(p, acc)
	log.Debugf("rng: mixed %d bytes from generators %s", len(p), strings.Join(sourceNames, "+"))
	return nil
}

// NewDefaultRand combines crypto/rand, math/rand, ChaCha20, PCG64, and MT19937.
// Each seeded generator consumes separate seed bytes from crypto/rand.Reader;
// CryptoRand also reads crypto/rand for every output buffer. Thus all five
// generators depend on the same OS randomness source. Algorithm diversity does
// not provide independent entropy sources or protection against its compromise.
//
// MathRand, PCG64Rand, and MT19937Rand are statistical PRNGs, not security
// fallbacks. The default relies on the OS RNG and generator implementations;
// it does not establish information-theoretic or unconditional quantum security.
// Initialization panics if a complete seed cannot be read. Read errors must be
// handled by the caller; no failed generator is silently dropped.
func NewDefaultRand(ctx context.Context) RNG {
	rng := newDefaultRand(ctx, crand.Reader)
	trace.FromContext(ctx).WithPrefix("RNG").Tracef("Default RNG: five generators sharing the OS randomness source")
	return rng
}

// InitialEntropyBytes is the number of bytes consumed from EACH additional
// source to initialize the four seeded generators. Later ChaCha20 reseeds
// consume 44 more bytes from each source.
const InitialEntropyBytes = 8 + 32 + 12 + 16 + 8

// NewRandWithEntropy supplements the OS randomness source with caller-supplied
// sources. It XORs fresh bytes from every source into the seed material, then
// assigns disjoint seed ranges to math/rand, ChaCha20, PCG64, and MT19937.
// CryptoRand continues to use the OS RNG directly on every output read.
//
// Each source must fill buffers completely or return an error, honor context
// cancellation where possible, and use fresh bytes for each request. The caller
// owns source lifetimes and must keep them available for later ChaCha20 reseeds.
// No source is skipped on error. Source independence and secrecy must be assessed
// by the caller; the number of configured sources does not establish either.
func NewRandWithEntropy(ctx context.Context, sources ...RNG) (RNG, error) {
	rng, err := newRandWithEntropy(ctx, NewCryptoRand(), sources...)
	if err != nil {
		return nil, err
	}
	return rng, nil
}

func newRandWithEntropy(ctx context.Context, osSource RNG, sources ...RNG) (*MultiRNG, error) {
	for i, source := range sources {
		if source == nil {
			return nil, fmt.Errorf("external entropy source %d is nil", i+1)
		}
	}
	seedMixer := &MultiRNG{Sources: append([]RNG{osSource}, sources...)}
	seed := make([]byte, InitialEntropyBytes)
	defer clear(seed)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := seedMixer.Read(ctx, seed); err != nil {
		return nil, fmt.Errorf("initialize generator seeds: %w", err)
	}
	// The complete seed buffer was obtained before constructing any generators.
	// All constructors below consume only their fixed-size portion of it.
	rng := newDefaultRand(ctx, bytes.NewReader(seed))
	for _, source := range rng.Sources {
		if c, ok := source.(*ChaCha20Rand); ok {
			c.seedSource = seedMixer
		}
	}
	trace.FromContext(ctx).WithPrefix("RNG").Infof("RNG seeds include OS randomness and %d additional source(s)", len(sources))
	return rng, nil
}

// seedReader adapts complete reads to the RNG contract without changing a
// process-wide random source. Cancellation during a blocking read is the
// underlying reader's responsibility.
type seedReader struct{ io.Reader }

func (r seedReader) Name() string { return "seed reader" }
func (r seedReader) Read(ctx context.Context, p []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := io.ReadFull(r.Reader, p)
	return err
}

// newDefaultRand accepts the seed reader explicitly so seed consumption and
// failures can be tested without replacing the process-wide crypto/rand.Reader.
func newDefaultRand(ctx context.Context, seedSource io.Reader) *MultiRNG {
	sources := []RNG{
		NewCryptoRand(),
		newMathRand(seedSource),
		newChaCha20Rand(seedSource),
		newPCG64Rand(seedSource),
		newMT19937Rand(seedSource),
	}

	return &MultiRNG{
		Sources: sources,
	}
}
