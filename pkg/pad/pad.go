// Copyright 2025 Ray Ozzie. All rights reserved.

// Package pad implements K-of-N threshold splitting with random pads and XOR.
// Any K intact collections from the same backup can reconstruct the data.
//
// The ideal scheme hides contents from fewer than K collections when all pad
// bytes are uniform, independent of the data and other pads, secret from the
// attacker, and never reused. The default RNG relies on the OS randomness
// source and seeded software generators; it does not establish these
// information-theoretic assumptions or guarantee forward secrecy.
//
// Data is processed in chunks and distributed across all K-of-N combinations.
// On-disk file names use "<collectionName>_<chunkNumber>.<format>"; headers use
// "<collectionName>:<chunkNumber>:<dataBytes>:<backupID>". These headers and
// collection sizes are public. The backup identifier detects accidental mixing.
package pad

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/rayozzie/padlock/pkg/trace"
)

// NewChunkFunc defines a function type for creating new chunk files.
// This is a callback function provided by the caller to create output files for each chunk.
// It creates a file with the specified collection name, chunk number, and format (e.g., bin or png).
// The returned WriteCloser must be properly closed by the caller after writing is complete.
type NewChunkFunc func(collectionName string, chunkNumber int, chunkFormat string) (io.WriteCloser, error)

// Pad represents the configuration for a one-time pad K-of-N threshold scheme operation.
// It maintains the parameters for the threshold scheme and the names of the collections
// that will be generated.
//
// The combinatorial mapping ensures that any K collections contain a complete
// set of pieces for reconstruction. Confidentiality with fewer collections
// depends on the pad randomness described in the package documentation.
type Pad struct {
	TotalCopies      int         // N: Total number of collections to create (2-26)
	RequiredCopies   int         // K: Minimum collections needed for reconstruction (2-N)
	Collections      []string    // Names of each collection (e.g., ["3A5", "3B5", "3C5", ...])
	PermutationCount int         // C(N-1,K-1): Number of pieces in each collection chunk
	SizeTracker      interface{} // Tracks file sizes during encoding and decoding operations
}

// NewPadForEncode creates a new Pad instance with the specified parameters for a K-of-N threshold scheme.
//
// Parameters:
//   - totalCopies (N): The total number of collections to create. Must be between 2 and 26.
//     This represents the total number of shares in the threshold scheme.
//   - requiredCopies (K): The minimum number of collections required to reconstruct the data.
//     Must be at least 2 and not greater than totalCopies.  Note that when creating a pad
//     on decode, just set requiredCopies to the same as totalCopies.
//
// Returns:
//   - A configured Pad instance with generated collection names
//   - An error if the parameters are invalid
//
// Collection names are automatically generated in the format "<K><ID><N>", where:
//   - K is the requiredCopies value
//   - ID is a letter from A-Z representing the collection index
//   - N is the totalCopies value
//
// For example, with K=3, N=5, the collections would be: ["3A5", "3B5", "3C5", "3D5", "3E5"]
func NewPadForEncode(ctx context.Context, totalCopies, requiredCopies int) (*Pad, error) {
	p := &Pad{}
	return p, PadInit(ctx, p, totalCopies, requiredCopies)
}

// NewPadForDecode creates a new Pad instance with the specified parameters for a K-of-N threshold scheme.
//
// Parameters:
//   - availableCopies: The number of input streams, including duplicate collections.
//     Must be at least 2. Decode checks the number of distinct collections against K.
//
// Returns:
//   - A configured Pad instance that can be used until parameters can be extracted
//   - An error if the parameters are invalid
func NewPadForDecode(ctx context.Context, availableCopies int) (*Pad, error) {
	if availableCopies < 2 {
		if availableCopies == 1 {
			return nil, fmt.Errorf("found 1 collection, at least 2 required to decode")
		}
		return nil, fmt.Errorf("found %d collections, at least 2 required to decode", availableCopies)
	}
	p := &Pad{}
	// These are provisional parameters until Decode reads the actual K/N.
	// The 26-letter limit applies to distinct collections, not duplicate inputs.
	provisionalCopies := min(availableCopies, 26)
	return p, PadInit(ctx, p, provisionalCopies, provisionalCopies)
}

