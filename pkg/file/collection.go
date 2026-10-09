// Copyright 2025 Ray Ozzie. All rights reserved.

// Package file provides the file system operations for the padlock threshold splitting system.
//
// This package handles all interactions with the file system, including:
// - Managing collections (creating, finding, archiving)
// - Serializing and deserializing directories to/from streams
// - Compressing and decompressing data
// - Reading and writing data chunks in different formats
//
// It abstracts the underlying storage details away from the core threshold splitting
// functionality in the pad package, allowing the system to work with different
// storage formats and approaches without changing the splitting logic.
package file

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rayozzie/padlock/pkg/trace"
)

// Collection represents a collection of encoded data in the padlock system.
//
// A collection is one of the N shares in the K-of-N threshold scheme. Each collection
// contains chunks of encoded data that, when combined with chunks from K-1 other
// collections, can reconstruct the original data. Collections can be stored as
// directories on disk or packaged as TAR files for distribution.
type Collection struct {
	Name   string // The name of the collection (e.g., "3A5")
	Path   string // The filesystem path to the collection
	Format Format // The format of the data chunks (binary or PNG)
}

// CreateCollections creates collection directories for the padlock scheme
func CreateCollections(ctx context.Context, outputDir string, collectionNames []string) ([]Collection, error) {
	log := trace.FromContext(ctx).WithPrefix("COLLECTION")

	log.Debugf("Creating %d collections in %s", len(collectionNames), outputDir)

	// Create collections
	collections := make([]Collection, len(collectionNames))
	for i, collName := range collectionNames {
		collPath, err := CreateCollectionDirectory(ctx, outputDir, collName)
		if err != nil {
			return nil, err
		}

		collections[i] = Collection{
			Name: collName,
			Path: collPath,
		}

		log.Debugf("Created collection %d: %s at %s", i+1, collName, collPath)
	}

	return collections, nil
}

// FindCollections locates collection directories and TAR files without extracting
// archives. The second return value is retained for API compatibility and is
// always empty; callers have no temporary discovery directory to clean up.
func FindCollections(ctx context.Context, inputDir string) ([]Collection, string, error) {
	log := trace.FromContext(ctx).WithPrefix("COLLECTION")

	log.Debugf("Finding collections in %s", inputDir)
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}

	// Check if we have files in the input directory
	files, err := os.ReadDir(inputDir)
	if err != nil {
		log.Error(fmt.Errorf("failed to read input directory: %w", err))
		return nil, "", fmt.Errorf("failed to read input directory: %w", err)
	}

	var collections []Collection
	var skipped skippedChunkNames
	for _, entry := range files {
		if !entry.IsDir() {
			if _, format := ParseChunkFilename(entry.Name()); format == "" {
				skipped.add(entry.Name())
			}
		}
	}
	skipped.report(ctx, inputDir)

	// First, gather all collection directories
	log.Debugf("Checking for collection directories")
	for _, entry := range files {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if entry.IsDir() {
			collName := entry.Name()
			// Check if this looks like a collection directory (e.g. "3A5")
			if len(collName) >= 3 && IsCollectionName(collName) {
				collPath := filepath.Join(inputDir, collName)
				log.Debugf("Found collection directory: %s", collPath)

				// Determine the format by looking at the files
				format, err := DetermineCollectionFormat(collPath)
				if err != nil {
					log.Error(fmt.Errorf("failed to determine format for collection %s: %w", collName, err))
					continue
				}

				collections = append(collections, Collection{
					Name:   collName,
					Path:   collPath,
					Format: format,
				})

				log.Debugf("Added collection %s with format %s", collName, format)
			}
		}
	}

	// Probe TAR metadata, keeping renamed collections in their original archives.
	for _, entry := range files {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tar") {
			continue
		}
		if strings.HasPrefix(entry.Name(), "._") {
			log.Debugf("Skipping AppleDouble sidecar: %s", entry.Name())
			continue
		}
		tarPath := filepath.Join(inputDir, entry.Name())
		collection, err := findTarCollection(ctx, tarPath)
		if err != nil {
			if errors.Is(err, ErrSparseTarEntry) || errors.Is(err, ErrInvalidCollectionFile) || ctx.Err() != nil {
				return nil, "", err
			}
			log.Error(err)
			continue
		}
		if collection.Name == "" {
			log.Debugf("No collection found in TAR file: %s", tarPath)
			continue
		}
		collections = append(collections, collection)
		log.Debugf("Added TAR collection %s with format %s from %s", collection.Name, collection.Format, tarPath)
	}

	// Check if we found any collections
	if len(collections) == 0 {
		log.Error(fmt.Errorf("no collections found in %s", inputDir))
		return nil, "", fmt.Errorf("no collections found in %s", inputDir)
	}

	// Sort collections by name
	sort.Slice(collections, func(i, j int) bool {
		return collections[i].Name < collections[j].Name
	})

	log.Debugf("Found %d collections", len(collections))
	return collections, "", nil
}

