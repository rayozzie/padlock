package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/rayozzie/padlock"
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

func checkOutputDir(dirPath string, clear bool) error {
	expandedPath, err := filepath.Abs(dirPath)
	if err != nil {
		return fmt.Errorf("failed to expand path %s: %w", dirPath, err)
	}
	dirPath = expandedPath

	info, err := os.Stat(dirPath)
	if err == nil && info.IsDir() {
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			return fmt.Errorf("failed to read output directory: %w", err)
		}
		if len(entries) > 0 {
			if !clear {
				fileList := ""
				remainingCount := 0
				for i, entry := range entries {
					if i < 5 {
						fileList += fmt.Sprintf("\n  - %s", entry.Name())
					} else {
						remainingCount++
					}
				}
				errorMsg := fmt.Sprintf("Output directory is not empty. Use -clear to clear the output directory.%s", fileList)
				if remainingCount > 0 {
					errorMsg += fmt.Sprintf("\n  ... and %d more files/directories", remainingCount)
				}
				return fmt.Errorf("%s", errorMsg)
			}

			log.Printf("Clearing output directory: %s", dirPath)
			var clearErrors []string
			for _, entry := range entries {
				entryPath := filepath.Join(dirPath, entry.Name())
				if err := os.RemoveAll(entryPath); err != nil {
					errMsg := fmt.Sprintf("failed to remove %s: %v", entryPath, err)
					log.Printf("%s", errMsg)
					clearErrors = append(clearErrors, errMsg)
				}
			}
			if len(clearErrors) > 0 {
				if len(clearErrors) <= 3 {
					return fmt.Errorf("failed to fully clear directory: %v", clearErrors)
				}
				return fmt.Errorf("failed to fully clear directory: %v and %d more errors", clearErrors[:3], len(clearErrors)-3)
			}
			entries, err = os.ReadDir(dirPath)
			if err != nil {
				return fmt.Errorf("failed to recheck output directory after clearing: %w", err)
			}
			if len(entries) > 0 {
				return fmt.Errorf("output directory not empty after clearing, manual intervention required")
			}
		}
		return nil
	}
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	return nil
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

		fs := flag.NewFlagSet("encode", flag.ExitOnError)
		nVal := fs.Int("copies", 2, "number of collections (must be between 2 and 26)")
		reqVal := fs.Int("required", 2, "minimum collections required for reconstruction")
		formatVal := fs.String("format", "png", "bin or png (default: png)")
		clearVal := fs.Bool("clear", false, "clear output directory if not empty")
		chunkVal := fs.Int("chunk", 2*1024*1024, "maximum candidate block size in bytes (default ~2MB)")
		verboseVal := fs.Bool("verbose", false, "enable detailed (trace/debug) output")
		zipVal := fs.Bool("zip", false, "create zip files for each collection instead of directories")
		fs.Parse(os.Args[4:])

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

		if err := checkOutputDir(outputDir, *clearVal); err != nil {
			log.Fatalf("Error: %v", err)
		}

		cfg := padlock.EncodeConfig{
			InputDir:        inputDir,
			OutputDir:       outputDir,
			N:               *nVal,
			K:               *reqVal,
			Format:          *formatVal,
			ChunkSize:       *chunkVal,
			RNG:             padlock.NewDefaultRNG(),
			ClearIfNotEmpty: *clearVal,
			Verbose:         *verboseVal,
			Compression:     padlock.DefaultCompressionMode,
			ZipCollections:  *zipVal,
		}

		if err := padlock.EncodeData(cfg); err != nil {
			log.Fatalf("Error: Encode failed: %v", err)
		}

	case "decode":
		if len(os.Args) < 4 {
			usage()
		}

		inputDir := os.Args[2]
		outputDir := os.Args[3]

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

		fs := flag.NewFlagSet("decode", flag.ExitOnError)
		clearVal := fs.Bool("clear", false, "clear output directory if not empty")
		verboseVal := fs.Bool("verbose", false, "enable detailed (trace/debug) output")
		fs.Parse(os.Args[4:])

		if err := checkOutputDir(outputDir, *clearVal); err != nil {
			log.Fatalf("Error: %v", err)
		}

		cfg := padlock.DecodeConfig{
			InputDir:    inputDir,
			OutputDir:   outputDir,
			RNG:         padlock.NewDefaultRNG(),
			Verbose:     *verboseVal,
			Compression: padlock.DefaultCompressionMode,
		}

		if err := padlock.DecodeData(cfg); err != nil {
			log.Fatalf("Error: Decode failed: %v", err)
		}

	default:
		usage()
	}
}