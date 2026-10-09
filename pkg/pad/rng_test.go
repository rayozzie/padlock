// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"testing"

	"github.com/rayozzie/padlock/pkg/trace"
)

// TestRNGInterfaces verifies that all RNG implementations comply with the RNG interface
func TestRNGInterfaces(t *testing.T) {
	// Create a context with tracing
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Test buffer
	buf := make([]byte, 1024)

	// Test each RNG implementation
	rngs := []RNG{
		NewCryptoRand(),
		NewMathRand(),
		NewDefaultRand(ctx),
		NewTestRNG(0),
		NewChaCha20Rand(),
		NewPCG64Rand(),
		NewMT19937Rand(),
	}

	for i, rng := range rngs {
		err := rng.Read(ctx, buf)
		if err != nil {
			t.Errorf("RNG implementation %d failed to read random bytes: %v", i, err)
		}
	}
}

// TestMultiRNGRandomness tests the randomness of MultiRNG
func TestMultiRNGRandomness(t *testing.T) {
	// Create a context with tracing
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create a MultiRNG instance
	rng := NewDefaultRand(ctx)

	// Test buffer (larger sample for statistical tests)
	const bufSize = 100000
	buf := make([]byte, bufSize)

	// Get random bytes
	err := rng.Read(ctx, buf)
	if err != nil {
		t.Fatalf("MultiRNG read failed: %v", err)
	}

	// Run statistical tests on the output
	runRandomnessTests(t, "MultiRNG", buf)
}

// Helper functions for statistical tests

// TestStreamBasedRNG tests that the MultiRNG can be used in a streaming fashion
// where random bytes are generated in multiple chunks rather than all at once.
func TestStreamBasedRNG(t *testing.T) {
	// Create a context with tracing
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create a MultiRNG instance
	rng := NewDefaultRand(ctx)

	// Set up multiple buffers to simulate streaming
	const bufSize = 1024
	buffers := make([][]byte, 10)
	for i := range buffers {
		buffers[i] = make([]byte, bufSize)
	}

	// Generate random bytes in multiple calls (simulating streaming)
	for i := range buffers {
		err := rng.Read(ctx, buffers[i])
		if err != nil {
			t.Fatalf("MultiRNG read failed on buffer %d: %v", i, err)
		}
	}

	// Combine all buffers for statistical analysis
	combinedBuffer := make([]byte, bufSize*len(buffers))
	for i, buf := range buffers {
		copy(combinedBuffer[i*bufSize:], buf)
	}

	// Run statistical tests on the combined output
	runRandomnessTests(t, "MultiRNG-Stream", combinedBuffer)

	// Verify that each buffer has different content (not duplicated)
	for i := 0; i < len(buffers)-1; i++ {
		if bytes.Equal(buffers[i], buffers[i+1]) {
			t.Errorf("Buffers %d and %d have identical content, which is extremely unlikely with proper randomness", i, i+1)
		}
	}
}

// statisticalTestAlpha is the per-criterion false-alarm target under the IID
// uniform model. Each sample has nine criteria (frequency, runs, six lags, and
// chi-square); seven live samples currently run. The chi-square calibration is
// asymptotic, so this is not a rigorous finite-sample bound for the whole suite.
// Use a conservative target for automated regression tests, not 3/4-sigma
// cutoffs that routinely reject healthy output when applied many times.
const statisticalTestAlpha = 1e-9

// fairBitCountDeviation inverts the two-sided Hoeffding bound
// P(|S - n/2| >= d) <= 2*exp(-2*d*d/n) for independent fair bits.
// https://doi.org/10.1080/01621459.1963.10500830 (Theorem 2).
func fairBitCountDeviation(trials int) float64 {
	return math.Sqrt(float64(trials) * math.Log(2/statisticalTestAlpha) / 2)
}

// runRandomnessTests checks samples for some gross defects. Passing does not
// prove independence, unpredictability, or security. Keep failures visible;
// never retry a sample until it passes.
func runRandomnessTests(t *testing.T, rngName string, data []byte) {
	t.Helper()
	// Run frequency test (distribution of 0s and 1s at bit level)
	if err := frequencyTest(data); err != nil {
		t.Errorf("%s failed frequency test: %v", rngName, err)
	}

	// Run runs test (consecutive identical bits)
	if err := runsTest(data); err != nil {
		t.Errorf("%s failed runs test: %v", rngName, err)
	}

	// Report histogram entropy as a diagnostic. Byte uniformity is checked
	// once, by the aggregate chi-square statistic below, not by another 256
	// separate per-byte thresholds or an uncalibrated entropy cutoff.
	entropy := calculateEntropy(data)
	t.Logf("%s entropy: %.6f bits per byte (ideal: 8.0)", rngName, entropy)

	// Run autocorrelation test
	if err := autocorrelationTest(data); err != nil {
		t.Errorf("%s failed autocorrelation test: %v", rngName, err)
	}

	// Run chi-square test on byte frequencies
	if err := chiSquareTest(data); err != nil {
		t.Errorf("%s failed chi-square test: %v", rngName, err)
	}

	// Calculate a simple hash of the data for verification
	hash := sha256.Sum256(data)
	t.Logf("%s output hash (first 8 bytes): %x", rngName, hash[:8])
}