// DetermineCollectionFormat determines the format of a collection by looking at its files
// Exported so it can be used by other packages
func DetermineCollectionFormat(collPath string) (Format, error) {
	files, err := os.ReadDir(collPath)
	if err != nil {
		return "", fmt.Errorf("failed to read collection directory: %w", err)
	}

	var skipped skippedChunkNames
	for _, f := range files {
		if !f.IsDir() {
			if _, format := ParseChunkFilename(f.Name()); format != "" {
				return format, nil
			}
			skipped.add(f.Name())
		}
	}

	if skipped.count > 0 {
		return "", fmt.Errorf("no recognized chunk filenames in %q; ignored %d BIN/PNG files (for example %q)", collPath, skipped.count, skipped.example)
	}
	return "", fmt.Errorf("unable to determine format for collection %q: no chunk files found", collPath)
}

// IsCollectionName checks if a string looks like a collection name (e.g. "3A5" or "12Z26")
// Exported so it can be used by other packages
func IsCollectionName(name string) bool {
	if len(name) < 3 {
		return false
	}

	// Check if the first character(s) are digits (K)
	firstDigitIndex := -1
	for i := 0; i < len(name); i++ {
		if name[i] >= '0' && name[i] <= '9' {
			firstDigitIndex = i
		} else {
			break
		}
	}

	if firstDigitIndex < 0 {
		return false // Must start with at least one digit
	}

	// After the initial digits, there must be a letter
	if firstDigitIndex+2 >= len(name) {
		return false // Must have a letter and at least one final digit
	}

	letterChar := name[firstDigitIndex+1]
	if (letterChar < 'A' || letterChar > 'Z') && (letterChar < 'a' || letterChar > 'z') {
		return false // Middle character must be a letter
	}

	// Final position(s) must be digits
	for i := firstDigitIndex + 2; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}

	return true
}

// CollectionReader reads data from a collection
type CollectionReader struct {
	Collection       Collection
	ChunkIndex       int
	Formatter        Formatter
	sortedChunkFiles []string           // Cached list of sorted chunk files in directory
	tarFile          *os.File           // File handle for TAR files
	tarReader        *tar.Reader        // Stored-order fallback for non-seekable TARs
	tarChunks        []tarChunkLocation // Numeric index for seekable TAR files
	tarErr           error              // Terminal failure or EOF; never reopen a finished TAR
}

// Close releases the retained TAR file when decoding stops before reaching EOF.
func (cr *CollectionReader) Close() error {
	if cr.tarFile == nil {
		return nil
	}
	err := cr.tarFile.Close()
	cr.tarFile, cr.tarReader = nil, nil
	cr.tarChunks = nil
	return err
}

// NewCollectionReader creates a new collection reader
func NewCollectionReader(collection Collection) *CollectionReader {
	return &CollectionReader{
		Collection: collection,
		ChunkIndex: 1, // Start at chunk 1
		Formatter:  GetFormatter(collection.Format),
	}
}

