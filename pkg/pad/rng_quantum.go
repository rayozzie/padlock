package pad

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/rayozzie/padlock/pkg/trace"
)

// quantumEnabledKey is a context key for storing whether ANU Quantum RNG is enabled
type quantumEnabledKey struct{}

// WithQuantumEnabled returns a new context with quantum RNG enabled or disabled
func WithQuantumEnabled(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, quantumEnabledKey{}, enabled)
}

// IsQuantumEnabled returns whether quantum RNG is enabled in this context
func IsQuantumEnabled(ctx context.Context) bool {
	if val, ok := ctx.Value(quantumEnabledKey{}).(bool); ok {
		return val
	}
	return false
}

// quantomRandResponse represents the JSON response from the ANU QRNG API.
type quantomRandResponse struct {
	Type    string `json:"type"`
	Length  int    `json:"length"`
	Data    []uint `json:"data"`
	Success bool   `json:"success"`
}

// QuantumRand is a random number generator that uses the ANU Quantum Random Numbers API.
//
// This implementation connects to the Australian National University's quantum random
// number generator service, which produces true randomness from quantum vacuum fluctuations.
// This source provides high-quality entropy derived from fundamental quantum processes.
//
// The ANU Quantum Random Numbers service: https://qrng.anu.edu.au
//
// Key security properties:
// - Provides true random numbers from quantum vacuum fluctuations (not algorithmic)
// - Offers an independent, external source of entropy to complement local sources
// - Ensures high statistical quality of randomness based on quantum physics
//
// Limitations:
// - Requires network connectivity
// - Subject to rate limiting and service availability
// - External dependency introduces potential for service outages
// - Network-based delivery may be subject to interception (mitigated by XOR mixing)
//
// Usage:
// This generator is included in MultiRNG via NewDefaultRNG() only when quantum
// RNG is explicitly enabled with the -quantum-anu flag.
type QuantumRand struct {
	// apiURL is the endpoint for the ANU QRNG API
	apiURL string
	// client is the HTTP client used for API requests
	client *http.Client
	// lock protects against concurrent access
	lock sync.Mutex
	// cache stores pre-fetched random bytes to reduce API calls
	cache []byte
	// maxCacheSize is the maximum number of bytes to cache
	maxCacheSize int
	// maxRequestSize is the maximum number of bytes to request in a single API call
	maxRequestSize int
}

// NewQuantumRand creates a new quantum random number generator that uses the ANU QRNG API.
func NewQuantumRand() *QuantumRand {
	return &QuantumRand{
		apiURL:         "https://qrng.anu.edu.au/API/jsonI.php",
		client:         &http.Client{Timeout: 10 * time.Second},
		cache:          make([]byte, 0, 1024),
		maxCacheSize:   8192, // 8KB cache
		maxRequestSize: 1024, // Request 1024 bytes at a time (API limit)
	}
}

// Name
func (q *QuantumRand) Name() string {
	return "quantum"
}

// Read implements the RNG interface by fetching quantum random numbers from the ANU QRNG API.
// It handles caching to reduce network calls.
func (q *QuantumRand) Read(ctx context.Context, p []byte) error {
	log := trace.FromContext(ctx).WithPrefix("QUANTUM-RNG")

	q.lock.Lock()
	defer q.lock.Unlock()

	// Fill the provided buffer from our cache and API as needed
	bytesRead := 0
	for bytesRead < len(p) {
		// If the cache is empty, refill it
		if len(q.cache) == 0 {
			if err := q.refillCache(ctx, log); err != nil {
				log.Error(fmt.Errorf("quantum RNG refill failed: %w", err))
				return fmt.Errorf("quantum RNG unavailable: %w", err)
			}
		}

		// Copy as many bytes as we can from the cache
		n := copy(p[bytesRead:], q.cache)
		bytesRead += n

		// Update the cache, removing the bytes we just copied
		q.cache = q.cache[n:]
	}

	return nil
}

// refillCache fetches random bytes from the ANU QRNG API and stores them in the cache.
func (q *QuantumRand) refillCache(ctx context.Context, log *trace.Tracer) error {
	// Determine how many bytes to request
	bytesToRequest := q.maxCacheSize - len(q.cache)
	if bytesToRequest <= 0 {
		return nil
	}

	// Limit the request size to avoid overloading the API
	if bytesToRequest > q.maxRequestSize {
		bytesToRequest = q.maxRequestSize
	}

	log.Debugf("Refilling quantum cache with %d bytes from API", bytesToRequest)

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Continue with the request
	}

	// Prepare the request URL
	url := fmt.Sprintf("%s?length=%d&type=uint8", q.apiURL, bytesToRequest)

	// Create a new request with context
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Add appropriate headers
	req.Header.Add("User-Agent", "Padlock-One-Time-Pad/1.0")
	req.Header.Add("Accept", "application/json")

	// Execute the request
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check the response status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API returned non-OK status %d: %s", resp.StatusCode, body)
	}

	// Parse the response
	var qResp quantomRandResponse
	if err := json.NewDecoder(resp.Body).Decode(&qResp); err != nil {
		return fmt.Errorf("failed to decode API response: %w", err)
	}

	// Verify the response
	if !qResp.Success {
		return fmt.Errorf("API reported non-success status")
	}
	if qResp.Type != "uint8" {
		return fmt.Errorf("unexpected data type in response: %s", qResp.Type)
	}
	if len(qResp.Data) == 0 {
		return fmt.Errorf("API returned empty data array")
	}

	// Convert the uint array to bytes and add to cache
	newBytes := make([]byte, len(qResp.Data))
	for i, val := range qResp.Data {
		newBytes[i] = byte(val)
	}

	// Append the new bytes to our cache
	q.cache = append(q.cache, newBytes...)
	log.Debugf("Successfully added %d quantum random bytes to cache", len(newBytes))

	return nil
}
