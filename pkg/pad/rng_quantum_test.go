package pad

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rayozzie/padlock/pkg/trace"
)

// TestQuantumEnabledFlag tests the context value for quantum RNG enablement
func TestQuantumEnabledFlag(t *testing.T) {
	// Create a basic context
	ctx := context.Background()

	// Test default value (should be disabled)
	enabled := IsQuantumEnabled(ctx)
	if enabled {
		t.Errorf("Expected quantum RNG to be disabled by default, but it was enabled")
	}

	// Test explicit enablement
	ctx = WithQuantumEnabled(ctx, true)
	enabled = IsQuantumEnabled(ctx)
	if !enabled {
		t.Errorf("Expected quantum RNG to be enabled after setting flag, but it was disabled")
	}

	// Test explicit disablement
	ctx = WithQuantumEnabled(ctx, false)
	enabled = IsQuantumEnabled(ctx)
	if enabled {
		t.Errorf("Expected quantum RNG to be disabled after unsetting flag, but it was enabled")
	}
}

// TestQuantumRandWithMockAPI tests the quantum RNG implementation with a mock API
func TestQuantumRandWithMockAPI(t *testing.T) {
	// Create a mock server that simulates the ANU QRNG API
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return a successful response with 10 random bytes
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"type":"uint8","length":10,"data":[42,56,123,99,212,78,14,38,222,118],"success":true}`))
	}))
	defer mockServer.Close()

	// Create a quantum RNG that uses our mock server
	qrng := NewQuantumRand()
	qrng.apiURL = mockServer.URL                         // Override the API URL
	qrng.client = &http.Client{Timeout: 1 * time.Second} // Short timeout for testing

	// Create a context with tracer
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Test buffer
	buf := make([]byte, 20)

	// Read random bytes
	err := qrng.Read(ctx, buf)
	if err != nil {
		t.Errorf("Failed to read random bytes: %v", err)
	}

	// Verify we got some non-zero data
	hasNonZero := false
	for _, b := range buf {
		if b != 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		t.Errorf("Expected non-zero bytes in the output, got all zeros")
	}
}

// TestQuantumRandWithFailingAPI tests the quantum RNG behavior when the API fails
func TestQuantumRandWithFailingAPI(t *testing.T) {
	// Create a mock server that simulates a failing API
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return an error response
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"success":false,"message":"Service is currently unavailable"}`))
	}))
	defer mockServer.Close()

	// Create a quantum RNG that uses our mock server
	qrng := NewQuantumRand()
	qrng.apiURL = mockServer.URL                         // Override the API URL
	qrng.client = &http.Client{Timeout: 1 * time.Second} // Short timeout for testing

	// Create a context with tracer
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)

	// Test buffer
	buf := make([]byte, 20)

	// Read random bytes - should fail
	err := qrng.Read(ctx, buf)
	if err == nil {
		t.Errorf("Expected error when API fails, but got nil")
	}
}

// TestDefaultRandWithQuantumEnabled tests the NewDefaultRand function with quantum RNG enabled
func TestDefaultRandWithQuantumEnabled(t *testing.T) {
	// Create a context with quantum RNG enabled
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)
	ctx = WithQuantumEnabled(ctx, true)

	// Create RNG
	rng := NewDefaultRand(ctx)

	// Verify it's a MultiRNG
	multiRNG, ok := rng.(*MultiRNG)
	if !ok {
		t.Fatalf("Expected NewDefaultRand to return a MultiRNG, got %T", rng)
	}

	// Count the sources
	sourceCount := len(multiRNG.Sources)

	// We expect 6 sources when quantum is enabled (including quantum RNG)
	expectedSources := 6
	if sourceCount != expectedSources {
		t.Errorf("Expected %d sources with quantum enabled, got %d", expectedSources, sourceCount)
	}

	// Last source should be a QuantumRand
	_, ok = multiRNG.Sources[sourceCount-1].(*QuantumRand)
	if !ok {
		t.Errorf("Expected last source to be a QuantumRand, got %T", multiRNG.Sources[sourceCount-1])
	}
}

// TestDefaultRandWithQuantumDisabled tests the NewDefaultRand function with quantum RNG disabled
func TestDefaultRandWithQuantumDisabled(t *testing.T) {
	// Create a context with quantum RNG disabled
	ctx := context.Background()
	tracer := trace.NewTracer("TEST", trace.LogLevelVerbose)
	ctx = trace.WithContext(ctx, tracer)
	ctx = WithQuantumEnabled(ctx, false)

	// Create RNG
	rng := NewDefaultRand(ctx)

	// Verify it's a MultiRNG
	multiRNG, ok := rng.(*MultiRNG)
	if !ok {
		t.Fatalf("Expected NewDefaultRand to return a MultiRNG, got %T", rng)
	}

	// Count the sources
	sourceCount := len(multiRNG.Sources)

	// We expect 5 sources when quantum is disabled (no quantum RNG)
	expectedSources := 5
	if sourceCount != expectedSources {
		t.Errorf("Expected %d sources with quantum disabled, got %d", expectedSources, sourceCount)
	}

	// Last source should not be a QuantumRand
	_, ok = multiRNG.Sources[sourceCount-1].(*QuantumRand)
	if ok {
		t.Errorf("Expected last source not to be a QuantumRand, but it was")
	}
}
