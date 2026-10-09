// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"fmt"
	"io"
)

const backupIDBytes = 16

// backupIdentity binds all supplied chunks to the first chunk's identifier,
// including chunks from collections beyond the K selected for reconstruction.
// An all-legacy set has empty identifiers; mixing identified and legacy chunks
// is rejected as well. This is a consistency check, not authentication.
type backupIdentity struct {
	set        bool
	id         string
	collection string
}

func (b *backupIdentity) check(id, collection string, chunk int) error {
	if !b.set {
		b.set, b.id, b.collection = true, id, collection
		return nil
	}
	if id != b.id {
		return fmt.Errorf("collections belong to different backups: %s chunk %d has a different backup identifier from %s", collection, chunk, b.collection)
	}
	return nil
}

func readChunkName(reader io.Reader) (string, error) {
	var length [1]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		if err == io.EOF {
			return "", io.EOF
		}
		return "", fmt.Errorf("failed to read chunk name length: %w", err)
	}
	name := make([]byte, int(length[0]))
	if _, err := io.ReadFull(reader, name); err != nil {
		// EOF after the length byte is a truncated header, not a stream end.
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return "", fmt.Errorf("failed to read chunk name length %d: %w", len(name), err)
	}
	return string(name), nil
}

// CheckBackupIDs checks the first chunk header in every collection, including
// matching K/N, enough distinct collection letters, and matching first-chunk
// payloads for duplicates, before a caller prepares the restore destination.
// It returns readers that replay consumed bytes and then continue from the
// original streams. File adapters expose their already buffered chunk; other
// readers need at most one saved first payload per duplicated collection letter.
// The caller
// retains ownership of the underlying readers and must close them as needed.
// Decode also checks every chunk against the first identifier throughout the
// operation. Matching identifiers detect accidental mixing, not tampering.
func CheckBackupIDs(collections []io.Reader) ([]io.Reader, error) {
	if len(collections) == 0 {
		return nil, fmt.Errorf("no collections to decode")
	}
	type firstChunk struct {
		name, collection string
		size             int
		letter           int
		replay           []byte
	}
	firstChunks := make([]firstChunk, len(collections))
	var backup backupIdentity
	var required, total, distinct int
	var counts [26]int
	for i, reader := range collections {
		name, err := readChunkName(reader)
		if err == io.EOF {
			return nil, fmt.Errorf("collection %d contains no encoded chunks", i+1)
		}
		if err != nil {
			return nil, fmt.Errorf("collection %d: %w", i+1, err)
		}
		collection, chunk, size, id, err := extractFromChunkName(name)
		if err != nil {
			return nil, fmt.Errorf("invalid chunk header %q: %w", name, err)
		}
		if err := backup.check(id, collection, chunk); err != nil {
			return nil, err
		}
		if chunk != 1 {
			return nil, chunkNumberMismatch(reader, i, collection, 1, chunk)
		}
		k, n, letter, err := extractFromCollectionLabel(collection)
		if err != nil {
			return nil, fmt.Errorf("invalid collection label in chunk %q: %w", name, err)
		}
		if i == 0 {
			required, total = k, n
		} else if k != required || n != total {
			return nil, fmt.Errorf("collection parameters mismatch: expected %d-of-%d, got %d-of-%d in %s", required, total, k, n, collection)
		}
		index := int(letter[0] - 'A')
		if counts[index] == 0 {
			distinct++
		}
		counts[index]++
		firstChunks[i] = firstChunk{name: name, collection: collection, size: size, letter: index}
	}
	if distinct < required {
		return nil, fmt.Errorf("not enough distinct collections to decode: %d < %d (%d inputs supplied)", distinct, required, len(collections))
	}
	var payloads [26][]byte
	var borrowed [26]bool
	var firstInputs [26]int // First input index plus one, by collection letter.
	var scratch []byte
	permutations := collectionPermutationCount(total, required)
	for i, first := range firstChunks {
		if counts[first.letter] < 2 {
			continue
		}
		if first.size > int(^uint(0)>>1)/permutations {
			return nil, fmt.Errorf("invalid chunk size in %q: %d bytes with %d permutations overflows int", first.name, first.size, permutations)
		}
		expectedSize := first.size * permutations
		previous := firstInputs[first.letter] - 1
		if previous >= 0 && first.size != firstChunks[previous].size {
			return nil, fmt.Errorf("conflicting duplicate collection %s at chunk 1: inputs %d and %d declare different payload sizes", first.collection, previous+1, i+1)
		}
		var payload []byte
		if buffered, ok := collections[i].(interface{ BufferedChunk() []byte }); ok {
			payload = buffered.BufferedChunk()
			if len(payload) != expectedSize {
				return nil, fmt.Errorf("invalid first payload in collection %s (input %d): expected %d bytes, got %d", first.collection, i+1, expectedSize, len(payload))
			}
			if previous < 0 {
				borrowed[first.letter] = true
			}
		} else if previous < 0 {
			var err error
			payload, err = io.ReadAll(io.LimitReader(collections[i], int64(expectedSize)))
			if err == nil && len(payload) != expectedSize {
				err = io.ErrUnexpectedEOF
			}
			if err != nil {
				return nil, fmt.Errorf("read collection %s chunk 1 (input %d): %w", first.collection, i+1, err)
			}
			firstChunks[i].replay = payload
		} else {
			if scratch == nil {
				scratch = make([]byte, 32*1024)
			}
			equal, err := compareDuplicatePayload(context.Background(), collections[i], payloads[first.letter], scratch)
			if err != nil {
				return nil, fmt.Errorf("read duplicate collection %s chunk 1 (input %d): %w", first.collection, i+1, err)
			}
			if equal {
				// Replay the agreed bytes without retaining another payload copy.
				// A buffered source may reuse its storage after preflight; own one
				// copy if any unbuffered duplicate must replay these bytes later.
				if borrowed[first.letter] {
					payloads[first.letter] = bytes.Clone(payloads[first.letter])
					borrowed[first.letter] = false
				}
				payload = payloads[first.letter]
				firstChunks[i].replay = payload
			}
		}
		if previous < 0 {
			firstInputs[first.letter] = i + 1
			payloads[first.letter] = payload
		} else if !bytes.Equal(payloads[first.letter], payload) {
			return nil, fmt.Errorf("conflicting duplicate collection %s at chunk 1: inputs %d and %d have different payloads", first.collection, previous+1, i+1)
		}
	}
	readers := make([]io.Reader, len(collections))
	for i, first := range firstChunks {
		header := append([]byte{byte(len(first.name))}, first.name...)
		readers[i] = &checkedCollectionReader{
			Reader: io.MultiReader(bytes.NewReader(header), bytes.NewReader(first.replay), collections[i]),
			source: collections[i],
		}
	}
	return readers, nil
}

// Preserve the input path through preflight's replay wrapper for diagnostics.
type checkedCollectionReader struct {
	io.Reader
	source io.Reader
}

func (r *checkedCollectionReader) Path() string {
	if source, ok := r.source.(interface{ Path() string }); ok {
		return source.Path()
	}
	return ""
}

func chunkNumberMismatch(reader io.Reader, input int, collection string, expected, got int) error {
	location := fmt.Sprintf("input %d", input+1)
	if source, ok := reader.(interface{ Path() string }); ok && source.Path() != "" {
		location += fmt.Sprintf(", %q", source.Path())
	}
	return fmt.Errorf("chunk number mismatch in collection %s (%s): expected %d, got %d", collection, location, expected, got)
}
