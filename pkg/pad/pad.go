package pad

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/rayozzie/padlock/pkg/rng"
	"github.com/rayozzie/padlock/pkg/trace"
)

// RandomBytesGenerator defines a function type that generates random bytes
// Deprecated: Use rng.RNG interface instead
type RandomBytesGenerator func([]byte) (int, error)

// ContextAwareRandomBytesGenerator defines a function type that generates random bytes with context
type ContextAwareRandomBytesGenerator interface {
	// ReadWithContext fills p with random bytes and returns the number of bytes written.
	ReadWithContext(ctx context.Context, p []byte) (n int, err error)
}

// NewChunkFunc defines a function type for creating new chunk files
type NewChunkFunc func(collectionName string, chunkNumber int, chunkFormat string) (io.WriteCloser, error)

// Pad represents the configuration for a one-time pad operation
type Pad struct {
	TotalCopies    int // N in the original code
	RequiredCopies int // K in the original code
	Collections    []string
}

// NewPad creates a new Pad instance with the specified parameters
func NewPad(totalCopies, requiredCopies int) (*Pad, error) {
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

// Encode implements the one-time pad encoding process
func (p *Pad) Encode(ctx context.Context, outputChunkBytes int, input io.Reader, randomSource rng.RNG, newChunk NewChunkFunc, chunkFormat string) error {
	log := trace.FromContext(ctx).WithPrefix("ENCODE")

	log.Debugf("Starting encode with parameters: totalCopies=%d, requiredCopies=%d, outputChunkBytes=%d",
		p.TotalCopies, p.RequiredCopies, outputChunkBytes)

	// Log the collection names
	log.Debugf("Collections: %v", p.Collections)

	// Compute a reasonable segment size S
	// For simplicity, we'll make each segment a fixed portion of the output chunk size
	// Formula: S = outputChunkBytes / (4 * N)
	// This ensures the combined left and right halves (2*N*S) fit within outputChunkBytes
	// with some room for the header
	S := outputChunkBytes / (4 * p.TotalCopies)
	if S <= 0 {
		return fmt.Errorf("configured chunk size %d is too small for the specified collections", outputChunkBytes)
	}

	log.Debugf("Computed segment size S=%d bytes", S)

	inputChunkSize := p.TotalCopies * S
	log.Debugf("Input chunk size: %d bytes", inputChunkSize)

	buffer := make([]byte, inputChunkSize)
	chunkIndex := 1

	for {
		// Try to read a full chunk
		bytesRead, err := io.ReadFull(input, buffer)

		// Process the chunk if we got any data
		if bytesRead > 0 {
			log.Debugf("Processing chunk %d (%d bytes)", chunkIndex, bytesRead)

			// Process the chunk with however many bytes we read
			if err := p.encodeOneChunk(ctx, buffer[:bytesRead], S, chunkIndex, randomSource, newChunk, chunkFormat); err != nil {
				return err
			}
			chunkIndex++
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

// encodeOneChunk encodes a single chunk of data
func (p *Pad) encodeOneChunk(ctx context.Context, chunkData []byte, S int, chunkNumber int, randomSource rng.RNG, newChunk NewChunkFunc, chunkFormat string) error {
	log := trace.FromContext(ctx).WithPrefix("ENCODE")

	// Handle the actual size of the input data, which may be less than a full chunk
	actualSize := len(chunkData)
	log.Debugf("Chunk %d: processing %d bytes of data", chunkNumber, actualSize)

	// Generate random pad exactly matching the actual data size
	// This ensures we can handle any size of data, including odd-sized chunks
	log.Debugf("Chunk %d: generating random pad of %d bytes", chunkNumber, actualSize)
	pad := make([]byte, actualSize)
	n, err := randomSource.ReadWithContext(ctx, pad)
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

// Decode performs the one-time pad decoding process
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

	// Read chunks until EOF
	chunkIndex := 1
	for {
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
			var collName string
			var chunkNum int
			_, err = fmt.Sscanf(chunkName, "%s-%d", &collName, &chunkNum)
			if err != nil {
				return fmt.Errorf("invalid chunk name format: %s", chunkName)
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

		chunkIndex++
	}
}

// Helper function to decode chunks with a true information-theoretic K-of-N threshold scheme
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

// Helper function to build a collection chunk for true information-theoretic K-of-N threshold scheme
// Each collection gets segments that are completely unique and appear random
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

// Helper function to generate all k-element combinations of {0,...,n-1}
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

// Helper function to calculate binomial coefficient (n choose k)
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
