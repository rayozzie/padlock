// Copyright 2025 Ray Ozzie. All rights reserved.

// This file contains implementations of various random number generator providers
// used by the padlock system.

package pad

import (
	"context"
	"crypto/cipher"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	mrand "math/rand"
	rand2 "math/rand/v2"
	"sync"

	"github.com/rayozzie/padlock/pkg/trace"
	"github.com/seehuhn/mt19937"
	"golang.org/x/crypto/chacha20"
)

// CryptoRand obtains every output buffer from Go's crypto/rand package.
// Its OS randomness source is also used to seed the other default generators.
// A nil read error does not detect a compromised or predictable OS RNG.
type CryptoRand struct {
	// lock protects against concurrent access to the crypto RNG
	lock sync.Mutex
}

// NewCryptoRand creates a crypto/rand based RNG
func NewCryptoRand() *CryptoRand {
	return &CryptoRand{}
}

// Name
func (r *CryptoRand) Name() string {
	return "crypto"
}

// Read fills p using crypto/rand, with context support for logging.
func (r *CryptoRand) Read(ctx context.Context, p []byte) error {
	log := trace.FromContext(ctx).WithPrefix("CRYPTO-RNG")

	r.lock.Lock()
	defer r.lock.Unlock()

	n, err := crand.Read(p)
	if err != nil {
		log.Error(fmt.Errorf("crypto/rand read failed: %w", err))
		return fmt.Errorf("crypto/rand read failed: %w", err)
	}
	if n != len(p) {
		log.Error(fmt.Errorf("crypto/rand read returned %d bytes, expected %d", n, len(p)))
		return fmt.Errorf("crypto/rand read returned %d bytes, expected %d", n, len(p))
	}

	return nil
}

// MathRand wraps the legacy math/rand PRNG. Its eight-byte seed is reduced by
// math/rand.NewSource to fewer than 2^31 distinct initial states. A random seed
// does not make this generator suitable as a security fallback or sole pad source.
type MathRand struct {
	// src is the pseudorandom source
	src *mrand.Rand
	// lock protects against concurrent access to the math RNG
	lock sync.Mutex
}

// NewMathRand seeds math/rand from crypto/rand.Reader. It panics on seed failure.
func NewMathRand() *MathRand {
	return newMathRand(crand.Reader)
}

func newMathRand(seedSource io.Reader) *MathRand {
	seed := mustReadSeed(seedSource, 8, "math/rand")
	defer clear(seed)
	return &MathRand{
		src: mrand.New(mrand.NewSource(int64(binary.BigEndian.Uint64(seed)))),
	}
}

// mustReadSeed preserves the constructors' panic-on-failure API. Never seed
// with a partial read, a fixed fallback, or a timestamp after a source failure.
// Callers clear the temporary buffer after installing their state. This is
// best-effort cleanup, not guaranteed erasure of runtime or generator copies.
func mustReadSeed(source io.Reader, size int, name string) []byte {
	seed := make([]byte, size)
	if _, err := io.ReadFull(source, seed); err != nil {
		clear(seed)
		panic(fmt.Errorf("failed to generate %s seed: %w", name, err))
	}
	return seed
}

// Name
func (r *MathRand) Name() string {
	return "math"
}

// Read fills p from the MathRand state.
func (mr *MathRand) Read(ctx context.Context, p []byte) error {

	mr.lock.Lock()
	defer mr.lock.Unlock()

	for i := range p {
		p[i] = byte(mr.src.Intn(256))
	}

	return nil
}

// ChaCha20 has a 32-bit block counter and produces 64 bytes per block.
const chaCha20StreamBytes uint64 = 1 << 38

// ChaCha20Rand implements RNG using the ChaCha20 stream cipher. It reseeds
// before a read would exceed the current stream's counter limit. By default its
// key and nonce use the same OS source as the other generators. NewRandWithEntropy
// also mixes every selected external source into initial and replacement seeds.
type ChaCha20Rand struct {
	lock       sync.Mutex
	stream     cipher.Stream
	remaining  uint64 // bytes still available from the current stream
	seedSource RNG
}

// NewChaCha20Rand seeds ChaCha20 from crypto/rand.Reader. It panics on seed failure.
func NewChaCha20Rand() *ChaCha20Rand {
	return newChaCha20Rand(crand.Reader)
}

func newChaCha20Rand(seedSource io.Reader) *ChaCha20Rand {
	rng := &ChaCha20Rand{seedSource: seedReader{seedSource}}
	if err := rng.reseed(context.Background()); err != nil {
		panic(fmt.Errorf("failed to initialize ChaCha20 random source: %w", err))
	}
	return rng
}

