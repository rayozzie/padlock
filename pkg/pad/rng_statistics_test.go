// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// This fixed corpus exercises the statistical checks themselves. It is not
// evidence about the production generators' entropy or unpredictability.
// Do not remove seeds that fail, search for passing seeds, or retry a failure.
func TestRNGStatisticsFixedCorpus(t *testing.T) {
	for _, size := range []int{10240, 100000} {
		for seed := int64(0); seed < 128; seed++ {
			t.Run(fmt.Sprintf("bytes=%d/seed=%d", size, seed), func(t *testing.T) {
				data := make([]byte, size)
				if _, err := rand.New(rand.NewSource(seed)).Read(data); err != nil {
					t.Fatal(err)
				}
				runRandomnessTests(t, "fixed corpus", data)
			})
		}
	}
}

func TestRNGStatisticsRejectDefects(t *testing.T) {
	const size = 100000
	random := make([]byte, size)
	if _, err := rand.New(rand.NewSource(0)).Read(random); err != nil {
		t.Fatal(err)
	}
	stuckBit := bytes.Clone(random)
	repeatedBytes, complementedBytes := make([]byte, size), make([]byte, size)
	for i := range stuckBit {
		stuckBit[i] &= 0x7f
	}
	for i := 0; i < size/2; i++ {
		repeatedBytes[2*i], repeatedBytes[2*i+1] = random[i], random[i]
		complementedBytes[2*i], complementedBytes[2*i+1] = random[i], ^random[i]
	}
	for _, tc := range []struct {
		name  string
		data  []byte
		check func([]byte) error
	}{
		{"zero_bits", make([]byte, size), frequencyTest},
		{"one_bits", bytes.Repeat([]byte{0xff}, size), frequencyTest},
		{"stuck_bit", stuckBit, frequencyTest},
		{"too_many_runs", bytes.Repeat([]byte{0x55}, size), runsTest},
		{"too_few_runs", bytes.Repeat([]byte{0x00, 0xff}, size/2), runsTest},
		{"repeated_bytes", repeatedBytes, autocorrelationTest},
		{"complemented_bytes", complementedBytes, autocorrelationTest},
		{"nonuniform_bytes_with_balanced_bits", bytes.Repeat([]byte{0x0f, 0xf0}, size/2), chiSquareTest},
		{"missing_byte_values", stuckBit, chiSquareTest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.check(tc.data); err == nil {
				t.Fatal("statistical check accepted the deliberately defective stream")
			}
		})
	}
}

func TestRNGStatisticsRequireSamples(t *testing.T) {
	for _, check := range []struct {
		name string
		fn   func([]byte) error
	}{
		{"frequency", frequencyTest},
		{"runs", runsTest},
		{"autocorrelation", autocorrelationTest},
		{"chi-square", chiSquareTest},
	} {
		t.Run(check.name, func(t *testing.T) {
			if err := check.fn(nil); err == nil {
				t.Fatal("statistical check accepted an empty sample")
			}
		})
	}
}

func TestRNGStatisticsLagDiagnostic(t *testing.T) {
	err := autocorrelationTest(bytes.Repeat([]byte{0x55}, 10000))
	if err == nil || !strings.Contains(err.Error(), "lag 1") {
		t.Fatalf("expected the failing lag in the diagnostic, got %v", err)
	}
}
