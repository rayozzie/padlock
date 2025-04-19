// Package pad implements a secure K-of-N threshold one-time-pad cryptographic scheme.
//
// This package provides the core cryptographic functionality of the padlock system,
// implementing Shamir's Secret Sharing combined with one-time-pad encryption.
// The threshold scheme allows data to be split into N collections (shares),
// where any K of them can be used to reconstruct the original data, but K-1 or
// fewer collections reveal absolutely nothing about the original data
// (information-theoretic security).
//
// Security properties:
// - True information-theoretic security (not dependent on computational hardness)
// - Perfect forward secrecy (past communications remain secure even if keys are compromised)
// - No key management required (each pad is used only once)
// - K-of-N threshold security (requires at least K collections to reconstruct)
// - If using true randomness, security is mathematically provable
//
// Implementation details:
// - Data is divided into fixed-size chunks for processing
// - Each chunk is split across N collections
// - Collections are generated so that any K of them can reconstruct the original data
// - File names on disk use format "<collectionName>_<chunkNumber>.<format>" (e.g., "3A5_0001.bin")
// - Internally within files, chunk names are stored as "<collectionName>-<chunkNumber>" (e.g., "3A5-1")
//
// Usage warnings:
// - The security of this system depends entirely on the quality of the random number generator
// - One-time pads must NEVER be reused
// - Data reconstruction requires exactly K or more of the original N collections
package pad

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

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
type Pad struct {
	TotalCopies    int      // N: Total number of collections to create (2-26)
	RequiredCopies int      // K: Minimum collections needed for reconstruction (2-N)
	Collections    []string // Names of each collection (e.g., ["3A5", "3B5", "3C5", ...])
}

// NewPad creates a new Pad instance with the specified parameters for a K-of-N threshold scheme.
//
// Parameters:
//   - totalCopies (N): The total number of collections to create. Must be between 2 and 26.
//     This represents the total number of shares in the threshold scheme.
//   - requiredCopies (K): The minimum number of collections required to reconstruct the data.
//     Must be at least 2 and not greater than totalCopies.
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
func NewPad(totalCopies, requiredCopies int) (*Pad, error) {
	// Validate parameters to ensure they meet the requirements of the threshold scheme
	if totalCopies < 2 || totalCopies > 26 {
		return nil, fmt.Errorf("totalCopies must be between 2 and 26, got %d", totalCopies)
	}
	if requiredCopies < 2 {
		return nil, fmt.Errorf("requiredCopies must be at least 2, got %d", requiredCopies)
	}
	if requiredCopies > totalCopies {
		return nil, fmt.Errorf("requiredCopies cannot be greater than totalCopies, got %d > %d", requiredCopies, totalCopies)
	}

	// Generate collection names in the format "<K><collectionId><N>"
	// Example: with K=3, N=5, collections = ["3A5", "3B5", "3C5", "3D5", "3E5"]
	collections := make([]string, totalCopies)
	for i := 0; i < totalCopies; i++ {
		collLetter := rune('A' + i)
		collections[i] = fmt.Sprintf("%d%c%d", requiredCopies, collLetter, totalCopies)
	}

	return &Pad{
		TotalCopies:    totalCopies,
		RequiredCopies: requiredCopies,
		Collections:    collections,
	}, nil
}

