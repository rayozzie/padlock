// Package main provides the command-line interface for the padlock cryptographic system.
//
// Padlock is a K-of-N threshold one-time-pad cryptographic system that provides
// information-theoretic security. This means that:
// - Data is split into N collections (shares)
// - Any K collections can reconstruct the original data
// - K-1 or fewer collections reveal absolutely nothing about the original data
// - Security depends entirely on the quality of randomness used
//
// The command-line interface supports two main operations:
// 1. encode: Split input data across N collections with K-of-N threshold security
// 2. decode: Reconstruct original data using K or more collections
//
// Usage examples:
//
//   # Create 3 collections where any 2 can reconstruct the data, in PNG format
//   padlock encode /path/to/input /path/to/output -copies 3 -required 2 -format png
//
//   # Reconstruct the original data from K or more collections
//   padlock decode /path/to/collections /path/to/output
//
//   # Enable verbose logging for debugging
//   padlock encode /path/to/input /path/to/output -verbose
//
//   # Create ZIP archives for each collection instead of directories
//   padlock encode /path/to/input /path/to/output -zip
//
// Security considerations:
// - Never reuse the same collections for different data (violates one-time pad security)
// - Keep collections physically separated to reduce risk of compromise
// - For maximum security, distribute collections through different channels/locations
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/rayozzie/padlock/pkg/pad"
	"github.com/rayozzie/padlock/pkg/padlock"
	"github.com/rayozzie/padlock/pkg/trace"
)

// usage prints the command-line help information and exits.
//
// This function displays usage instructions for the padlock command-line tool,
// explaining the available commands, their parameters, and options.
// After displaying the help text, it exits with status code 1.
func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  padlock encode <inputDir> <outputDir> [-copies N] [-required REQUIRED] [-format bin|png] [-clear] [-chunk SIZE] [-verbose] [-zip]
  padlock decode <inputDir> <outputDir> [-clear] [-verbose]

Commands:
  encode            Split input data into N collections with K-of-N threshold security
  decode            Reconstruct original data from K or more collections

Parameters:
  <inputDir>        Source directory containing data to encode or collections to decode
  <outputDir>       Destination directory for encoded collections or decoded data

Options:
  -copies N         Number of collections to create (must be between 2 and 26, default: 2)
  -required REQUIRED  Minimum collections required for reconstruction (default: 2)
  -format FORMAT    Output format: bin or png (default: png)
  -clear            Clear output directory if not empty
  -chunk SIZE       Maximum candidate block size in bytes (default ~2MB)
  -verbose          Enable detailed (debug/trace) output
  -zip              Create zip files for each collection instead of directories

Examples:
  padlock encode ~/Documents/secret ~/Collections -copies 5 -required 3 -format png -zip
  padlock decode ~/Collections/subset ~/Restored -clear