// PadInit initializes a new Pad instance with the specified parameters for a K-of-N threshold scheme.
//
// Parameters:
//   - An uninitialized Pad instance
//   - totalCopies (N): The total number of collections to create. Must be between 2 and 26.
//     This represents the total number of shares in the threshold scheme.
//   - requiredCopies (K): The minimum number of collections required to reconstruct the data.
//     Must be at least 2 and not greater than totalCopies.  Note that when creating a pad
//     on decode, just set requiredCopies to the same as totalCopies.
//
// Returns:
//   - An error if the parameters are invalid
//
// Collection names are automatically generated in the format "<K><ID><N>", where:
//   - K is the requiredCopies value
//   - ID is a letter from A-Z representing the collection index
//   - N is the totalCopies value
//
// For example, with K=3, N=5, the collections would be: ["3A5", "3B5", "3C5", "3D5", "3E5"]
func PadInit(ctx context.Context, p *Pad, totalCopies, requiredCopies int) error {
	log := trace.FromContext(ctx).WithPrefix("pad-init")
	// Validate parameters to ensure they meet the requirements of the threshold scheme
	if err := validateCopies(totalCopies, requiredCopies); err != nil {
		return err
	}

	// Set up the Pad instance with the specified parameters
	p.TotalCopies = totalCopies
	p.RequiredCopies = requiredCopies

	// Generate collection names in the format "<K><collectionId><N>"
	// Example: with K=3, N=5, collections = ["3A5", "3B5", "3C5", "3D5", "3E5"]
	p.Collections = make([]string, totalCopies)
	for i := 0; i < totalCopies; i++ {
		collLetter := collectionLetterFromIndex(i)
		p.Collections[i] = buildCollectionLabel(requiredCopies, totalCopies, collLetter)
	}

	// Keep only the count. Encoding visits combinations one at a time;
	// decoding calculates the required payload positions directly.
	p.PermutationCount = collectionPermutationCount(totalCopies, requiredCopies)
	log.Debugf("Pad K=%d N=%d: %d pieces per collection; collections %v", requiredCopies, totalCopies, p.PermutationCount, p.Collections)

	return nil
}

// Create a collection label from parameters
func buildCollectionLabel(requiredCopies, totalCopies int, collLetter string) string {
	return fmt.Sprintf("%d%s%d", requiredCopies, collLetter, totalCopies)
}

// extractFromCollectionLabel parses a label like "3A5" and returns requiredCopies, totalCopies, and collLetter
// with full validation according to the defined rules.
func extractFromCollectionLabel(label string) (requiredCopies int, totalCopies int, collLetter string, err error) {
	if len(label) < 3 {
		return 0, 0, "", fmt.Errorf("label too short")
	}

	// Find first non-digit: expected to be the collection letter
	i := 0
	for i < len(label) && unicode.IsDigit(rune(label[i])) {
		i++
	}
	if i == 0 || i >= len(label)-1 {
		return 0, 0, "", fmt.Errorf("invalid format: expected digits, then letter, then digits")
	}

	requiredStr := label[:i]
	letterChar := label[i]
	totalStr := label[i+1:]

	requiredCopies, err = strconv.Atoi(requiredStr)
	if err != nil {
		return 0, 0, "", fmt.Errorf("invalid requiredCopies: %v", err)
	}

	totalCopies, err = strconv.Atoi(totalStr)
	if err != nil {
		return 0, 0, "", fmt.Errorf("invalid totalCopies: %v", err)
	}

	// Validation: total ∈ [2, 26]
	if totalCopies < 2 || totalCopies > 26 {
		return 0, 0, "", fmt.Errorf("totalCopies out of range: %d", totalCopies)
	}

	// Validation: required ∈ [2, total]
	if requiredCopies < 2 || requiredCopies > totalCopies {
		return 0, 0, "", fmt.Errorf("requiredCopies out of range: %d", requiredCopies)
	}

	// Validation: letter is uppercase and within allowed range
	if letterChar < 'A' || letterChar > byte('A'+totalCopies-1) {
		return 0, 0, "", fmt.Errorf("collLetter %q out of range for total %d", letterChar, totalCopies)
	}

	return requiredCopies, totalCopies, string(letterChar), nil
}

