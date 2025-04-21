package pad

import (
	"context"
)

// TestRNG is a deterministic RNG implementation for testing purposes.
//
// This RNG generates a predictable sequence of bytes based on a counter,
// which makes tests reproducible. It is NOT secure for actual cryptographic
// use, but is valuable for testing code that depends on the RNG interface.
//
// The key property is that it will produce the exact same sequence of bytes
// when created with the same initial counter value, which allows for
// deterministic test behavior.
type TestRNG struct {
	// counter is a byte that increments with each byte generated
	counter byte
}

// NewTestRNG creates a new test RNG with an initial counter value.
func NewTestRNG(initialValue byte) *TestRNG {
	return &TestRNG{counter: initialValue}
}

// Name
func (r *TestRNG) Name() string {
	return "test"
}

// Read implements the RNG interface with a deterministic, counter-based
// random number generator suitable for testing.
func (r *TestRNG) Read(ctx context.Context, p []byte) (err error) {
	// Normal behavior: fill the buffer with sequential counter values
	for i := range p {
		p[i] = r.counter
		r.counter++
	}
	return nil
}