`)
	os.Exit(1)
}

// main is the entry point for the padlock command-line tool.
//
// This function:
// 1. Parses command-line arguments and flags
// 2. Validates inputs and options
// 3. Creates appropriate configuration
// 4. Sets up logging and context
// 5. Executes the requested operation (encode or decode)
//
// The two main commands supported are:
// - encode: Splits input data across N collections with K-of-N threshold security
// - decode: Reconstructs original data using K or more collections
//
// Error handling:
// - Invalid parameters or flags trigger usage display
// - File access errors are reported with specific messages
// - Operational errors during encoding/decoding are reported with context
func main() {
	// Ensure a command is provided
	if len(os.Args) < 2 {
		usage()
	}

	cmd := os.Args[1]

	switch cmd {
	case "encode":
		if len(os.Args) < 4 {
			usage()
		}

		inputDir := os.Args[2]
		outputDir := os.Args[3]

		// Validate input directory
		inputStat, err := os.Stat(inputDir)
		if err != nil {
			if os.IsNotExist(err) {
				log.Fatalf("Error: Input directory does not exist: %s", inputDir)
			}
			log.Fatalf("Error: Cannot access input directory %s: %v", inputDir, err)
		}
		if !inputStat.IsDir() {
			log.Fatalf("Error: Input path is not a directory: %s", inputDir)
		}

		// Parse flags
		fs := flag.NewFlagSet("encode", flag.ExitOnError)
		nVal := fs.Int("copies", 2, "number of collections (must be between 2 and 26)")
		reqVal := fs.Int("required", 2, "minimum collections required for reconstruction")
		formatVal := fs.String("format", "png", "bin or png (default: png)")
		clearVal := fs.Bool("clear", false, "clear output directory if not empty")
		chunkVal := fs.Int("chunk", 2*1024*1024, "maximum candidate block size in bytes (default ~2MB)")
		verboseVal := fs.Bool("verbose", false, "enable detailed (trace/debug) output")
		zipVal := fs.Bool("zip", false, "create zip files for each collection instead of directories")
		fs.Parse(os.Args[4:])

		// Validate flags
		if *nVal < 2 || *nVal > 26 {
			log.Fatalf("Error: Number of collections (-copies) must be between 2 and 26, got %d", *nVal)
		}
		if *reqVal < 2 {
			log.Printf("Warning: -required value %d is too small, using minimum value of 2", *reqVal)
			*reqVal = 2
		}
		if *reqVal > *nVal {
			log.Printf("Warning: -required value %d cannot be greater than number of collections (-copies) %d; adjusting to %d", *reqVal, *nVal, *nVal)
			*reqVal = *nVal
		}

		*formatVal = strings.ToLower(*formatVal)
		if *formatVal != "bin" && *formatVal != "png" {
			log.Fatalf("Error: -format must be 'bin' or 'png', got '%s'", *formatVal)
		}

		// Create config
		format := padlock.FormatPNG
		if *formatVal == "bin" {
			format = padlock.FormatBin
		}

		cfg := padlock.EncodeConfig{
			InputDir:        inputDir,
			OutputDir:       outputDir,
			N:               *nVal,
			K:               *reqVal,
			Format:          format,
			ChunkSize:       *chunkVal,
			RNG:             pad.NewDefaultRNG(),
			ClearIfNotEmpty: *clearVal,
			Verbose:         *verboseVal,
			Compression:     padlock.CompressionGzip,
			ZipCollections:  *zipVal,
		}

		// Create context with tracer
		ctx := context.Background()
		logLevel := trace.LogLevelNormal
		if *verboseVal {
			logLevel = trace.LogLevelVerbose
		}
		log := trace.NewTracer("MAIN", logLevel)
		ctx = trace.WithContext(ctx, log)

		// Encode the directory
		if err := padlock.EncodeDirectory(ctx, cfg); err != nil {
			log.Fatal(fmt.Errorf("encode failed: %w", err))
		}

	case "decode":
		if len(os.Args) < 4 {
			usage()
		}

		inputDir := os.Args[2]
		outputDir := os.Args[3]

		// Validate input directory
		inputStat, err := os.Stat(inputDir)
		if err != nil {
			if os.IsNotExist(err) {
				log.Fatalf("Error: Input directory does not exist: %s", inputDir)
			}
			log.Fatalf("Error: Cannot access input directory %s: %v", inputDir, err)
		}
		// Input must be a directory for decoding
		if !inputStat.IsDir() {
			log.Fatalf("Error: Input path is not a directory: %s. The input should be a directory containing collection subdirectories or ZIP files.", inputDir)
		}

		// Parse flags
		fs := flag.NewFlagSet("decode", flag.ExitOnError)
		clearVal := fs.Bool("clear", false, "clear output directory if not empty")
		verboseVal := fs.Bool("verbose", false, "enable detailed (trace/debug) output")
		fs.Parse(os.Args[4:])

		// Create config
		cfg := padlock.DecodeConfig{
			InputDir:        inputDir,
			OutputDir:       outputDir,
			RNG:             pad.NewDefaultRNG(),
			Verbose:         *verboseVal,
			Compression:     padlock.CompressionGzip,
			ClearIfNotEmpty: *clearVal,
		}

		// Create context with tracer
		ctx := context.Background()
		logLevel := trace.LogLevelNormal
		if *verboseVal {
			logLevel = trace.LogLevelVerbose
		}
		log := trace.NewTracer("MAIN", logLevel)
		ctx = trace.WithContext(ctx, log)

		// Decode the directory
		if err := padlock.DecodeDirectory(ctx, cfg); err != nil {
			log.Fatal(fmt.Errorf("decode failed: %w", err))
		}

	default:
		usage()
	}
}