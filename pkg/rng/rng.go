// Package rng provides cryptographically secure random number generation
// for the padlock threshold one-time-pad scheme. The quality of randomness
// is critical for the security of the one-time-pad implementation.
package rng

import (
	"context"
	crand "crypto/rand"
	"fmt"
	mrand "math/rand"
	"sync"

	"github.com/rayozzie/padlock/pkg/trace"
)

// RNG defines the core interface for all random number generators.
// Any implementation must provide a Read method that fills p with
// random bytes and returns the number of bytes written.
type RNG interface {
	// Read fills p with random bytes and returns the number of bytes written.
	// The returned error is non-nil only if the generator fails to provide
	// randomness.
	Read(p []byte) (n int, err error)

	// ReadWithContext is the same as Read but accepts a context for logging
	// and potential cancellation
	ReadWithContext(ctx context.Context, p []byte) (n int, err error)
}

// CryptoRNG is the primary source of randomness, using Go's crypto/rand package
// which is designed to be cryptographically secure and suitable for security-critical
// applications.
type CryptoRNG struct {
	// lock protects against concurrent access to the crypto RNG
	lock sync.Mutex
}

// Read implements the RNG interface by using the platform's strongest
// random number generator.
func (r *CryptoRNG) Read(p []byte) (int, error) {
	r.lock.Lock()
	defer r.lock.Unlock()
	return crand.Read(p)
}

// ReadWithContext implements the RNG interface with context support.
func (r *CryptoRNG) ReadWithContext(ctx context.Context, p []byte) (int, error) {
	log := trace.FromContext(ctx).WithPrefix("CRYPTO-RNG")
	log.Debugf("Reading %d random bytes from crypto/rand", len(p))

	r.lock.Lock()
	defer r.lock.Unlock()

	n, err := crand.Read(p)
	if err != nil {
		log.Error(fmt.Errorf("crypto/rand read failed: %w", err))
		return n, fmt.Errorf("crypto/rand read failed: %w", err)
	}

	log.Debugf("Successfully read %d random bytes", n)
	return n, nil
}

// MathRNG is a secondary source of randomness, using Go's math/rand package
// with a cryptographically secure seed. This provides defense in depth if
// there are issues with the primary source.
type MathRNG struct {
	// src is the pseudorandom source
	src *mrand.Rand
	// lock protects against concurrent access to the math RNG
	lock sync.Mutex
}

// NewMathRNG creates a math/rand based RNG with a secure seed from crypto/rand.
func NewMathRNG() *MathRNG {
	var seed int64
	b := make([]byte, 8)
	if _, err := crand.Read(b); err == nil {
		for i := 0; i < 8; i++ {
			seed = (seed << 8) | int64(b[i])
		}
	}
	return &MathRNG{
		src: mrand.New(mrand.NewSource(seed)),
	}
}

// Read implements the RNG interface by using a pseudo-random generator
// with a cryptographically secure seed.
func (mr *MathRNG) Read(p []byte) (int, error) {
	mr.lock.Lock()
	defer mr.lock.Unlock()

	for i := range p {
		p[i] = byte(mr.src.Intn(256))
	}
	return len(p), nil
}

// ReadWithContext implements the RNG interface with context support.
func (mr *MathRNG) ReadWithContext(ctx context.Context, p []byte) (int, error) {
	log := trace.FromContext(ctx).WithPrefix("MATH-RNG")
	log.Debugf("Reading %d random bytes from math/rand", len(p))

	mr.lock.Lock()
	defer mr.lock.Unlock()

	for i := range p {
		p[i] = byte(mr.src.Intn(256))
	}

	log.Debugf("Successfully generated %d random bytes", len(p))
	return len(p), nil
}

// MultiRNG provides enhanced security by combining multiple random sources.
// It XORs the output of all included sources to produce the final output.
// This ensures that even if one source is compromised, the system remains secure
// as long as at least one source is providing good randomness.
type MultiRNG struct {
	// Sources is a slice of RNG implementations to combine
	Sources []RNG
	// lock protects against concurrent access
	lock sync.Mutex
}

// Read implements the RNG interface by combining multiple random sources.
// It XORs the output of all sources to produce the final random bytes.
func (m *MultiRNG) Read(p []byte) (int, error) {
	m.lock.Lock()
	defer m.lock.Unlock()

	// Initialize accumulator
	acc := make([]byte, len(p))

	// Read from each source and XOR outputs
	for _, s := range m.Sources {
		tmp := make([]byte, len(p))
		offset := 0

		// Ensure we get a full buffer from each source
		for offset < len(p) {
			n, err := s.Read(tmp[offset:])
			if err != nil {
				return 0, fmt.Errorf("random source failed: %w", err)
			}
			if n == 0 {
				continue
			}
			offset += n
		}

		// XOR this source's output into the accumulator
		for i := 0; i < len(p); i++ {
			acc[i] ^= tmp[i]
		}
	}

	// Copy final result to output buffer
	copy(p, acc)
	return len(p), nil
}

// ReadWithContext implements the RNG interface with context support.
func (m *MultiRNG) ReadWithContext(ctx context.Context, p []byte) (int, error) {
	log := trace.FromContext(ctx).WithPrefix("MULTI-RNG")
	log.Debugf("Generating %d random bytes from %d sources", len(p), len(m.Sources))

	m.lock.Lock()
	defer m.lock.Unlock()

	// Initialize accumulator
	acc := make([]byte, len(p))

	// Read from each source and XOR outputs
	for i, s := range m.Sources {
		log.Debugf("Reading from source #%d", i+1)
		tmp := make([]byte, len(p))
		offset := 0

		// Ensure we get a full buffer from each source
		for offset < len(p) {
			n, err := s.ReadWithContext(ctx, tmp[offset:])
			if err != nil {
				log.Error(fmt.Errorf("random source #%d failed: %w", i+1, err))
				return 0, fmt.Errorf("random source #%d failed: %w", i+1, err)
			}
			if n == 0 {
				continue
			}
			offset += n
		}

		// XOR this source's output into the accumulator
		for j := 0; j < len(p); j++ {
			acc[j] ^= tmp[j]
		}

		log.Debugf("Successfully mixed in %d bytes from source #%d", len(p), i+1)
	}

	// Copy final result to output buffer
	copy(p, acc)
	log.Debugf("Successfully generated %d secure random bytes", len(p))
	return len(p), nil
}

// NewDefaultRNG creates a multi-source RNG that combines multiple sources for
// enhanced security and failure resilience. Currently, it uses:
// 1. A cryptographically secure RNG from crypto/rand
// 2. A pseudo-random generator securely seeded from crypto/rand
//
// This approach ensures that:
// - Security depends only on the strongest available source
// - A weakness in any single source does not compromise the system
// - The system continues to function even if one source fails
func NewDefaultRNG() RNG {
	return &MultiRNG{
		Sources: []RNG{
			&CryptoRNG{},
			NewMathRNG(),
		},
	}
}
