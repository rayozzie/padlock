package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/rayozzie/padlock/pkg/padlock"
	"github.com/rayozzie/padlock/pkg/rng"
	"github.com/rayozzie/padlock/pkg/trace"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  padlock encode <inputDir> <outputDir> [-copies N] [-required REQUIRED] [-format bin|png] [-clear] [-chunk SIZE] [-verbose] [-zip]
  padlock decode <inputDir> <outputDir> [-clear] [-verbose]

Options:
  -copies N         Number of collections to create (must be between 2 and 26, default: 2)
  -required REQUIRED  Minimum collections required for reconstruction (default: 2)
  -format FORMAT    Output format: bin or png (default: png)
  -clear            Clear output directory if not empty
  -chunk SIZE       Maximum candidate block size in bytes (default ~2MB)
  -verbose          Enable detailed (debug/trace) output
  -zip              Create zip files for each collection instead of directories
`)
	os.Exit(1)
}

func main() {
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
			RNG:             rng.NewDefaultRNG(),
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
			RNG:             rng.NewDefaultRNG(),
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
