package pad

import (
	"context"
	"testing"

	"github.com/rayozzie/padlock/pkg/trace"
)

// TestCryptoRNGRandomness tests the randomness of CryptoRNG
func TestCryptoRNGRandomness(t *testing.T) {
	// Create a context with tracing
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create a CryptoRNG instance
	rng := &CryptoRNG{}

	// Test buffer (larger sample for statistical tests)
	const bufSize = 100000
	buf := make([]byte, bufSize)

	// Get random bytes
	n, err := rng.Read(ctx, buf)
	if err != nil {
		t.Fatalf("CryptoRNG read failed: %v", err)
	}
	if n != bufSize {
		t.Fatalf("CryptoRNG returned short read: got %d, want %d", n, bufSize)
	}

	// Run statistical tests on the output
	runRandomnessTests(t, "CryptoRNG", buf)
}

// TestMathRNGRandomness tests the randomness of MathRNG
func TestMathRNGRandomness(t *testing.T) {
	// Create a context with tracing
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create a MathRNG instance
	rng := NewMathRNG()

	// Test buffer (larger sample for statistical tests)
	const bufSize = 100000
	buf := make([]byte, bufSize)

	// Get random bytes
	n, err := rng.Read(ctx, buf)
	if err != nil {
		t.Fatalf("MathRNG read failed: %v", err)
	}
	if n != bufSize {
		t.Fatalf("MathRNG returned short read: got %d, want %d", n, bufSize)
	}

	// Run statistical tests on the output
	runRandomnessTests(t, "MathRNG", buf)
}

// TestChaCha20RandRandomness tests the randomness of ChaCha20Rand
func TestChaCha20RandRandomness(t *testing.T) {
	// Create a context with tracing
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create a ChaCha20Rand instance
	rng := NewChaCha20Rand()

	// Test buffer (larger sample for statistical tests)
	const bufSize = 100000
	buf := make([]byte, bufSize)

	// Get random bytes
	n, err := rng.Read(ctx, buf)
	if err != nil {
		t.Fatalf("ChaCha20Rand read failed: %v", err)
	}
	if n != bufSize {
		t.Fatalf("ChaCha20Rand returned short read: got %d, want %d", n, bufSize)
	}

	// Run statistical tests on the output
	runRandomnessTests(t, "ChaCha20Rand", buf)
}

// TestPCG64RandRandomness tests the randomness of PCG64Rand (math/rand/v2 implementation)
func TestPCG64RandRandomness(t *testing.T) {
	// Create a context with tracing
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create a PCG64Rand instance
	rng := NewPCG64Rand()

	// Test buffer (larger sample for statistical tests)
	const bufSize = 100000
	buf := make([]byte, bufSize)

	// Get random bytes
	n, err := rng.Read(ctx, buf)
	if err != nil {
		t.Fatalf("PCG64Rand read failed: %v", err)
	}
	if n != bufSize {
		t.Fatalf("PCG64Rand returned short read: got %d, want %d", n, bufSize)
	}

	// Run statistical tests on the output
	runRandomnessTests(t, "PCG64Rand", buf)
}

// TestMT19937RandRandomness tests the randomness of MT19937Rand
func TestMT19937RandRandomness(t *testing.T) {
	// Create a context with tracing
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create a MT19937Rand instance
	rng := NewMT19937Rand()

	// Test buffer (larger sample for statistical tests)
	const bufSize = 100000
	buf := make([]byte, bufSize)

	// Get random bytes
	n, err := rng.Read(ctx, buf)
	if err != nil {
		t.Fatalf("MT19937Rand read failed: %v", err)
	}
	if n != bufSize {
		t.Fatalf("MT19937Rand returned short read: got %d, want %d", n, bufSize)
	}

	// Run statistical tests on the output
	runRandomnessTests(t, "MT19937Rand", buf)
}

// TestTestRNGPredictability verifies that TestRNG produces predictable sequences
func TestTestRNGPredictability(t *testing.T) {
	// Create a context with tracing
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create two TestRNG instances
	rng1 := &TestRNG{}
	rng2 := &TestRNG{}

	// Test buffer
	buf1 := make([]byte, 1024)
	buf2 := make([]byte, 1024)

	// Get random bytes from both RNGs
	_, err := rng1.Read(ctx, buf1)
	if err != nil {
		t.Fatalf("TestRNG read failed: %v", err)
	}

	_, err = rng2.Read(ctx, buf2)
	if err != nil {
		t.Fatalf("TestRNG read failed: %v", err)
	}

	// Verify that both RNGs produced the same sequence
	for i := 0; i < len(buf1); i++ {
		if buf1[i] != buf2[i] {
			t.Errorf("TestRNG instances produced different sequences at index %d: %d != %d", i, buf1[i], buf2[i])
			break
		}
	}

	// Verify the sequence matches expectations (counter should increment by 1 each time)
	for i := 0; i < len(buf1); i++ {
		if buf1[i] != byte(i) {
			t.Errorf("TestRNG did not produce expected sequence at index %d: got %d, want %d", i, buf1[i], i)
			break
		}
	}
}
