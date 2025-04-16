package padlock

import (
	crand "crypto/rand"
	"fmt"
	mrand "math/rand"
)

// RNG is a simple interface for reading random bytes.
type RNG interface {
	Read(p []byte) (n int, err error)
}

// CryptoRNG uses crypto/rand.
type CryptoRNG struct{}

func (r *CryptoRNG) Read(p []byte) (int, error) {
	return crand.Read(p)
}

// MathRNG uses math/rand seeded from crypto/rand.
type MathRNG struct {
	src *mrand.Rand
}

func newMathRNG() *MathRNG {
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
func (mr *MathRNG) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(mr.src.Intn(256))
	}
	return len(p), nil
}

// MultiRNG XORs outputs from multiple RNG sources and ensures each source fills the buffer.
type MultiRNG struct {
	Sources []RNG
}

func (m *MultiRNG) Read(p []byte) (int, error) {
	acc := make([]byte, len(p))
	for i := range acc {
		acc[i] = 0
	}
	for _, s := range m.Sources {
		tmp := make([]byte, len(p))
		offset := 0
		for offset < len(p) {
			n, err := s.Read(tmp[offset:])
			if err != nil {
				return 0, fmt.Errorf("RNG source failed: %w", err)
			}
			if n == 0 {
				continue
			}
			offset += n
		}
		for i := 0; i < len(p); i++ {
			acc[i] ^= tmp[i]
		}
	}
	copy(p, acc)
	return len(p), nil
}

// NewDefaultRNG returns a MultiRNG combining CryptoRNG and MathRNG.
func NewDefaultRNG() RNG {
	return &MultiRNG{
		Sources: []RNG{
			&CryptoRNG{},
			newMathRNG(),
		},
	}
}
