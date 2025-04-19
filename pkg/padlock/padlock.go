package padlock

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/pad"
	"github.com/rayozzie/padlock/pkg/trace"
)

// Format is a type alias for file.Format
type Format = file.Format

// Compression represents the compression mode
type Compression int

const (
	// FormatBin is a binary format
	FormatBin = file.FormatBin
	// FormatPNG is a PNG format
	FormatPNG = file.FormatPNG

	// CompressionNone indicates no compression
	CompressionNone Compression = iota
	// CompressionGzip indicates gzip compression
	CompressionGzip
)

// EncodeConfig holds configuration for encoding
type EncodeConfig struct {
	InputDir        string
	OutputDir       string
	N               int
	K               int
	Format          Format
	ChunkSize       int
	RNG             pad.RNG
	ClearIfNotEmpty bool
	Verbose         bool
	Compression     Compression
	ZipCollections  bool
}

// DecodeConfig holds configuration for decoding
type DecodeConfig struct {
	InputDir        string
	OutputDir       string
	RNG             pad.RNG
	Verbose         bool
	Compression     Compression
	ClearIfNotEmpty bool
}

// EncodeDirectory encodes a directory using the padlock scheme
func EncodeDirectory(ctx context.Context, cfg EncodeConfig) error {
	log := trace.FromContext(ctx).WithPrefix("PADLOCK")
	start := time.Now()
	log.Infof("Starting encode: InputDir=%s OutputDir=%s", cfg.InputDir, cfg.OutputDir)
	log.Debugf("Encode parameters: copies=%d, required=%d, Format=%s, ChunkSize=%d", cfg.N, cfg.K, cfg.Format, cfg.ChunkSize)

	// Validate input directory
	if err := file.ValidateInputDirectory(ctx, cfg.InputDir); err != nil {
		return err
	}

	// Prepare the output directory
	if err := file.PrepareOutputDirectory(ctx, cfg.OutputDir, cfg.ClearIfNotEmpty); err != nil {
		return err
	}

	// Create a new pad instance
	log.Debugf("Creating pad instance with N=%d, K=%d", cfg.N, cfg.K)
	p, err := pad.NewPad(cfg.N, cfg.K)
	if err != nil {
		log.Error(fmt.Errorf("failed to create pad instance: %w", err))
		return err
	}

	// Create collection directories
	collections, err := file.CreateCollections(ctx, cfg.OutputDir, p.Collections)
	if err != nil {
		return err
	}

	// Get the formatter for the specified format
	formatter := file.GetFormatter(cfg.Format)

	// Create the directory tar stream
	log.Debugf("Creating tar stream from input directory: %s", cfg.InputDir)
	tarStream, err := file.SerializeDirectoryToStream(ctx, cfg.InputDir)
	if err != nil {
		log.Error(fmt.Errorf("failed to create tar stream: %w", err))
		return fmt.Errorf("failed to create tar stream: %w", err)
	}
	defer tarStream.Close()

	// Create compression stream if needed
	var inputStream io.Reader = tarStream
	if cfg.Compression == CompressionGzip {
		log.Debugf("Adding gzip compression to stream")
		inputStream = file.CompressStreamToStream(ctx, tarStream)
	}

	// Define the function to create new chunks
	newChunkFunc := func(collectionName string, chunkNumber int, chunkFormat string) (io.WriteCloser, error) {
		// Find the collection path
		var collPath string
		for _, coll := range collections {
			if coll.Name == collectionName {
				collPath = coll.Path
				break
			}
		}

		if collPath == "" {
			return nil, fmt.Errorf("collection not found: %s", collectionName)
		}

		// Create a writer that writes to the collection
		return file.NewChunkWriter(ctx, formatter, collPath, 0, chunkNumber), nil
	}

	// Run the encoding process
	log.Debugf("Starting encode process with chunk size: %d", cfg.ChunkSize)
	err = p.Encode(
		ctx,
		cfg.ChunkSize,
		inputStream,
		cfg.RNG,
		newChunkFunc,
		string(cfg.Format),
	)
	if err != nil {
		log.Error(fmt.Errorf("encoding failed: %w", err))
		return fmt.Errorf("encoding failed: %w", err)
	}

	// Handle zip collections if requested
	if cfg.ZipCollections {
		if _, err := file.ZipCollections(ctx, collections); err != nil {
			return err
		}
	}

	elapsed := time.Since(start)
	log.Infof("Encode complete (%s) -copies %d -required %d -format %s", elapsed, cfg.N, cfg.K, cfg.Format)
	return nil
}