// ReadNextChunk reads the next chunk from the collection.
// TAR readers retain EOF or a failure; use a new CollectionReader to retry.
func (cr *CollectionReader) ReadNextChunk(ctx context.Context) ([]byte, error) {
	log := trace.FromContext(ctx).WithPrefix("COLLECTION-READER")

	log.Debugf("Reading next chunk %d from collection %s (path: %s)",
		cr.ChunkIndex, cr.Collection.Name, cr.Collection.Path)

	// Check if this collection is a TAR file
	if strings.HasSuffix(cr.Collection.Path, ".tar") {
		log.Debugf("Collection is a TAR file, using TAR reader")
		// Read directly from TAR file
		return cr.readNextChunkFromTar(ctx)
	}

	// Lazy initialization of sorted chunk files list for directory-based collections
	if cr.sortedChunkFiles == nil {
		log.Debugf("Initializing sorted chunk files for collection in directory %s", cr.Collection.Path)

		// Read all files in the directory
		entries, err := os.ReadDir(cr.Collection.Path)
		if err != nil {
			log.Error(fmt.Errorf("failed to read collection directory: %w", err))
			return nil, fmt.Errorf("failed to read collection directory: %w", err)
		}

		// Use chunk names, not extensions alone: macOS AppleDouble sidecars
		// have the same extension as their corresponding chunk files.
		var chunkFiles []string
		var skipped skippedChunkNames
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if entry.IsDir() {
				continue
			}

			name := entry.Name()
			if _, format := ParseChunkFilename(name); format != "" {
				// Inspect every candidate before returning the first chunk, so
				// decode preflight rejects even a late FIFO before clearing output.
				if _, err := inspectCollectionFile(filepath.Join(cr.Collection.Path, name)); err != nil {
					return nil, err
				}
				chunkFiles = append(chunkFiles, name)
			} else {
				skipped.add(name)
				log.Debugf("Skipping non-chunk file: %s", name)
			}
		}
		skipped.report(ctx, cr.Collection.Path)

		// If no chunk files found, return EOF
		if len(chunkFiles) == 0 {
			log.Debugf("No chunk files found in collection directory: %s", cr.Collection.Path)
			return nil, io.EOF
		}

		// Order by chunk number even when it exceeds the four-digit padding.
		sortChunkFiles(chunkFiles)

		// Log the sorted files for debugging
		if len(chunkFiles) > 0 {
			log.Debugf("Sorted %d chunk files, first: %s, last: %s",
				len(chunkFiles), chunkFiles[0], chunkFiles[len(chunkFiles)-1])
		}

		// Store the sorted chunk files
		cr.sortedChunkFiles = chunkFiles
		log.Debugf("Found and sorted %d chunk files in directory", len(chunkFiles))
	}

	// Check if we've reached the end of the chunk files
	if cr.ChunkIndex > len(cr.sortedChunkFiles) {
		log.Debugf("No more chunks in collection (reached end of sorted files)")
		return nil, io.EOF
	}

	// Get the current chunk file
	chunkFile := cr.sortedChunkFiles[cr.ChunkIndex-1]
	filePath := filepath.Join(cr.Collection.Path, chunkFile)

	log.Debugf("Reading chunk %d (file: %s) from collection %s", cr.ChunkIndex, chunkFile, cr.Collection.Name)

	_, format := ParseChunkFilename(chunkFile)
	data, err := readCollectionFile(filePath, format)
	if err != nil {
		return nil, err
	}

	log.Debugf("Successfully read %d bytes from chunk file %s", len(data), chunkFile)

	// Increment the chunk index for the next read
	cr.ChunkIndex++

	return data, nil
}