// frequencyTest checks if the proportion of 1s and 0s in the bit sequence
// is approximately 50% each, as expected from a random sequence.
func frequencyTest(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("frequency test requires a nonempty sample")
	}
	bitCount := 0
	for _, b := range data {
		// Count bits in byte using Hamming weight (population count)
		for mask := byte(1); mask > 0; mask <<= 1 {
			if (b & mask) != 0 {
				bitCount++
			}
		}
	}

	totalBits := len(data) * 8
	proportion := float64(bitCount) / float64(totalBits)

	deviation := math.Abs(proportion - 0.5)
	maxDeviation := fairBitCountDeviation(totalBits) / float64(totalBits)

	if deviation > maxDeviation {
		return &randomnessError{
			test:     "frequency",
			got:      proportion,
			expected: 0.5,
			maxDev:   maxDeviation,
		}
	}

	return nil
}

// runsTest checks for unusually many or few runs of identical bits.
func runsTest(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("runs test requires a nonempty sample")
	}
	// Extract bits into a slice for easier processing
	bits := make([]bool, len(data)*8)
	for i, b := range data {
		for j := 0; j < 8; j++ {
			bits[i*8+j] = ((b >> j) & 1) == 1
		}
	}

	// Count runs
	runCount := 1 // Start at 1 for the first run
	for i := 1; i < len(bits); i++ {
		if bits[i] != bits[i-1] {
			runCount++
		}
	}

	// For m independent fair bits, the m-1 transition indicators are also
	// independent fair bits: runs = 1 + Binomial(m-1, 1/2).
	transitions := len(bits) - 1
	expectedRuns := 1 + float64(transitions)/2
	deviation := math.Abs(float64(runCount) - expectedRuns)
	maxDeviation := fairBitCountDeviation(transitions)

	if deviation > maxDeviation {
		return &randomnessError{
			test:     "runs",
			got:      float64(runCount),
			expected: expectedRuns,
			maxDev:   maxDeviation,
		}
	}

	return nil
}

// calculateEntropy calculates the Shannon entropy (in bits per symbol)
// in the sample histogram, which does not measure unpredictability.
func calculateEntropy(data []byte) float64 {
	if len(data) == 0 {
		return 0
	}

	// Count occurrences of each byte value
	counts := make([]int, 256)
	for _, b := range data {
		counts[b]++
	}

	// Calculate entropy
	entropy := 0.0
	for _, count := range counts {
		if count > 0 {
			p := float64(count) / float64(len(data))
			entropy -= p * math.Log2(p)
		}
	}

	return entropy
}

// autocorrelationTest checks for unusually high or low bit agreement at fixed lags.
func autocorrelationTest(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("autocorrelation test requires a nonempty sample")
	}
	// Extract bits into a slice for easier processing
	bits := make([]bool, len(data)*8)
	for i, b := range data {
		for j := 0; j < 8; j++ {
			bits[i*8+j] = ((b >> j) & 1) == 1
		}
	}

	// Check autocorrelation at various lags
	lags := []int{1, 2, 8, 16, 32, 64}
	for _, lag := range lags {
		if lag >= len(bits) {
			continue
		}

		matchCount := 0
		comparisonCount := len(bits) - lag

		for i := 0; i < comparisonCount; i++ {
			if bits[i] == bits[i+lag] {
				matchCount++
			}
		}

		// Fraction of agreeing bit pairs, not a Pearson correlation coefficient.
		correlation := float64(matchCount) / float64(comparisonCount)

		// For a fixed lag, the comparisons form disjoint chains. Under IID
		// fair input bits their equality indicators are independent fair bits.
		deviation := math.Abs(correlation - 0.5)
		maxDeviation := fairBitCountDeviation(comparisonCount) / float64(comparisonCount)

		if deviation > maxDeviation {
			return &randomnessError{
				test:     "autocorrelation",
				lag:      lag,
				got:      correlation,
				expected: 0.5,
				maxDev:   maxDeviation,
			}
		}
	}

	return nil
}

// chiSquareTest performs a chi-square test on the byte frequencies
// to check for uniform distribution.
func chiSquareTest(data []byte) error {
	if len(data) < 5*256 {
		return fmt.Errorf("chi-square test requires at least %d sample bytes", 5*256)
	}
	// Count occurrences of each byte value
	counts := make([]int, 256)
	for _, b := range data {
		counts[b]++
	}

	// Calculate chi-square statistic
	expectedCount := float64(len(data)) / 256
	chiSquare := 0.0
	for _, count := range counts {
		deviation := float64(count) - expectedCount
		chiSquare += (deviation * deviation) / expectedCount
	}

	// Pearson's statistic approaches chi-square with 255 degrees of freedom.
	// Laurent-Massart bounds give two tail probabilities of at most exp(-x)
	// for that limiting distribution. This is an asymptotic calibration for
	// the finite byte histogram, not an exact probability or security claim.
	// https://doi.org/10.1214/aos/1015957395 (equations 4.3 and 4.4).
	const degrees = 255
	x := math.Log(2 / statisticalTestAlpha)
	lower := degrees - 2*math.Sqrt(degrees*x)
	upper := degrees + 2*math.Sqrt(degrees*x) + 2*x
	if chiSquare < lower || chiSquare > upper {
		return fmt.Errorf("chi-square test failed: got %.6f, expected within [%.6f, %.6f]", chiSquare, lower, upper)
	}

	return nil
}

// randomnessError represents a failure in a randomness test.
type randomnessError struct {
	test     string  // The name of the test that failed
	lag      int     // Optional lag value for autocorrelation
	got      float64 // The observed value
	expected float64 // The expected value for truly random data
	maxDev   float64 // The maximum allowable deviation
}

func (e *randomnessError) Error() string {
	if e.lag > 0 {
		return fmt.Sprintf("%s test failed for lag %d: got %.6f, expected %.6f±%.6f", e.test, e.lag, e.got, e.expected, e.maxDev)
	}
	return fmt.Sprintf("%s test failed: got %.6f, expected %.6f±%.6f", e.test, e.got, e.expected, e.maxDev)
}
