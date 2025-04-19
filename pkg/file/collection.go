package file

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rayozzie/padlock/pkg/trace"
)

// Collection represents a collection of encoded data
type Collection struct {
	Name   string
	Path   string
	Format Format
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

// FindCollections locates collection directories or ZIP files in the input directory
func FindCollections(ctx context.Context, inputDir string) ([]Collection, string, error) {
	log := trace.FromContext(ctx).WithPrefix("COLLECTION")

	log.Debugf("Finding collections in %s", inputDir)

	// Create a temporary directory for extracted zip files if needed
	tempDir := ""
	hasZipFiles := false

	// Check if we have zip files in the input directory
	files, err := os.ReadDir(inputDir)
	if err != nil {
		log.Error(fmt.Errorf("failed to read input directory: %w", err))
		return nil, "", fmt.Errorf("failed to read input directory: %w", err)
	}

	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".zip" {
			hasZipFiles = true
			break
		}
	}

	if hasZipFiles {
		log.Debugf("Found zip files, creating temporary directory for extraction")
		var err error
		tempDir, err = os.MkdirTemp("", "padlock-*")
		if err != nil {
			log.Error(fmt.Errorf("failed to create temporary directory: %w", err))
			return nil, "", fmt.Errorf("failed to create temporary directory: %w", err)
		}
		log.Debugf("Created temporary directory: %s", tempDir)
	}

	// Gather collections from directories and zip files
	var collections []Collection

	// First, gather all collection directories
	log.Debugf("Checking for collection directories")
	for _, entry := range files {
		if entry.IsDir() {
			collName := entry.Name()
			// Check if this looks like a collection directory (e.g. "3A5")
			if len(collName) >= 3 && isCollectionName(collName) {
				collPath := filepath.Join(inputDir, collName)
				log.Debugf("Found collection directory: %s", collPath)

				// Determine the format by looking at the files
				format, err := determineCollectionFormat(collPath)
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

	// Then extract zip files if needed
	if hasZipFiles {
		log.Debugf("Checking for collection zip files")
		for _, entry := range files {
			if !entry.IsDir() && filepath.Ext(entry.Name()) == ".zip" {
				zipPath := filepath.Join(inputDir, entry.Name())
				log.Debugf("Found collection zip file: %s", zipPath)

				// Extract the zip file
				extractedDir, err := ExtractZipCollection(ctx, zipPath, tempDir)
				if err != nil {
					log.Error(fmt.Errorf("failed to extract zip collection %s: %w", zipPath, err))
					continue
				}

				collName := filepath.Base(extractedDir)
				if !isCollectionName(collName) {
					log.Error(fmt.Errorf("invalid collection name in zip file: %s", collName))
					continue
				}

				// Determine the format by looking at the files
				format, err := determineCollectionFormat(extractedDir)
				if err != nil {
					log.Error(fmt.Errorf("failed to determine format for extracted collection %s: %w", collName, err))
					continue
				}

				collections = append(collections, Collection{
					Name:   collName,
					Path:   extractedDir,
					Format: format,
				})

				log.Debugf("Added collection %s from zip with format %s", collName, format)
			}
		}
	}

	if len(collections) == 0 {
		log.Error(fmt.Errorf("no collections found in %s", inputDir))
		if tempDir != "" {
			os.RemoveAll(tempDir)
		}
		return nil, "", fmt.Errorf("no collections found in %s", inputDir)
	}

	// Sort collections by name
	sort.Slice(collections, func(i, j int) bool {
		return collections[i].Name < collections[j].Name
	})

	log.Debugf("Found %d collections", len(collections))
	return collections, tempDir, nil
}

// ZipCollections creates zip archives for each collection
func ZipCollections(ctx context.Context, collections []Collection) ([]string, error) {
	log := trace.FromContext(ctx).WithPrefix("COLLECTION")

	log.Infof("Creating zip archives for %d collections", len(collections))
	zipPaths := make([]string, len(collections))

	for i, coll := range collections {
		zipPath, err := ZipCollection(ctx, coll.Path)
		if err != nil {
			log.Error(fmt.Errorf("failed to create zip for collection %s: %w", coll.Name, err))
			return nil, err
		}

		// Remove the original directory
		if err := CleanupCollectionDirectory(ctx, coll.Path); err != nil {
			log.Error(fmt.Errorf("failed to remove original collection directory after zipping: %w", err))
			return nil, err
		}

		zipPaths[i] = zipPath
		log.Infof("Created zip archive for collection %s: %s", coll.Name, zipPath)
	}

	return zipPaths, nil
}

// ExtractRequiredInfo extracts N and K parameters from collection name
func ExtractRequiredInfo(collName string) (n int, k int, err error) {
	// Collection names are in the format "<required><Letter><copies>" (e.g., "3A5")
	var letter rune
	_, err = fmt.Sscanf(collName, "%d%c%d", &k, &letter, &n)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to parse collection name %s: %w", collName, err)
	}

	return n, k, nil
}

// determineCollectionFormat determines the format of a collection by looking at its files
func determineCollectionFormat(collPath string) (Format, error) {
	files, err := os.ReadDir(collPath)
	if err != nil {
		return "", fmt.Errorf("failed to read collection directory: %w", err)
	}

	for _, f := range files {
		name := f.Name()
		if !f.IsDir() {
			if strings.HasPrefix(name, "IMG") && strings.HasSuffix(strings.ToUpper(name), ".PNG") {
				return FormatPNG, nil
			} else if strings.HasSuffix(name, ".bin") {
				return FormatBin, nil
			}
		}
	}

	return "", fmt.Errorf("unable to determine format for collection")
}

// isCollectionName checks if a string looks like a collection name (e.g. "3A5")
func isCollectionName(name string) bool {
	if len(name) < 3 {
		return false
	}

	// Check if the first character is a digit (K)
	if name[0] < '0' || name[0] > '9' {
		return false
	}

	// Check if the middle character is a letter (A-Z)
	middleChar := name[1]
	if (middleChar < 'A' || middleChar > 'Z') && (middleChar < 'a' || middleChar > 'z') {
		return false
	}

	// Check if the last character is a digit (N)
	lastChar := name[len(name)-1]
	if lastChar < '0' || lastChar > '9' {
		return false
	}

	return true
}

// CollectionReader reads data from a collection
type CollectionReader struct {
	Collection Collection
	ChunkIndex int
	Formatter  Formatter
}

// NewCollectionReader creates a new collection reader
func NewCollectionReader(collection Collection) *CollectionReader {
	return &CollectionReader{
		Collection: collection,
		ChunkIndex: 1, // Start at chunk 1
		Formatter:  GetFormatter(collection.Format),
	}
}

// ReadNextChunk reads the next chunk from the collection
func (cr *CollectionReader) ReadNextChunk(ctx context.Context) ([]byte, error) {
	log := trace.FromContext(ctx).WithPrefix("COLLECTION-READER")

	log.Debugf("Reading chunk %d from collection %s", cr.ChunkIndex, cr.Collection.Name)

	data, err := cr.Formatter.ReadChunk(ctx, cr.Collection.Path, 0, cr.ChunkIndex)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			log.Debugf("No more chunks in collection %s", cr.Collection.Name)
			return nil, io.EOF
		}
		log.Error(fmt.Errorf("failed to read chunk %d from collection %s: %w", cr.ChunkIndex, cr.Collection.Name, err))
		return nil, err
	}

	// Increment the chunk index for the next call
	cr.ChunkIndex++

	log.Debugf("Successfully read chunk %d (%d bytes) from collection %s", cr.ChunkIndex-1, len(data), cr.Collection.Name)
	return data, nil
}