// readNextChunkFromTar reads the next chunk directly from a TAR file
func (cr *CollectionReader) readNextChunkFromTar(ctx context.Context) (data []byte, readErr error) {
	log := trace.FromContext(ctx).WithPrefix("TAR-READER")
	if cr.tarErr != nil {
		return nil, cr.tarErr
	}
	defer func() {
		if readErr != nil {
			if closeErr := cr.Close(); closeErr != nil {
				readErr = errors.Join(readErr, fmt.Errorf("close TAR archive %q: %w", cr.Collection.Path, closeErr))
			}
			// Preserve plain io.EOF when closing succeeds: adapters use it to
			// distinguish an exhausted collection from a read/close failure.
			cr.tarErr = readErr
		}
	}()

	// If this is the first time accessing the TAR file, open it and prepare the reader
	if cr.tarFile == nil {
		log.Debugf("Opening TAR file for streaming: %s", cr.Collection.Path)

		// Open the TAR file
		file, err := openCollectionFile(cr.Collection.Path)
		if err != nil {
			return nil, fmt.Errorf("open TAR archive %q: %w", cr.Collection.Path, err)
		}

		// Store the file handle so we can close it later
		cr.tarFile = file

		cr.tarChunks, err = indexTarChunks(ctx, file, cr.Collection.Path)
		if err != nil {
			return nil, err
		}

		log.Debugf("Set up TAR streaming for collection %s", cr.Collection.Name)
	}
	if cr.tarReader == nil {
		return cr.readIndexedTarChunk(ctx)
	}

	// Read and process the next entry from the TAR file
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header, err := cr.tarReader.Next()
		if err == io.EOF {
			log.Debugf("Reached end of TAR file %s", cr.Collection.Path)
			return nil, io.EOF
		}
		if err != nil {
			return nil, fmt.Errorf("read TAR header from %q: %w", cr.Collection.Path, err)
		}

		// Reject even non-chunk entries before the skip path can expand holes.
		if err := RejectSparseTarEntry(header); err != nil {
			return nil, fmt.Errorf("read TAR entry %q from %q: %w", header.Name, cr.Collection.Path, err)
		}

		name := header.Name
		_, format := ParseChunkFilename(path.Base(name))
		if format != "" {
			if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
				return nil, fmt.Errorf("read TAR entry %q from %q: chunk is not a regular file", name, cr.Collection.Path)
			}

			log.Debugf("Reading chunk %d (file: %s) from TAR stream for collection %s",
				cr.ChunkIndex, name, cr.Collection.Name)

			// Read the chunk content
			var data []byte
			var err error

			if format == FormatPNG {
				// For PNG files, extract data from the PNG
				var buf bytes.Buffer
				bytesRead, err := io.Copy(&buf, cr.tarReader)
				if err != nil {
					return nil, fmt.Errorf("read TAR entry %q from %q (read %d bytes): %w", name, cr.Collection.Path, bytesRead, err)
				}

				log.Debugf("Successfully read %d bytes from TAR chunk %s", bytesRead, name)

				// Extract data from the PNG with enhanced error reporting
				data, err = ExtractDataFromPNG(&buf)
				if err != nil {
					// Detailed error logging for PNG extraction failure
					pngErr := fmt.Errorf("decode PNG entry %q from TAR %q: %w", name, cr.Collection.Path, err)
					log.Error(pngErr)

					// Save a copy of the problematic PNG for debugging if needed
					if buf.Len() > 0 {
						log.Debugf("PNG error analysis: PNG size=%d bytes, first 16 bytes: %x",
							buf.Len(),
							buf.Bytes()[:min(16, buf.Len())])
					}

					// Return the error rather than just continuing, to help with debugging
					return nil, pngErr
				}
			} else {
				// For binary files, just read the content
				data, err = io.ReadAll(cr.tarReader)
				if err != nil {
					return nil, fmt.Errorf("read TAR entry %q from %q (read %d bytes): %w", name, cr.Collection.Path, len(data), err)
				}
			}

			log.Debugf("Successfully read %d bytes from TAR chunk %s", len(data), name)

			// Increment the chunk index for the next read
			cr.ChunkIndex++

			return data, nil
		} else {
			// Skip this entry but consume its content
			log.Debugf("Skipping non-chunk file in TAR: %s", name)
			bytesRead, err := io.Copy(io.Discard, cr.tarReader)
			if err != nil {
				return nil, fmt.Errorf("skip TAR entry %q from %q (read %d bytes): %w", name, cr.Collection.Path, bytesRead, err)
			}
		}
	}
}

// min is a helper function to get the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