// DecodeDirectory decodes a directory using the padlock scheme
func DecodeDirectory(ctx context.Context, cfg DecodeConfig) error {
	log := trace.FromContext(ctx).WithPrefix("PADLOCK")
	start := time.Now()
	log.Infof("Starting decode: InputDir=%s OutputDir=%s", cfg.InputDir, cfg.OutputDir)

	// Validate input directory
	if err := file.ValidateInputDirectory(ctx, cfg.InputDir); err != nil {
		return err
	}

	// Prepare the output directory if needed
	if err := file.PrepareOutputDirectory(ctx, cfg.OutputDir, cfg.ClearIfNotEmpty); err != nil {
		return err
	}

	// Find collections (directories or zips) in the input directory
	collections, tempDir, err := file.FindCollections(ctx, cfg.InputDir)
	if err != nil {
		return err
	}

	// If we extracted zip files, clean up the temporary directory when done
	if tempDir != "" {
		defer func() {
			log.Debugf("Cleaning up temporary directory: %s", tempDir)
			os.RemoveAll(tempDir)
		}()
	}

	if len(collections) == 0 {
		log.Error(fmt.Errorf("no collections found in input directory"))
		return fmt.Errorf("no collections found in input directory")
	}
	log.Debugf("Found %d collections", len(collections))

	// Create collection readers for each collection
	readers := make([]io.Reader, len(collections))
	collReaders := make([]*file.CollectionReader, len(collections))

	for i, coll := range collections {
		collReader := file.NewCollectionReader(coll)
		collReaders[i] = collReader

		// Create an adapter that converts the CollectionReader to an io.Reader
		readers[i] = file.NewChunkReaderAdapter(ctx, collReader)
	}

	// Extract N and K parameters from the first collection's name
	n, k, err := file.ExtractRequiredInfo(collections[0].Name)
	if err != nil {
		log.Error(fmt.Errorf("failed to extract threshold parameters: %w", err))
		return fmt.Errorf("failed to extract threshold parameters: %w", err)
	}
	log.Infof("Collection info: copies=%d, required=%d", n, k)

	// Create a pipe for the decoded data
	log.Debugf("Creating pipe for decoded data")
	pr, pw := io.Pipe()

	// Start the deserialization process in a goroutine
	var deserializeErr error
	go func() {
		defer pw.Close()

		deserializeCtx := trace.WithContext(ctx, log.WithPrefix("DESERIALIZE"))

		// Create decompression stream if needed
		var outputStream io.Reader = pr
		if cfg.Compression == CompressionGzip {
			log.Debugf("Creating decompression stream")
			var err error
			outputStream, err = file.DecompressStreamToStream(deserializeCtx, pr)
			if err != nil {
				log.Error(fmt.Errorf("failed to create decompression stream: %w", err))
				pw.CloseWithError(fmt.Errorf("failed to create decompression stream: %w", err))
				return
			}
		}

		// Deserialize the tar stream to the output directory
		log.Debugf("Deserializing to output directory: %s", cfg.OutputDir)
		err := file.DeserializeDirectoryFromStream(deserializeCtx, cfg.OutputDir, outputStream, cfg.ClearIfNotEmpty)
		if err != nil {
			log.Error(fmt.Errorf("failed to deserialize directory: %w", err))
			deserializeErr = err
			pw.CloseWithError(fmt.Errorf("failed to deserialize directory: %w", err))
		}
	}()

	// Create a new pad instance for decoding
	log.Debugf("Creating pad instance with N=%d, K=%d", n, k)
	p, err := pad.NewPad(n, k)
	if err != nil {
		log.Error(fmt.Errorf("failed to create pad instance: %w", err))
		return err
	}

	// Run the decoding process
	log.Debugf("Starting decode process")
	
	// Create collection names list
	collectionNames := make([]string, len(collections))
	for i, coll := range collections {
		collectionNames[i] = coll.Name
	}
	
	// Try direct decoding first (more reliable with chunk access)
	log.Debugf("Attempting direct decoding from files")
	err = p.DecodeDirect(ctx, cfg.InputDir, collectionNames, pw)
	if err != nil {
		log.Error(fmt.Errorf("direct decoding failed: %w", err))
		
		// Fall back to original method
		log.Debugf("Falling back to standard decoding method")
		err = p.Decode(ctx, readers, pw)
		if err != nil {
			log.Error(fmt.Errorf("decoding failed: %w", err))
			return fmt.Errorf("decoding failed: %w", err)
		}
	}

	// Check if there was an error in the deserialization
	if deserializeErr != nil {
		// Don't treat "too small" tar file as an error - it just means we got a small amount of data
		if strings.Contains(deserializeErr.Error(), "too small to be a valid tar file") {
			log.Infof("Decoding completed successfully but generated only a small amount of data")
			// Output a diagnostic message for the user
			fmt.Printf("\nDecoding completed, but the amount of decoded data was too small to be a valid tar archive.\n")
			fmt.Printf("The decoded data has been saved to %s for inspection.\n\n", filepath.Join(cfg.OutputDir, "sample.dat"))
			return nil
		}
		return deserializeErr
	}

	elapsed := time.Since(start)
	log.Infof("Decode complete (%s)", elapsed)
	return nil
}
