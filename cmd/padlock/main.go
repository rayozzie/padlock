// Copyright 2025 Ray Ozzie. All rights reserved.

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
func usage() {
	fmt.Fprintf(os.Stderr, `Usage:
  padlock encode <inputDir> <outputDir> [-copies N] [-required REQUIRED] [-format bin|png] [-clear] [-chunk SIZE] [-verbose] [-files]
  padlock encode <inputDir> <outputDir1> <outputDir2> ... <outputDirN> [-required REQUIRED] [-format bin|png] [-clear] [-chunk SIZE] [-verbose] [-files]
  padlock encode <inputDir> <outputDir> [-copies N] [-required REQUIRED] [-format bin|png] [-chunk SIZE] [-verbose] [-dryrun]
  padlock encode <inputDir> [-copies N] [-required REQUIRED] [-format bin|png] [-chunk SIZE] [-verbose] [-dryrun]
  padlock decode <inputDir> <outputDir> [-clear] [-verbose]
  padlock decode <inputDir1> <inputDir2> ... <inputDirN> <outputDir> [-clear] [-verbose]
  padlock decode <inputDir1> <inputDir2> ... <inputDirN> <outputDir> [-verbose] [-dryrun]
  padlock decode <inputDir> [-verbose] [-dryrun]

Commands:
  encode            Split input data into N collections with K-of-N threshold security
  decode            Reconstruct original data from K or more collections

Parameters:
  <inputDir>        Source directory containing data to encode or collections to decode
  <outputDir>       Destination directory for encoded collections or decoded data
  <outputDir1>..N>  Individual destination directories for each collection (number of dirs = number of copies)
  <inputDir1>..N>   For decode: collection directories to process (last argument is output directory)

Options:
  -copies N         Number of collections to create (must be between 2 and 26, default: 2)
                    With multiple output directories, defaults to their count; explicit values must match
  -required REQUIRED  Minimum collections required for reconstruction (2 through -copies; default: 2)
                    Defaults to all collections when multiple output directories are provided
  -format FORMAT    Output format: bin or png (default: png)
  -clear            Clear output directories if not empty
  -chunk SIZE       Maximum payload bytes per collection chunk, before headers (default: 2 MiB)
                    Minimum depends on -copies and -required (e.g. 6 bytes for 3-of-5)
  -verbose          Enable detailed debug output
  -files            Create individual files for each collection instead of tar archives (default: creates tar archives)
  -dryrun           Calculate and display size information without actually writing output files
  -entropy-file PATH  Additional seed bytes from a file, device, or pipe; '-' reads stdin
                    Repeatable; encode only. Keep outside input/output directories; never reuse bytes
  -entropy-url URL  Additional seed bytes from HTTPS application/octet-stream (repeatable; encode only)
  -entropy-timeout DURATION  Time limit per external source open/read (default: 10s)
                    Dry runs do not read external sources. Decode does not need them
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
		handleEncode()
	case "decode":
		handleDecode()
	default:
		usage()
	}
}

// handleEncode handles the encode command
func handleEncode() {
	if len(os.Args) < 3 {
		usage()
	}

	inputDir := os.Args[2]

	// Parse flags
	fs := flag.NewFlagSet("encode", flag.ExitOnError)
	nVal := fs.Int("copies", 2, "number of collections (2-26; must match multiple output directories)")
	reqVal := fs.Int("required", 2, "minimum collections required for reconstruction (2 through -copies; defaults to all collections with multiple output directories)")
	formatVal := fs.String("format", "png", "bin or png (default: png)")
	clearVal := fs.Bool("clear", false, "clear output directory if not empty")
	chunkVal := fs.Int("chunk", 2*1024*1024, "maximum payload bytes per collection chunk before headers; minimum depends on -copies and -required (default: 2 MiB)")
	verboseVal := fs.Bool("verbose", false, "enable detailed debug output (includes all trace information)")
	filesVal := fs.Bool("files", false, "create individual files for each collection instead of tar archives")
	dryrunVal := fs.Bool("dryrun", false, "calculate and display size information without actually writing output files")
	entropy := registerEntropyFlags(fs)

	// Determine if we're in size-only mode
	dryrunMode := false
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "-dryrun" {
			dryrunMode = true
			break
		}
	}

	// Collect output directories
	var outputDirs []string
	if len(os.Args) > 3 && !strings.HasPrefix(os.Args[3], "-") {
		// First output directory
		outputDirs = append(outputDirs, os.Args[3])

		// Check for additional output directories
		for i := 4; i < len(os.Args); i++ {
			if strings.HasPrefix(os.Args[i], "-") {
				break
			}
			outputDirs = append(outputDirs, os.Args[i])
		}
	}

	// In dry run mode, output directory is optional
	if len(outputDirs) == 0 && !dryrunMode {
		// Check if -dryrun flag appears after the input dir
		foundDryRunFlag := false
		for i := 3; i < len(os.Args); i++ {
			if os.Args[i] == "-dryrun" {
				foundDryRunFlag = true
				break
			}
		}

		// If not in dry run mode and no output directory, show usage
		if !foundDryRunFlag {
			usage()
		}
	}

	// Calculate where to start parsing flags
	flagsStartIndex := 3
	if len(outputDirs) > 0 {
		flagsStartIndex = 3 + len(outputDirs)
	}

	// Parse flags if there are any
	if flagsStartIndex < len(os.Args) {
		fs.Parse(os.Args[flagsStartIndex:])
	}

	// Explicit default values must not be mistaken for omitted flags.
	copiesSet, requiredSet := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "copies":
			copiesSet = true
		case "required":
			requiredSet = true
		}
	})

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

	// Infer N from multiple output directories only when -copies was omitted.
	if len(outputDirs) > 1 {
		if !copiesSet {
			*nVal = len(outputDirs)
		} else if *nVal != len(outputDirs) {
			log.Fatalf("Error: Number of output directories (%d) does not match -copies value (%d)",
				len(outputDirs), *nVal)
		}
	}

	// Validate flags
	if *nVal < 2 || *nVal > 26 {
		log.Fatalf("Error: Number of collections (-copies) must be between 2 and 26, got %d", *nVal)
	}

	// If -required not explicitly set on command line, default to same as copies when using multiple output dirs
	if !requiredSet && len(outputDirs) > 1 {
		// Only update if we have multiple output directories and -required wasn't specified
		*reqVal = *nVal
		log.Printf("Setting -required to %d to match number of collections", *reqVal)
	}
	if *reqVal < 2 {
		log.Fatalf("Error: -required value %d must be at least 2", *reqVal)
	}

	if *reqVal > *nVal {
		log.Fatalf("Error: -required value %d cannot be greater than number of collections (-copies) %d", *reqVal, *nVal)
	}

	if err := pad.ValidateChunkSize(*nVal, *reqVal, *chunkVal); err != nil {
		log.Fatalf("Error: %v", err)
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

	// Create context with tracer
	ctx := context.Background()
	logLevel := trace.LogLevelNormal
	if *verboseVal {
		logLevel = trace.LogLevelVerbose
	}
	tracer := trace.NewTracer("MAIN", logLevel)
	ctx = trace.WithContext(ctx, tracer)

	cfg := padlock.EncodeConfig{
		InputDir:           inputDir,
		OutputDir:          "", // Will be set below if not in size mode
		OutputDirs:         nil, // Will be set below if not in size mode
		N:                  *nVal,
		K:                  *reqVal,
		Format:             format,
		ChunkSize:          *chunkVal,
		ClearIfNotEmpty:    *clearVal,
		Verbose:            *verboseVal,
		Compression:        padlock.CompressionGzip,
		ArchiveCollections: !*filesVal,
		SizeOnly:           *dryrunVal || dryrunMode,
	}

	// Set output directories
	if len(outputDirs) > 0 {
		cfg.OutputDir = outputDirs[0] // First output dir for backward compatibility
		cfg.OutputDirs = outputDirs
	} else if cfg.SizeOnly {
		// In dry run mode, we can use placeholder paths
		cfg.OutputDir = "dryrun-output"
		cfg.OutputDirs = []string{"dryrun-output"}
	} else {
		// Not in dry run mode and no output directories specified - this is an error
		log.Fatalf("Error: At least one output directory must be specified")
	}

	// Initialize all selected sources before EncodeDirectory can clear outputs.
	sources, closeEntropy, err := entropy.prepare(ctx, inputDir, outputDirs, cfg.SizeOnly, os.Stdin)
	if err != nil {
		log.Fatalf("entropy setup failed: %v", err)
	}
	defer closeEntropy()
	if len(sources) == 0 {
		cfg.RNG = pad.NewDefaultRand(ctx)
	} else {
		cfg.RNG, err = pad.NewRandWithEntropy(ctx, sources...)
		if err != nil {
			closeEntropy()
			log.Fatalf("entropy initialization failed: %v", err)
		}
	}
	if cfg.SizeOnly && len(entropy.specs) > 0 {
		tracer.Infof("Dry run: external entropy sources are not opened or consumed")
	}

	// Encode the directory
	if err := padlock.EncodeDirectory(ctx, cfg); err != nil {
		closeEntropy()
		log.Fatal(fmt.Errorf("encode failed: %w", err))
	}
}

// handleDecode handles the decode command
func handleDecode() {
	if len(os.Args) < 3 {
		usage()
	}

	// First find where the flags start (if any)
	flagIndex := -1
	for i := 2; i < len(os.Args); i++ {
		if strings.HasPrefix(os.Args[i], "-") {
			flagIndex = i
			break
		}
	}

	// If no flags were found, flagIndex is still -1
	if flagIndex == -1 {
		flagIndex = len(os.Args)
	}

	// Parse flags
	fs := flag.NewFlagSet("decode", flag.ExitOnError)
	clearVal := fs.Bool("clear", false, "clear output directory if not empty")
	verboseVal := fs.Bool("verbose", false, "enable detailed debug output")
	dryrunVal := fs.Bool("dryrun", false, "calculate and display size information without actually writing output files")

	// Parse flags if there are any
	if flagIndex < len(os.Args) {
		fs.Parse(os.Args[flagIndex:])
	}

	// Check if we're in size-only mode
	dryrunMode := *dryrunVal
	for i := 2; i < flagIndex; i++ {
		if os.Args[i] == "-dryrun" {
			dryrunMode = true
			break
		}
	}

	// Collect all the non-flag arguments
	args := os.Args[2:flagIndex]

	// Need at least input directories
	if len(args) < 1 {
		usage()
	}

	// Need at least one input directory
	// In dry run mode, the output directory is optional
	var outputDir string
	var inputDirs []string

	if len(args) >= 2 {
		// Last non-flag argument is the output directory
		outputDir = args[len(args)-1]
		// All other non-flag arguments are input directories
		inputDirs = args[:len(args)-1]
	} else if len(args) == 1 && dryrunMode {
		// In dry run mode with just one arg, it's the input directory
		outputDir = ""
		inputDirs = args
	} else {
		// Not enough arguments
		usage()
	}

	// Validate input directories
	for _, dir := range inputDirs {
		inputStat, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				log.Fatalf("Error: Input directory does not exist: %s", dir)
			}
			log.Fatalf("Error: Cannot access input directory %s: %v", dir, err)
		}
		// Input must be a directory for decoding
		if !inputStat.IsDir() {
			log.Fatalf("Error: Input path is not a directory: %s. The input should be a directory containing collection subdirectories or TAR files.", dir)
		}
	}

	// Create context with tracer
	ctx := context.Background()
	logLevel := trace.LogLevelNormal
	if *verboseVal {
		logLevel = trace.LogLevelVerbose
	}
	tracer := trace.NewTracer("MAIN", logLevel)
	ctx = trace.WithContext(ctx, tracer)

	// Create RNG with the configured context
	rng := pad.NewDefaultRand(ctx)

	// Create config
	cfg := padlock.DecodeConfig{
		InputDir:        inputDirs[0], // First input dir for backward compatibility
		InputDirs:       inputDirs,
		OutputDir:       outputDir,
		RNG:             rng,
		Verbose:         *verboseVal,
		Compression:     padlock.CompressionGzip,
		ClearIfNotEmpty: *clearVal,
		SizeOnly:        *dryrunVal || dryrunMode,
	}

	// In dry run mode, check if we need a placeholder output directory
	if cfg.SizeOnly && outputDir == "" {
		cfg.OutputDir = "dryrun-output"
	}

	// Decode the directory
	if err := padlock.DecodeDirectory(ctx, cfg); err != nil {
		log.Fatal(fmt.Errorf("decode failed: %w", err))
	}
}
