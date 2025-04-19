package pad

import (
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
		&CryptoRNG{},
		NewMathRNG(),
		NewDefaultRNG(),
		&TestRNG{},
	}

	for i, rng := range rngs {
		n, err := rng.Read(ctx, buf)
		if err != nil {
			t.Errorf("RNG implementation %d failed to read random bytes: %v", i, err)
		}
		if n != len(buf) {
			t.Errorf("RNG implementation %d returned short read: got %d, want %d", i, n, len(buf))
		}
	}
}

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

// TestMultiRNGRandomness tests the randomness of MultiRNG
func TestMultiRNGRandomness(t *testing.T) {
	// Create a context with tracing
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Create a MultiRNG instance
	rng := NewDefaultRNG()

	// Test buffer (larger sample for statistical tests)
	const bufSize = 100000
	buf := make([]byte, bufSize)

	// Get random bytes
	n, err := rng.Read(ctx, buf)
	if err != nil {
		t.Fatalf("MultiRNG read failed: %v", err)
	}
	if n != bufSize {
		t.Fatalf("MultiRNG returned short read: got %d, want %d", n, bufSize)
	}

	// Run statistical tests on the output
	runRandomnessTests(t, "MultiRNG", buf)
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

// Helper functions for statistical tests

// runRandomnessTests applies a suite of statistical tests to evaluate the randomness
// of the provided byte slice. These tests are based on well-established cryptographic
// testing methodologies, but simplified for unit testing purposes.
func runRandomnessTests(t *testing.T, rngName string, data []byte) {
	// Run frequency test (distribution of 0s and 1s at bit level)
	if err := frequencyTest(data); err != nil {
		t.Errorf("%s failed frequency test: %v", rngName, err)
	}

	// Run runs test (consecutive identical bits)
	if err := runsTest(data); err != nil {
		t.Errorf("%s failed runs test: %v", rngName, err)
	}

	// Run byte distribution test (distribution of byte values)
	if err := byteDistributionTest(data); err != nil {
		t.Errorf("%s failed byte distribution test: %v", rngName, err)
	}

	// Run entropy test (Shannon entropy)
	entropy := calculateEntropy(data)
	t.Logf("%s entropy: %.6f bits per byte (ideal: 8.0)", rngName, entropy)
	if entropy < 7.9 {
		t.Errorf("%s has suspiciously low entropy: %.6f bits per byte", rngName, entropy)
	}

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

	// For a truly random sequence, proportion should be close to 0.5
	// We use a 3-sigma confidence interval
	deviation := math.Abs(proportion - 0.5)
	maxDeviation := 3.0 * math.Sqrt(0.25/float64(totalBits))

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

// runsTest checks for the number of runs (consecutive sequences of identical bits)
// to verify independence of bits in the sequence.
func runsTest(data []byte) error {
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

	// For a random sequence, the expected number of runs is approximately:
	// (number of bits / 2) + 1
	expectedRuns := float64(len(bits)/2) + 1
	stdDev := math.Sqrt(float64(len(bits)-1) / 4)

	// Check if the observed run count is within a reasonable range
	// We use a 3-sigma confidence interval
	deviation := math.Abs(float64(runCount) - expectedRuns)
	maxDeviation := 3.0 * stdDev

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

// byteDistributionTest checks if the distribution of byte values is uniform.
func byteDistributionTest(data []byte) error {
	// Count occurrences of each byte value
	counts := make([]int, 256)
	for _, b := range data {
		counts[b]++
	}

	// For a uniform distribution, each value should appear approximately
	// the same number of times
	expectedCount := float64(len(data)) / 256

	// Check if the distribution is within a reasonable range
	// We use a looser bound here since perfect uniformity is not expected
	// with finite samples
	maxDeviation := 4.0 * math.Sqrt(expectedCount)

	for i, count := range counts {
		deviation := math.Abs(float64(count) - expectedCount)
		if deviation > maxDeviation {
			return &randomnessError{
				test:     "byte distribution",
				value:    i,
				got:      float64(count),
				expected: expectedCount,
				maxDev:   maxDeviation,
			}
		}
	}

	return nil
}

// calculateEntropy calculates the Shannon entropy (in bits per symbol)
// of the data, which measures the randomness/unpredictability.
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

// autocorrelationTest checks for correlations between bits at different positions,
// which would indicate non-randomness.
func autocorrelationTest(data []byte) error {
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

		// Calculate correlation coefficient
		correlation := float64(matchCount) / float64(comparisonCount)

		// For a random sequence, the correlation should be close to 0.5
		deviation := math.Abs(correlation - 0.5)
		// Use a slightly more lenient boundary (4-sigma instead of 3-sigma)
		maxDeviation := 4.0 * math.Sqrt(0.25/float64(comparisonCount))

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

	// For 255 degrees of freedom (256 categories - 1),
	// the chi-square value should be approximately 255 ± some reasonable margin
	// if the distribution is uniform
	expectedChiSquare := 255.0
	stdDev := math.Sqrt(2 * 255)
	// Use a more lenient threshold for chi-square (5-sigma instead of 3-sigma)
	// This is reasonable as crypto/rand is known to be secure but can have statistical variations in small samples
	maxDeviation := 5.0 * stdDev

	if math.Abs(chiSquare-expectedChiSquare) > maxDeviation {
		return &randomnessError{
			test:     "chi-square",
			got:      chiSquare,
			expected: expectedChiSquare,
			maxDev:   maxDeviation,
		}
	}

	return nil
}

// randomnessError represents a failure in a randomness test.
type randomnessError struct {
	test     string  // The name of the test that failed
	value    int     // Optional value (e.g., byte value)
	lag      int     // Optional lag value for autocorrelation
	got      float64 // The observed value
	expected float64 // The expected value for truly random data
	maxDev   float64 // The maximum allowable deviation
}

func (e *randomnessError) Error() string {
	if e.value >= 0 {
		return formatError(e.test, float64(e.value), e.got, e.expected, e.maxDev)
	}
	if e.lag > 0 {
		return formatErrorWithLag(e.test, e.lag, e.got, e.expected, e.maxDev)
	}
	return formatErrorNoValue(e.test, e.got, e.expected, e.maxDev)
}

func formatError(test string, value, got, expected, maxDev float64) string {
	return formatErrorWithValue(test, "value", value, got, expected, maxDev)
}

func formatErrorWithLag(test string, lag int, got, expected, maxDev float64) string {
	return formatErrorWithValue(test, "lag", float64(lag), got, expected, maxDev)
}

func formatErrorWithValue(test, label string, value, got, expected, maxDev float64) string {
	return f("%s test failed for %s %.0f: got %.6f, expected %.6f±%.6f", test, label, value, got, expected, maxDev)
}

func formatErrorNoValue(test string, got, expected, maxDev float64) string {
	return f("%s test failed: got %.6f, expected %.6f±%.6f", test, got, expected, maxDev)
}

// Helper function for string formatting
func f(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}