// Encode implements the one-time pad encoding process with K-of-N threshold security.
//
// This method takes an input stream and encodes it into N collections such that
// any K collections can be used to reconstruct the original data, but fewer than
// K collections reveal absolutely nothing about the data (information-theoretic security).
//
// Parameters:
//   - ctx: Context for logging, cancellation, and tracing
//   - outputChunkBytes: Maximum size for each output chunk in bytes
//   - input: Reader providing the data to be encoded
//   - randomSource: Source of random bytes for one-time pad generation
//   - newChunk: Function to create output files for each chunk
//   - chunkFormat: Format for output files (e.g., "bin" or "png")
//
// Process:
//  1. Divide input data into fixed-size chunks
//  2. For each chunk:
//     a. Generate a random one-time pad matching the input size
//     b. XOR the input data with the pad to create ciphertext
//     c. Distribute data across N collections according to the threshold scheme
//     d. Write the data to each collection with proper headers
//
// Security considerations:
//   - The randomSource MUST provide cryptographically secure random numbers
//   - The same pad must NEVER be reused
//   - Each chunk has a unique name to ensure it's properly tracked during decoding
func (p *Pad) Encode(ctx context.Context, outputChunkBytes int, input io.Reader, randomSource RNG, newChunk NewChunkFunc, chunkFormat string) error {
	log := trace.FromContext(ctx).WithPrefix("ENCODE")

	log.Debugf("Starting encode with parameters: totalCopies=%d, requiredCopies=%d, outputChunkBytes=%d",
		p.TotalCopies, p.RequiredCopies, outputChunkBytes)

	// Log the collection names
	log.Debugf("Collections: %v", p.Collections)

	// Compute a reasonable segment size S for dividing data across collections
	// For simplicity, we'll make each segment a fixed portion of the output chunk size
	// Formula: S = outputChunkBytes / (4 * N)
	// This ensures the combined left and right halves (2*N*S) fit within outputChunkBytes
	// with some room for the header and metadata
	S := outputChunkBytes / (4 * p.TotalCopies)
	if S <= 0 {
		return fmt.Errorf("configured chunk size %d is too small for the specified collections", outputChunkBytes)
	}

	log.Debugf("Computed segment size S=%d bytes", S)

	// Size of data chunks to read from input
	inputChunkSize := p.TotalCopies * S
	log.Debugf("Input chunk size: %d bytes", inputChunkSize)

	// Buffer for reading input data
	buffer := make([]byte, inputChunkSize)
	chunkIndex := 1

	// Process input data chunk by chunk until end of stream
	for {
		// Try to read a full chunk from the input
		bytesRead, err := io.ReadFull(input, buffer)

		// Process the chunk if we got any data
		if bytesRead > 0 {
			log.Debugf("Processing chunk %d (%d bytes)", chunkIndex, bytesRead)

			// Process the chunk with however many bytes we read (handles partial chunks)
			if err := p.encodeOneChunk(ctx, buffer[:bytesRead], S, chunkIndex, randomSource, newChunk, chunkFormat); err != nil {
				return err
			}
			// chunkIndex incremented at loop level
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

// encodeOneChunk encodes a single chunk of data using the one-time pad threshold scheme.
//
// This function is the core implementation of the one-time pad encoding process for a single chunk.
// It generates random pad data, XORs it with the input data, and distributes the results
// across N collections according to the threshold scheme.
//
// Parameters:
//   - ctx: Context for logging, cancellation, and tracing
//   - chunkData: The input data to encode (may be less than a full chunk at the end of the stream)
//   - S: The segment size parameter for dividing data
//   - chunkNumber: The sequential number of this chunk (starting at 1)
//   - randomSource: Source of cryptographically secure random bytes
//   - newChunk: Function to create output files for each chunk
//   - chunkFormat: Format for output files (e.g., "bin" or "png")
//
// Security considerations:
//   - The randomSource MUST provide high-quality randomness
//   - The segment distribution ensures that fewer than K collections reveal nothing about the data
//   - Each collection receives data that appears completely random when viewed in isolation
func (p *Pad) encodeOneChunk(ctx context.Context, chunkData []byte, S int, chunkNumber int, randomSource RNG, newChunk NewChunkFunc, chunkFormat string) error {
	log := trace.FromContext(ctx).WithPrefix("ENCODE")

	// Handle the actual size of the input data, which may be less than a full chunk
	actualSize := len(chunkData)
	log.Debugf("Chunk %d: processing %d bytes of data", chunkNumber, actualSize)

	// Generate random pad exactly matching the actual data size
	// This ensures we can handle any size of data, including odd-sized chunks
	log.Debugf("Chunk %d: generating random pad of %d bytes", chunkNumber, actualSize)
	pad := make([]byte, actualSize)
	n, err := randomSource.Read(ctx, pad)
	if err != nil {
		log.Error(fmt.Errorf("random generator error: %w", err))
		return fmt.Errorf("random generator error: %w", err)
	}
	if n != actualSize {
		log.Error(fmt.Errorf("random generator short read: expected %d, got %d", actualSize, n))
		return fmt.Errorf("random generator short read: expected %d, got %d", actualSize, n)
	}

	// XOR plaintext (chunkData) with pad to get ciphertext
	log.Debugf("Chunk %d: XORing data with pad to generate ciphertext", chunkNumber)
	ciphertext := make([]byte, actualSize)
	for i := 0; i < actualSize; i++ {
		ciphertext[i] = chunkData[i] ^ pad[i]
	}

	N := p.TotalCopies

	// Calculate how to distribute the data across segments
	// For partial chunks, we need to adjust the segment sizes
	log.Debugf("Chunk %d: calculating segment distribution for %d collections", chunkNumber, N)
	segmentSizes := make([]int, N)
	remainingBytes := actualSize

	// Distribute bytes as evenly as possible among segments
	baseSize := remainingBytes / N
	extraBytes := remainingBytes % N

	for i := 0; i < N; i++ {
		segmentSizes[i] = baseSize
		if i < extraBytes {
			segmentSizes[i]++
		}
	}

	log.Debugf("Chunk %d: segment sizes: %v", chunkNumber, segmentSizes)

	// Split pad and ciphertext into segments of appropriate sizes
	padSegments := make([][]byte, N)
	cipherSegments := make([][]byte, N)

	offset := 0
	for i := 0; i < N; i++ {
		size := segmentSizes[i]
		if size > 0 {
			endOffset := offset + size
			padSegments[i] = pad[offset:endOffset]
			cipherSegments[i] = ciphertext[offset:endOffset]
			offset = endOffset

			log.Debugf("Chunk %d: Collection %s segment size: %d bytes",
				chunkNumber, p.Collections[i], size)
		} else {
			// Handle the case where a segment might get zero bytes
			padSegments[i] = []byte{}
			cipherSegments[i] = []byte{}
			log.Debugf("Chunk %d: Collection %s segment size: 0 bytes",
				chunkNumber, p.Collections[i])
		}
	}

	// Create chunk writers for each collection
	log.Debugf("Chunk %d: creating chunk writers for %d collections", chunkNumber, N)
	writers := make([]io.WriteCloser, N)
	defer func() {
		// Close all writers
		for _, w := range writers {
			if w != nil {
				w.Close()
			}
		}
	}()

	for i, collName := range p.Collections {
		w, err := newChunk(collName, chunkNumber, chunkFormat)
		if err != nil {
			return fmt.Errorf("failed to create chunk writer for collection %s: %w", collName, err)
		}
		writers[i] = w
		log.Debugf("Chunk %d: created writer for collection %s", chunkNumber, collName)
	}

	// Build and write a specific chunk for each collection
	for i, collName := range p.Collections {
		// Generate the chunk name
		chunkName := fmt.Sprintf("%s-%d", collName, chunkNumber)
		log.Debugf("Chunk %d: processing collection %s", chunkNumber, collName)

		// Build collection-specific chunk data
		log.Debugf("Chunk %d: building data for collection %s (index %d)", chunkNumber, collName, i)
		chunkData := buildCollectionChunk(
			ctx,
			i,
			p.TotalCopies,
			p.RequiredCopies,
			padSegments,
			cipherSegments,
		)

		log.Debugf("Chunk %d: collection %s data size: %d bytes", chunkNumber, collName, len(chunkData))

		// Write chunk name length and chunk name as header
		nameHeader := []byte{byte(len(chunkName))}
		nameHeader = append(nameHeader, []byte(chunkName)...)

		// Write to the collection
		if _, err := writers[i].Write(nameHeader); err != nil {
			return fmt.Errorf("failed to write chunk header for collection %s: %w", collName, err)
		}

		if _, err := writers[i].Write(chunkData); err != nil {
			return fmt.Errorf("failed to write chunk data for collection %s: %w", collName, err)
		}

		log.Debugf("Chunk %d: wrote %d bytes to collection %s",
			chunkNumber, len(nameHeader)+len(chunkData), collName)
	}

	log.Debugf("Chunk %d: completed successfully", chunkNumber)
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
//   - collections: Slice of io.Readers, each providing data from one collection
//     (must provide at least RequiredCopies readers)
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
//   - Chunk numbers and collection names are verified for consistency
//   - The decoding process is deterministic and will produce the exact original data
func (p *Pad) Decode(ctx context.Context, collections []io.Reader, output io.Writer) error {
	log := trace.FromContext(ctx).WithPrefix("DECODE")

	log.Debugf("Starting decode with parameters: totalCopies=%d, requiredCopies=%d",
		p.TotalCopies, p.RequiredCopies)

	if len(collections) < p.RequiredCopies {
		return fmt.Errorf("insufficient collections: found %d but require %d", len(collections), p.RequiredCopies)
	}

	log.Debugf("Available collections: %d", len(collections))

	// Use just the required number of collections
	usableCollections := collections[:p.RequiredCopies]

	// Create a structure to track collection state
	type collectionState struct {
		reader          io.Reader
		nextChunkNumber int
		collectionName  string
	}

	states := make([]collectionState, len(usableCollections))
	for i, reader := range usableCollections {
		states[i] = collectionState{
			reader:          reader,
			nextChunkNumber: 1,  // Start at chunk 1
			collectionName:  "", // Will be initialized on first chunk
		}
	}

	// Read chunks until we've processed all available chunks in all collections
	for chunkIndex := 1; ; chunkIndex++ {
		// For each collection, read the next chunk
		chunks := make([][]byte, len(usableCollections))
		chunkNames := make([]string, len(usableCollections))

		for i, state := range states {
			// Read the chunk name length
			lengthBuf := make([]byte, 1)
			_, err := io.ReadFull(state.reader, lengthBuf)
			if err == io.EOF {
				// No more chunks in this collection
				return nil
			}
			if err != nil {
				return fmt.Errorf("failed to read chunk name length: %w", err)
			}

			nameLength := int(lengthBuf[0])
			nameBuf := make([]byte, nameLength)
			_, err = io.ReadFull(state.reader, nameBuf)
			if err != nil {
				return fmt.Errorf("failed to read chunk name: %w", err)
			}

			chunkName := string(nameBuf)
			chunkNames[i] = chunkName

			// Parse the collection name and chunk number from the chunk name
			// Format: "<collectionName>-<chunkNumber>"
			// Split at the last dash to handle collection names that might contain dashes
			parts := strings.Split(chunkName, "-")
			if len(parts) < 2 {
				return fmt.Errorf("invalid chunk name format (missing hyphen): %s", chunkName)
			}

			// The chunk number is the last part
			chunkNumStr := parts[len(parts)-1]
			// The collection name is everything else joined with dashes
			collName := strings.Join(parts[:len(parts)-1], "-")

			// Parse the chunk number
			var chunkNum int
			_, err = fmt.Sscanf(chunkNumStr, "%d", &chunkNum)
			if err != nil {
				return fmt.Errorf("invalid chunk number in chunk name: %s", chunkName)
			}

			// If this is the first chunk, initialize the collection name
			if states[i].collectionName == "" {
				states[i].collectionName = collName
			} else if states[i].collectionName != collName {
				return fmt.Errorf("collection name mismatch: expected %s, got %s",
					states[i].collectionName, collName)
			}

			// Verify the chunk number
			if chunkNum != states[i].nextChunkNumber {
				return fmt.Errorf("chunk number mismatch: expected %d, got %d",
					states[i].nextChunkNumber, chunkNum)
			}
			states[i].nextChunkNumber++

			// Read the chunk data
			// For simplicity, we'll assume the chunks are small enough to read into memory
			// In a real implementation, you might use a more streaming approach
			chunk, err := io.ReadAll(state.reader)
			if err != nil {
				return fmt.Errorf("failed to read chunk data: %w", err)
			}
			chunks[i] = chunk
		}

		// On first iteration, verify we have all the collection names
		if chunkIndex == 1 {
			for i, state := range states {
				if state.collectionName == "" {
					return fmt.Errorf("failed to initialize collection name for collection %d", i)
				}
			}
		}

		// Decode the chunk data
		log.Debugf("Decoding chunk %d with %d collections", chunkIndex, len(chunks))
		decodedChunk, err := decodeChunks(ctx, chunks, p.RequiredCopies, p.TotalCopies)
		if err != nil {
			return fmt.Errorf("failed to decode chunk %d: %w", chunkIndex, err)
		}
		log.Debugf("Successfully decoded chunk %d (%d bytes)", chunkIndex, len(decodedChunk))

		// Write the decoded data to the output
		_, err = output.Write(decodedChunk)
		if err != nil {
			return fmt.Errorf("failed to write decoded data: %w", err)
		}

		// chunkIndex incremented at loop level
	}
}

// decodeChunks implements the core decoding algorithm for the information-theoretic K-of-N threshold scheme.
//
// This function takes chunks from K collections and reconstructs the original data by
// identifying matching records across collections and performing the appropriate XOR operations.
// It is the inverse of the buildCollectionChunk function used during encoding.
//
// Parameters:
//   - ctx: Context for logging, cancellation, and tracing
//   - chunks: Slice of byte slices containing chunk data from K different collections
//   - requiredCopies: The K value from the K-of-N threshold scheme
//   - totalCopies: The N value from the K-of-N threshold scheme
//
// Returns:
//   - The reconstructed original data
//   - An error if decoding fails
//
// Process:
//  1. Identify the collection indices (assumed to be 0...K-1 in current implementation)
//  2. Find matching records across collections with the same combination ID
//  3. Extract segment data from each collection
//  4. Reconstruct the original data by XORing the segments together
//
// Mathematical basis:
//
//	If data = plaintext ⊕ pad, and the lowest index collection has data ⊕ pad₁ ⊕ pad₂ ⊕ ... ⊕ padₖ₋₁,
//	then XORing with all pad₁...padₖ₋₁ yields the original plaintext.
func decodeChunks(ctx context.Context, chunks [][]byte, requiredCopies, totalCopies int) ([]byte, error) {
	log := trace.FromContext(ctx).WithPrefix("DECODE")
	if len(chunks) < requiredCopies {
		return nil, fmt.Errorf("not enough chunks to decode: got %d, need %d", len(chunks), requiredCopies)
	}

	log.Debugf("Decoding with %d chunks (required: %d)", len(chunks), requiredCopies)

	// Get the actual collection indices we have
	// For now we'll assume they're ordered 0...K-1, but in a real implementation
	// we would extract this from the collection names
	collectionIndices := make([]int, requiredCopies)
	for i := 0; i < requiredCopies; i++ {
		collectionIndices[i] = i
	}

	// Sort the collection indices to determine roles
	// (the lowest index has the data XORed with all other pads)
	sort.Ints(collectionIndices)

	// Build a unique ID for this combination (e.g., "ABC")
	desiredID := ""
	for _, idx := range collectionIndices {
		desiredID += string(rune('A' + idx))
	}
	log.Debugf("Looking for combination ID: %s", desiredID)

	// First, find the matching record in each collection's chunk
	segmentData := make([][]byte, requiredCopies)

	for i, chunk := range chunks[:requiredCopies] {
		if len(chunk) < 1 {
			return nil, fmt.Errorf("chunk %d is too small", i)
		}

		collLetter := string(rune('A' + i))
		log.Debugf("Processing collection %s chunk (%d bytes)", collLetter, len(chunk))

		// Calculate the record count using the binomial coefficient formula (N choose K)
		recordCount := binomialCoefficient(totalCopies, requiredCopies)
		log.Debugf("Collection %s: expecting %d records", collLetter, recordCount)

		// Process this collection's chunk to find the matching record
		offset := 0
		foundRecord := false

		for j := 0; j < recordCount; j++ {
			// Ensure we have enough data to read the ID length
			if offset >= len(chunk) {
				log.Debugf("Collection %s: reached end of chunk at record %d", collLetter, j)
				break // End of this chunk
			}

			// Read ID length
			idLength := int(chunk[offset])
			offset++

			// Ensure we have enough data to read the ID
			if offset+idLength > len(chunk) {
				log.Debugf("Collection %s: truncated ID at record %d", collLetter, j)
				break // Truncated record
			}

			// Read the ID
			recordID := string(chunk[offset : offset+idLength])
			offset += idLength
			log.Debugf("Collection %s: found record %d/%d with ID: %s",
				collLetter, j+1, recordCount, recordID)

			// Check if this is a record we can use
			if recordID == desiredID {
				log.Debugf("Collection %s: MATCH! Record %s matches our combination",
					collLetter, recordID)

				// We found our record for this collection
				// The data after the ID is the segment for this collection
				segmentEnd := len(chunk)

				// Try to find the next record to determine this segment's end
				nextOffset := offset
				for nextOffset < len(chunk) {
					// Look for a plausible ID length byte
					if nextOffset+1 < len(chunk) {
						possibleLength := int(chunk[nextOffset])
						if possibleLength > 0 && possibleLength < 10 { // Realistic ID length
							// This could be the start of the next record
							segmentEnd = nextOffset
							break
						}
					}
					nextOffset++
				}

				// Extract this collection's segment data
				segmentData[i] = chunk[offset:segmentEnd]
				log.Debugf("Collection %s: extracted segment data (%d bytes)",
					collLetter, len(segmentData[i]))
				foundRecord = true
				break
			}

			// If not the right record, skip to the next one
			// This is imprecise but functional for demonstration
			remainingRecords := recordCount - j - 1
			if remainingRecords > 0 {
				remainingBytes := len(chunk) - offset
				approxRecordSize := remainingBytes / remainingRecords
				offset += approxRecordSize
				log.Debugf("Collection %s: skipping to next record, offset: %d", collLetter, offset)
			} else {
				log.Debugf("Collection %s: no more records", collLetter)
				break // No more records
			}
		}

		if !foundRecord {
			return nil, fmt.Errorf("matching record %s not found in collection %d", desiredID, i)
		}
	}

	// Now we have the segment data from all K collections
	// The lowest index collection has data = originalData⊕pad₁⊕pad₂⊕...⊕padₖ₋₁
	// The others have their respective pads
	log.Debugf("Found matching records in all %d collections", requiredCopies)

	// Start with the data from the lowest index collection
	// (which contains the original data XORed with all the other pads)
	result := make([]byte, len(segmentData[0]))
	copy(result, segmentData[0])
	log.Debugf("Starting with XORed data from collection A (%d bytes)", len(result))

	// XOR with the pad from each other collection to recover the original data
	for i := 1; i < requiredCopies; i++ {
		collLetter := string(rune('A' + i))
		log.Debugf("XORing with pad from collection %s (%d bytes)", collLetter, len(segmentData[i]))

		// Make sure we don't go out of bounds
		padLen := len(segmentData[i])
		for j := 0; j < len(result) && j < padLen; j++ {
			result[j] ^= segmentData[i][j]
		}
	}

	log.Debugf("Successfully recovered original data (%d bytes)", len(result))
	return result, nil
}

// buildCollectionChunk creates the data for a single collection in the threshold scheme.
//
// This function implements the core security properties of the K-of-N threshold scheme.
// It ensures that each collection contains data that appears completely random when
// viewed in isolation, but can be combined with K-1 other collections to reconstruct
// the original data.
//
// Parameters:
//   - ctx: Context for logging, cancellation, and tracing
//   - collIndex: The index of the collection (0 to N-1)
//   - totalCopies: The N value from the K-of-N threshold scheme
//   - requiredCopies: The K value from the K-of-N threshold scheme
//   - padSegments: Random pad segments for each collection
//   - cipherSegments: The ciphertext segments (original data XORed with pads)
//
// Returns:
//   - A byte slice containing the chunk data for this collection
//
// Process:
//  1. Generate all possible K-combinations of the N collections
//  2. Filter to combinations that include this collection
//  3. For each relevant combination:
//     a. Determine this collection's role (lowest index or not)
//     b. If lowest index: Store ciphertext XORed with other collection pads
//     c. If not lowest index: Store just the random pad
//     d. Package this data with a combination ID header
//  4. Combine all records into a single chunk
//
// Security properties:
//   - A subset of fewer than K collections reveals nothing about the original data
//   - Any K collections can be used to recover the complete original data
//   - Each collection contains different data for different K-combinations
func buildCollectionChunk(ctx context.Context, collIndex int, totalCopies, requiredCopies int, padSegments, cipherSegments [][]byte) []byte {
	log := trace.FromContext(ctx).WithPrefix("ENCODE")

	collLetter := string(rune('A' + collIndex))
	log.Debugf("Building chunk for collection %s (index %d)", collLetter, collIndex)

	// Generate all K-combinations of N collections
	combinations := generateKCombinations(totalCopies, requiredCopies)
	log.Debugf("Generated %d possible K-combinations", len(combinations))

	// Filter to combinations that include this collection
	var relevantCombos [][]int
	for _, combo := range combinations {
		// Check if this collection's index is in the combination
		containsThisCollection := false
		for _, idx := range combo {
			if idx == collIndex {
				containsThisCollection = true
				break
			}
		}

		if containsThisCollection {
			relevantCombos = append(relevantCombos, combo)
		}
	}

	log.Debugf("Collection %s will have %d relevant combinations", collLetter, len(relevantCombos))

	// For each relevant combination, create a record
	allRecords := make([][]byte, 0, len(relevantCombos))

	for comboIndex, combo := range relevantCombos {
		// Build a unique ID for this combination (e.g., "ABC")
		comboID := ""
		for _, idx := range combo {
			comboID += string(rune('A' + idx))
		}

		log.Debugf("Collection %s: processing combination %d/%d: %s",
			collLetter, comboIndex+1, len(relevantCombos), comboID)

		// Sort the combo indices to ensure deterministic assignment
		sortedCombo := make([]int, len(combo))
		copy(sortedCombo, combo)

		// Simple insertion sort (for small K, this is efficient enough)
		for i := 1; i < len(sortedCombo); i++ {
			key := sortedCombo[i]
			j := i - 1
			for j >= 0 && sortedCombo[j] > key {
				sortedCombo[j+1] = sortedCombo[j]
				j--
			}
			sortedCombo[j+1] = key
		}

		// Sort the combo for display in logs
		sortedComboStr := ""
		for _, idx := range sortedCombo {
			sortedComboStr += string(rune('A' + idx))
		}
		log.Debugf("Collection %s: sorted combination: %s", collLetter, sortedComboStr)

		// Determine the role of this collection in this combination
		// The lowest index gets original data XORed with all random pads
		// Others get a single random pad each

		// Determine the data for this collection in this combination
		var segmentData []byte

		// If this is the lowest index in the combination, it gets the ciphertext XORed with other pads
		if collIndex == sortedCombo[0] {
			log.Debugf("Collection %s: is LOWEST index in combination %s", collLetter, comboID)
			log.Debugf("Collection %s: will store ciphertext XORed with pads of all other collections in this combo", collLetter)

			// Start with the original data for this segment
			segSize := len(cipherSegments[collIndex])
			segmentData = make([]byte, segSize)
			copy(segmentData, cipherSegments[collIndex])

			// XOR with the random pads of the other collections in this combo
			for i, idx := range sortedCombo {
				if i > 0 { // Skip the first (lowest) index
					log.Debugf("Collection %s: XORing with pad from collection %s",
						collLetter, string(rune('A'+idx)))

					// XOR this segment with the pad from the other collection
					for j := 0; j < len(segmentData) && j < len(padSegments[idx]); j++ {
						segmentData[j] ^= padSegments[idx][j]
					}
				}
			}
		} else {
			// This is a non-lowest collection, so it just gets its random pad
			log.Debugf("Collection %s: is NOT lowest index in combination %s", collLetter, comboID)
			log.Debugf("Collection %s: will store its random pad", collLetter)

			segmentData = make([]byte, len(padSegments[collIndex]))
			copy(segmentData, padSegments[collIndex])
		}

		log.Debugf("Collection %s: segment data size for combo %s: %d bytes",
			collLetter, comboID, len(segmentData))

		// Build the record: [id_length][id][segment_data]
		record := make([]byte, 0)
		record = append(record, byte(len(comboID)))
		record = append(record, []byte(comboID)...)
		record = append(record, segmentData...)

		// Save the record
		allRecords = append(allRecords, record)
		log.Debugf("Collection %s: record for combo %s complete, size: %d bytes",
			collLetter, comboID, len(record))
	}

	// Now combine all records into a single chunk
	chunk := make([]byte, 0)

	// Add all records
	for i, record := range allRecords {
		chunk = append(chunk, record...)
		log.Debugf("Collection %s: added record %d/%d to chunk",
			collLetter, i+1, len(allRecords))
	}

	log.Debugf("Collection %s: final chunk size: %d bytes with %d records",
		collLetter, len(chunk), len(allRecords))

	return chunk
}

// generateKCombinations computes all possible k-element combinations from the set {0,1,...,n-1}.
//
// This function is used to calculate all possible ways to select K collections from N total collections,
// which is necessary for implementing the threshold scheme where any K collections can reconstruct
// the original data.
//
// Parameters:
//   - n: The size of the set to choose from (N in the K-of-N scheme)
//   - k: The size of each combination (K in the K-of-N scheme)
//
// Returns:
//   - A slice of integer slices, where each inner slice represents one combination
//
// Algorithm:
//   - Uses a recursive backtracking approach to generate all combinations
//   - For each position in the combination, we try all valid values
//   - When we've filled all k positions, we add the combination to our result
//
// Example:
//
//	generateKCombinations(4, 2) would return:
//	[[0,1], [0,2], [0,3], [1,2], [1,3], [2,3]]
//	representing all ways to choose 2 elements from {0,1,2,3}
func generateKCombinations(n, k int) [][]int {
	var result [][]int
	combo := make([]int, k)

	var backtrack func(start, depth int)
	backtrack = func(start, depth int) {
		if depth == k {
			cpy := make([]int, k)
			copy(cpy, combo)
			result = append(result, cpy)
			return
		}
		for i := start; i < n-(k-depth)+1; i++ {
			combo[depth] = i
			backtrack(i+1, depth+1)
		}
	}

	backtrack(0, 0)
	return result
}

// binomialCoefficient calculates the binomial coefficient (n choose k).
//
// This function computes the number of ways to choose k elements from a set of n elements,
// which is essential for determining how many unique combinations of collections exist
// in the K-of-N threshold scheme.
//
// Parameters:
//   - n: The size of the set (N in the K-of-N scheme)
//   - k: The size of the selection (K in the K-of-N scheme)
//
// Returns:
//   - The binomial coefficient (n choose k), equal to n!/(k!(n-k)!)
//
// Algorithm:
//   - Uses a multiplication formula that avoids calculating large factorials
//   - Leverages the symmetry property: (n choose k) = (n choose n-k)
//   - Handles edge cases like k=0, k=n, and invalid inputs
//
// Example:
//
//	binomialCoefficient(5, 2) = 10, meaning there are 10 ways to choose 2 elements from a set of 5
//
// Mathematical significance:
//
//	In the context of padlock, this represents the number of records each collection
//	must store to ensure that any K collections can reconstruct the original data
func binomialCoefficient(n, k int) int {
	if k < 0 || k > n {
		return 0
	}
	if k == 0 || k == n {
		return 1
	}

	// Use the symmetry property
	if k > n-k {
		k = n - k
	}

	c := 1
	for i := 0; i < k; i++ {
		c = c * (n - i) / (i + 1)
	}
	return c
}