// reseed installs a fresh stream only after initialization succeeds. Except
// during construction, the caller must hold c.lock.
func (c *ChaCha20Rand) reseed(ctx context.Context) error {
	seed := make([]byte, chacha20.KeySize+chacha20.NonceSize)
	defer clear(seed)
	if err := c.seedSource.Read(ctx, seed); err != nil {
		return fmt.Errorf("failed to generate ChaCha20 seed: %w", err)
	}
	key, nonce := seed[:chacha20.KeySize], seed[chacha20.KeySize:]

	stream, err := chacha20.NewUnauthenticatedCipher(key, nonce)
	if err != nil {
		return fmt.Errorf("failed to create ChaCha20 stream: %w", err)
	}

	// The cipher copies the key/nonce into its state; retain no extra seed copy.
	// Never restart the old stream or reuse its key/nonce after exhaustion.
	c.stream = stream
	c.remaining = chaCha20StreamBytes
	return nil
}

// Name
func (r *ChaCha20Rand) Name() string {
	return "chacha20"
}

// Read implements the RNG interface by generating random bytes using ChaCha20
func (c *ChaCha20Rand) Read(ctx context.Context, p []byte) error {

	c.lock.Lock()
	defer c.lock.Unlock()

	for len(p) > 0 {
		if c.remaining == 0 {
			if err := c.reseed(ctx); err != nil {
				return err
			}
		}

		// Split reads at the stream boundary, even when it falls within p.
		n := len(p)
		if uint64(n) > c.remaining {
			n = int(c.remaining) // remaining is smaller than an int-sized length
		}
		clear(p[:n])
		c.stream.XORKeyStream(p[:n], p[:n])
		c.remaining -= uint64(n)
		p = p[n:]
	}

	return nil
}

// PCG64Rand wraps the PCG64 algorithm from math/rand/v2. It is a statistical
// PRNG, not a security fallback or independent entropy source.
type PCG64Rand struct {
	lock sync.Mutex
	rng  *rand2.Rand
}

// NewPCG64Rand reads both PCG seed words from crypto/rand.Reader.
// It panics on seed failure.
func NewPCG64Rand() *PCG64Rand {
	return newPCG64Rand(crand.Reader)
}

func newPCG64Rand(seedSource io.Reader) *PCG64Rand {
	seed := mustReadSeed(seedSource, 16, "PCG64")
	defer clear(seed)
	rng := rand2.New(rand2.NewPCG(
		binary.LittleEndian.Uint64(seed[:8]),
		binary.LittleEndian.Uint64(seed[8:]),
	))

	return &PCG64Rand{
		rng: rng,
	}
}

// Name
func (r *PCG64Rand) Name() string {
	return "pcg64"
}

// Read implements the RNG interface by generating random bytes using PCG64
func (p *PCG64Rand) Read(ctx context.Context, b []byte) error {

	p.lock.Lock()
	defer p.lock.Unlock()

	for i := range b {
		b[i] = byte(p.rng.IntN(256))
	}

	return nil
}

// MT19937Rand wraps Mersenne Twister with a 64-bit seed. It is a statistical
// PRNG, not a security fallback or independent entropy source.
type MT19937Rand struct {
	lock    sync.Mutex
	rng     *mt19937.MT19937
	wrapper *mrand.Rand
}

// NewMT19937Rand seeds MT19937 from crypto/rand.Reader. It panics on seed failure.
func NewMT19937Rand() *MT19937Rand {
	return newMT19937Rand(crand.Reader)
}

func newMT19937Rand(seedSource io.Reader) *MT19937Rand {
	seed := mustReadSeed(seedSource, 8, "MT19937")
	defer clear(seed)
	// Create MT19937 instance
	mt := mt19937.New()

	// Seed the MT19937 instance
	mt.Seed(int64(binary.LittleEndian.Uint64(seed)))

	// Create a wrapper for easier usage
	wrapper := mrand.New(mt)

	return &MT19937Rand{
		rng:     mt,
		wrapper: wrapper,
	}
}

// Name
func (r *MT19937Rand) Name() string {
	return "mt19937"
}

// Read implements the RNG interface by generating random bytes using MT19937
func (m *MT19937Rand) Read(ctx context.Context, b []byte) error {

	m.lock.Lock()
	defer m.lock.Unlock()

	for i := range b {
		b[i] = byte(m.wrapper.Intn(256))
	}

	return nil
}