// Get the collection letter for a given 0-based index
func collectionLetterFromIndex(i int) string {
	if i < 0 || i >= 26 {
		panic("index out of range")
	}
	return string(rune('A' + i))
}

// Build a chunk name for a given collection name and chunk number and chunk data size
func buildChunkName(collName string, chunkNumber, chunkDataBytes int, backupID string) string {
	return fmt.Sprintf("%s:%d:%d:%s", collName, chunkNumber, chunkDataBytes, backupID)
}

// extractFromChunkName parses chunkName into its parts, validating each field.
func extractFromChunkName(chunkName string) (collName string, chunkNumber int, chunkDataBytes int, backupID string, err error) {
	parts := strings.Split(chunkName, ":")
	if len(parts) != 3 && len(parts) != 4 {
		return "", 0, 0, "", fmt.Errorf("invalid chunk name format: expected 3 or 4 parts separated by ':'")
	}

	collName = parts[0]

	chunkNumber, err = strconv.Atoi(parts[1])
	if err != nil || chunkNumber <= 0 {
		return "", 0, 0, "", fmt.Errorf("invalid chunkNumber: must be positive integer")
	}

	chunkDataBytes, err = strconv.Atoi(parts[2])
	if err != nil || chunkDataBytes <= 0 {
		return "", 0, 0, "", fmt.Errorf("invalid chunkDataBytes: must be positive integer")
	}

	// Three-field headers are the original format. Accept them without a warning.
	if len(parts) == 4 {
		id, decodeErr := hex.DecodeString(parts[3])
		if decodeErr != nil || len(id) != backupIDBytes {
			return "", 0, 0, "", fmt.Errorf("invalid backup identifier: expected %d hexadecimal characters", 2*backupIDBytes)
		}
		backupID = hex.EncodeToString(id)
	}
	return collName, chunkNumber, chunkDataBytes, backupID, nil
}

// Encode implements the one-time pad encoding process with K-of-N threshold security.
//
// This method takes an input stream and encodes it into N collections such that
// any K intact collections from the same backup can reconstruct the data.
// Confidentiality depends on the supplied RNG; the interface does not certify it.
//
// Parameters:
//   - ctx: Context for logging, cancellation, and tracing
//   - outputChunkBytes: Maximum size for each output chunk in bytes; must be at least PermutationCount
//   - input: Reader providing the data to be encoded
//   - randomSource: Source of random bytes for one-time pad generation
//   - newChunk: Function to create output files for each chunk
//   - chunkFormat: Format for output files (e.g., "bin" or "png")
//
// Process:
//  1. Divide input data into fixed-size chunks
//  2. For each chunk:
//     a. Request K-1 fresh pads for each combination of K collections
//     b. XOR the input with those pads to create the remaining piece
//     c. Distribute each combination's K pieces to its K collections
//     d. Write the data to each collection with proper headers
//
// Security considerations:
//   - Confidentiality depends on the pad randomness described in the package documentation
//   - The same pad must NEVER be reused
//   - Each chunk has a unique name to ensure it's properly tracked during decoding
func (p *Pad) Encode(ctx context.Context, outputChunkBytes int, input io.Reader, randomSource RNG, newChunk NewChunkFunc, chunkFormat string) error {
	log := trace.FromContext(ctx).WithPrefix("encode")
	if err := validateCopies(p.TotalCopies, p.RequiredCopies); err != nil {
		return err
	}
	if p.PermutationCount != collectionPermutationCount(p.TotalCopies, p.RequiredCopies) || len(p.Collections) != p.TotalCopies {
		return fmt.Errorf("invalid pad: collection configuration does not match copy counts")
	}
	for index, collName := range p.Collections {
		if collName != buildCollectionLabel(p.RequiredCopies, p.TotalCopies, collectionLetterFromIndex(index)) {
			return fmt.Errorf("invalid pad: unexpected collection %q at index %d", collName, index)
		}
	}
	if err := validateChunkSize(p.TotalCopies, p.RequiredCopies, outputChunkBytes, p.PermutationCount); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Public metadata uses a separate OS draw, never bytes from the secret pad
	// stream. Generate it per Encode call, including when the Pad is reused.
	var id [backupIDBytes]byte
	if _, err := crand.Read(id[:]); err != nil {
		return fmt.Errorf("generate backup identifier: %w", err)
	}
	backupID := hex.EncodeToString(id[:])

	// Compute a size of input to process in each chunk, given the number of ciphers that must fit into the chunk
	inputChunkBytes := outputChunkBytes / p.PermutationCount
	log.Debugf("Starting encode with inputChunkBytes=%d outputChunkBytes=%d", inputChunkBytes, outputChunkBytes)

	// Process input data chunk by chunk until end of stream
	buffer := make([]byte, inputChunkBytes)
	for chunkIndex := 1; ; chunkIndex++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Read a chunk of data from the input stream
		bytesRead, err := io.ReadFull(input, buffer)
		if bytesRead > 0 {

			// Create a new chunk
			if err := p.encodeOneChunk(ctx, buffer[:bytesRead], chunkIndex, backupID, randomSource, newChunk, chunkFormat); err != nil {
				return err
			}
		}

		// Check for errors or EOF
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			// We've reached the end of the input
			log.Debugf("Reached end of input stream after %d chunks", chunkIndex-1)
			break
		} else if err != nil {
			return fmt.Errorf("input read error: %w", err)
		}
	}

	log.Debugf("Encode completed successfully")
	return nil
}

// encodeOneChunk splits a data chunk across every K-collection combination.
// For each combination it requests K-1 pads R_i, computes C = data XOR R_1 XOR
// ... XOR R_(K-1), and distributes C and the pads as the K pieces. XORing all
// K pieces recovers the data.
//
// The missing pieces hide the data only under the randomness assumptions in
// the package documentation. New RNG reads do not by themselves establish
// independent entropy or an information-theoretic secrecy guarantee.
func (p *Pad) encodeOneChunk(ctx context.Context, chunkData []byte, chunkNumber int, backupID string, randomSource RNG, newChunk NewChunkFunc, chunkFormat string) error {
	log := trace.FromContext(ctx).WithPrefix("encode")

	// Handle the actual size of the input data, which may be less than a full chunk
	chunkDataBytes := len(chunkData)
	log.Debugf("Chunk %d: processing %d bytes of data", chunkNumber, chunkDataBytes)

	// Keep one contiguous payload per collection rather than millions of
	// combination strings, maps, slice headers, and separate tiny allocations.
	// Encode bounds chunkDataBytes so this product is <= outputChunkBytes.
	payloadBytes := chunkDataBytes * p.PermutationCount
	payloads := make([][]byte, p.TotalCopies)
	for i := range payloads {
		if err := ctx.Err(); err != nil {
			return err
		}
		payloads[i] = make([]byte, payloadBytes)
		// In a collection's sorted combinations, those with an earlier first
		// letter come first. These entries hold random pads; the remaining
		// entries hold the XOR result. Fill only the pad prefix, in one read,
		// assigning a distinct range of fresh bytes to every pad.
		xorPieces := combinationCount(p.TotalCopies-i-1, p.RequiredCopies-1)
		padBytes := (p.PermutationCount - xorPieces) * chunkDataBytes
		if padBytes > 0 {
			if err := randomSource.Read(ctx, payloads[i][:padBytes]); err != nil {
				return fmt.Errorf("random generator error: %w", err)
			}
		}
	}

	var positions [26]int
	var indexes [26]int
	combination := indexes[:p.RequiredCopies]
	for i := range combination {
		combination[i] = i
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		first := combination[0]
		cipher := payloads[first][positions[first] : positions[first]+chunkDataBytes]
		copy(cipher, chunkData)
		positions[first] += chunkDataBytes
		for _, index := range combination[1:] {
			pad := payloads[index][positions[index] : positions[index]+chunkDataBytes]
			for j := range cipher {
				cipher[j] ^= pad[j]
			}
			positions[index] += chunkDataBytes
		}
		if !nextCombination(combination, p.TotalCopies) {
			break
		}
	}

	// Distribute the chunk across all collections
	for index, collName := range p.Collections {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Create a new chunk writer for this collection
		w, err := newChunk(collName, chunkNumber, chunkFormat)
		if err != nil {
			return fmt.Errorf("failed to create chunk writer for collection %s: %w", collName, err)
		}

		// Generate the chunk name
		chunkName := buildChunkName(collName, chunkNumber, chunkDataBytes, backupID)
		log.Debugf("Chunk %d: processing collection %s", chunkNumber, collName)

		err = func() (writeErr error) {
			// Buffered writers persist the chunk in Close. Close exactly once on
			// every path, and preserve both write and close errors if both fail.
			defer func() {
				if err := w.Close(); err != nil {
					writeErr = errors.Join(writeErr, fmt.Errorf("failed to close chunk %d for collection %s: %w", chunkNumber, collName, err))
				}
			}()

			// Write the chunk name to the chunk.
			nameHeader := []byte{byte(len(chunkName))}
			nameHeader = append(nameHeader, []byte(chunkName)...)
			if n, err := w.Write(nameHeader); err != nil {
				return fmt.Errorf("failed to write chunk header for collection %s: %w", collName, err)
			} else if n != len(nameHeader) {
				return fmt.Errorf("failed to write chunk header for collection %s: %w", collName, io.ErrShortWrite)
			}

			// Payload order is the same sorted combination order used by
			// existing backups and decoders.
			if n, err := w.Write(payloads[index]); err != nil {
				return fmt.Errorf("failed to write chunk data for collection %s: %w", collName, err)
			} else if n != len(payloads[index]) {
				return fmt.Errorf("failed to write chunk data for collection %s: %w", collName, io.ErrShortWrite)
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}

	log.Infof("chunk %d completed successfully", chunkNumber)
	return nil
}

// Decode performs the one-time pad decoding process to reconstruct the original data.
//
// This method takes K or more collection readers and reconstructs the original data
// using the threshold scheme properties. It requires at least K collections to work;
// with fewer collections, no information about the original data can be recovered.
//
// Parameters:
//   - ctx: Context for logging, cancellation, and tracing
//   - collections: Slice of io.Readers in any order, each providing data from one collection
//     (must provide at least RequiredCopies distinct collections)
//   - output: Writer where the reconstructed original data will be written
//
// Process:
//  1. Verify that enough collections are provided (at least K)
//  2. Read chunks sequentially from each collection
//  3. For each chunk:
//     a. Read and validate chunk headers and names
//     b. Decode the chunk data using the threshold scheme
//     c. Write the decoded data to the output
//
// Security considerations:
//   - Attempting to decode with fewer than K collections will fail completely
//   - The collection readers must provide data from the same encoding operation
//   - Chunk numbers, collection names, and end-of-input positions are verified for consistency
//   - Duplicates count once and must have matching metadata and payloads in every chunk
//   - The decoding process is deterministic and will produce the exact original data
func (p *Pad) Decode(ctx context.Context, collections []io.Reader, output io.Writer) error {
	log := trace.FromContext(ctx).WithPrefix("decode")

	log.Debugf("Starting decode with %d collections", len(collections))
	if len(collections) == 0 {
		return fmt.Errorf("no collections to decode")
	}

	// Create a structure to track collection state
	type collectionState struct {
		reader          io.Reader
		nextChunkNumber int
		collectionName  string
	}
	type collectionChunk struct {
		collectionLetter string
		data             []byte
		inputIndex       int
	}

	states := make([]collectionState, len(collections))
	for i, reader := range collections {
		states[i] = collectionState{
			reader:          reader,
			nextChunkNumber: 1, // Start at chunk 1
		}
	}

	// We need to reinitialize the pad when we get some real data
	padReinitialized := false
	var backup backupIdentity
	var duplicateBuffer []byte

	// Read chunks until we've processed all available chunks in all collections
	for chunkIndex := 1; ; chunkIndex++ {
		// For each collection, read the next chunk
		var chunkDataBytes int
		var sizeCollection string
		chunks := make([]collectionChunk, 0, min(len(collections), 26))
		var chunkPositions [26]int // First chunk position plus one, indexed by letter.
		var endedCollections []string

		for i, state := range states {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Read the chunk name
			chunkName, err := readChunkName(state.reader)
			if err == io.EOF {
				if state.nextChunkNumber == 1 {
					return fmt.Errorf("collection %d contains no encoded chunks", i+1)
				}
				// No more chunks in this collection
				log.Debugf("Collection %d is done (EOF)", i)
				endedCollections = append(endedCollections, state.collectionName)
				continue
			}
			if err != nil {
				return err
			}
			log.Debugf("Collection %d: Chunk name: %s", i, chunkName)

			// Parse the collection name and chunk number from the chunk name
			collName, chunkNum, declaredDataBytes, backupID, err := extractFromChunkName(chunkName)
			if err != nil {
				return fmt.Errorf("invalid chunk header %q: %w", chunkName, err)
			}
			requiredCopies, totalCopies, collLetter, err := extractFromCollectionLabel(collName)
			if err != nil {
				return fmt.Errorf("invalid collection label in chunk %q: %w", chunkName, err)
			}
			if err := backup.check(backupID, collName, chunkNum); err != nil {
				return err
			}

			if !padReinitialized && len(collections) < requiredCopies {
				return fmt.Errorf("not enough copies to decode: %d < %d", len(collections), requiredCopies)
			}

			// If this is the first chunk, initialize the collection name
			if states[i].collectionName == "" {
				states[i].collectionName = collName
				log.Debugf("Collection %d: Initialized collection name: %s", i, collName)
			} else if states[i].collectionName != collName {
				return fmt.Errorf("collection name mismatch: expected %s, got %s",
					states[i].collectionName, collName)
			}

			// Verify the copies
			if padReinitialized && requiredCopies != p.RequiredCopies {
				return fmt.Errorf("required copies mismatch: expected %d, got %d",
					p.RequiredCopies, requiredCopies)
			}
			if padReinitialized && totalCopies != p.TotalCopies {
				return fmt.Errorf("total copies mismatch: expected %d, got %d",
					p.TotalCopies, totalCopies)
			}

			// Verify the chunk number
			if chunkNum != states[i].nextChunkNumber {
				log.Debugf("Collection %d: Chunk number mismatch: expected %d, got %d",
					i, states[i].nextChunkNumber, chunkNum)
				return chunkNumberMismatch(state.reader, i, collName, states[i].nextChunkNumber, chunkNum)
			}
			states[i].nextChunkNumber++

			// Check the untrusted size before multiplying it. Collection labels
			// have already validated the K/N bounds.
			permutationCount := collectionPermutationCount(totalCopies, requiredCopies)
			if declaredDataBytes > int(^uint(0)>>1)/permutationCount {
				return fmt.Errorf("invalid chunk size in %q: %d bytes with %d permutations overflows int",
					chunkName, declaredDataBytes, permutationCount)
			}
			// Compare every supplied collection before selecting the decode subset.
			// Reset the reference each round to allow a shorter final chunk.
			if chunkDataBytes == 0 {
				chunkDataBytes = declaredDataBytes
				sizeCollection = collName
			} else if declaredDataBytes != chunkDataBytes {
				return fmt.Errorf("chunk %d size mismatch: collection %s declares %d bytes, collection %s declares %d bytes",
					chunkNum, sizeCollection, chunkDataBytes, collName, declaredDataBytes)
			}
			readLength := chunkDataBytes * permutationCount

			letterIndex := int(collLetter[0] - 'A')
			if position := chunkPositions[letterIndex]; position != 0 {
				// Compare every payload byte, including pieces outside the selected
				// K-of-N combination. A public backup ID does not prove equality.
				if duplicateBuffer == nil {
					duplicateBuffer = make([]byte, 32*1024)
				}
				first := chunks[position-1]
				equal, err := compareDuplicatePayload(ctx, state.reader, first.data, duplicateBuffer)
				if err != nil {
					return fmt.Errorf("failed to read duplicate collection %s chunk %d (input %d): %w", collName, chunkNum, i+1, err)
				}
				if !equal {
					return fmt.Errorf("conflicting duplicate collection %s at chunk %d: inputs %d and %d have different payloads", collName, chunkNum, first.inputIndex+1, i+1)
				}
				continue
			}

			// Grow the buffer only as payload arrives, rather than allocating the
			// claimed length up front. LimitReader preserves the next chunk header.
			log.Debugf("Collection %d: Reading %d bytes of chunk data for %d byte chunk", i, readLength, chunkDataBytes)
			chunk, err := io.ReadAll(io.LimitReader(state.reader, int64(readLength)))
			if err != nil {
				return fmt.Errorf("failed to read chunk %q data: %w", chunkName, err)
			}
			if len(chunk) != readLength {
				return fmt.Errorf("failed to read chunk %q data: expected %d bytes, got %d: %w",
					chunkName, readLength, len(chunk), io.ErrUnexpectedEOF)
			}

			// Adopt the backup's parameters only after its first complete payload.
			if !padReinitialized {
				if err := PadInit(ctx, p, totalCopies, requiredCopies); err != nil {
					return fmt.Errorf("invalid pad parameters in chunk %q: %w", chunkName, err)
				}
				padReinitialized = true
				log.Debugf("Pad initialized with totalCopies:%d requiredCopies:%d", p.TotalCopies, p.RequiredCopies)
			}
			chunkPositions[letterIndex] = len(chunks) + 1
			chunks = append(chunks, collectionChunk{collectionLetter: collLetter, data: chunk, inputIndex: i})
			log.Debugf("Collection %d: Read %d bytes of chunk data", i, len(chunk))
		}

		// Successful completion requires every supplied collection to end at
		// the same chunk boundary. A shorter collection is incomplete, even
		// when enough other collections remain to meet the threshold.
		if len(endedCollections) == len(states) {
			log.Debugf("All collections have been fully processed")
			return nil
		}
		if len(endedCollections) > 0 {
			return fmt.Errorf("incomplete collections %s: missing chunk %d while other collections still contain data: %w",
				strings.Join(endedCollections, ", "), chunkIndex, io.ErrUnexpectedEOF)
		}

		// Select the first K distinct collections alphabetically, keeping each label paired
		// with its data. Reader states stay in input order for the next chunk.
		if len(chunks) < p.RequiredCopies {
			return fmt.Errorf("not enough distinct collections to decode: %d < %d (%d inputs supplied)", len(chunks), p.RequiredCopies, len(collections))
		}
		sort.Slice(chunks, func(i, j int) bool {
			return chunks[i].collectionLetter < chunks[j].collectionLetter
		})
		chunks = chunks[:p.RequiredCopies]
		chunkLetters := make([]string, len(chunks))
		for i, chunk := range chunks {
			chunkLetters[i] = chunk.collectionLetter
		}
		permutation := strings.Join(chunkLetters, "")
		log.Debugf("Permutation %s will be used for decode", permutation)

		// Generate the final data
		decodedChunk := make([]byte, chunkDataBytes)
		for _, chunk := range chunks {
			permIndex, err := collectionCombinationIndex(p.TotalCopies, permutation, chunk.collectionLetter)
			if err != nil {
				return err
			}
			log.Debugf("Collection %s: XORing data from permutation %d for %s", chunk.collectionLetter, permIndex, permutation)

			// Add defensive check to ensure chunks have expected size
			permBase := permIndex * chunkDataBytes
			if len(chunk.data) < permBase+chunkDataBytes {
				log.Error(fmt.Errorf("chunk data is truncated: expected at least %d bytes, but only have %d bytes",
					permBase+chunkDataBytes, len(chunk.data)))
				return fmt.Errorf("chunk data truncated in collection %s - possible corruption detected", chunk.collectionLetter)
			}

			// Debugging information to trace chunk XOR operations
			log.Debugf("XORing chunk data: collection=%s, permBase=%d, chunkDataBytes=%d, chunkSize=%d",
				chunk.collectionLetter, permBase, chunkDataBytes, len(chunk.data))

			// Perform the XOR operation with boundary checks
			for j := 0; j < chunkDataBytes; j++ {
				if permBase+j >= len(chunk.data) {
					log.Error(fmt.Errorf("buffer overflow during XOR at index %d (max: %d)",
						permBase+j, len(chunk.data)-1))
					return fmt.Errorf("buffer overflow during XOR operation - corrupt or incomplete collection")
				}
				decodedChunk[j] = decodedChunk[j] ^ chunk.data[permBase+j]
			}
		}

		// Write the decoded data to the output
		log.Debugf("chunk: %d bytes of decoded data written to output", len(decodedChunk))
		_, err := output.Write(decodedChunk)
		if err != nil {
			return fmt.Errorf("failed to write decoded data: %w", err)
		}

	}
}
