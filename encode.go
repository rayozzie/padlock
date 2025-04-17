package padlock

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EncodeConfig holds configuration for encoding.
type EncodeConfig struct {
	InputDir        string
	OutputDir       string
	N, K            int    // copies and required
	Format          string // "bin" or "png"
	ChunkSize       int    // Candidate block size (total size of candidate records for one chunk)
	RNG             RNG
	ClearIfNotEmpty bool
	Verbose         bool
	Formatter       Formatter
	Compression     CompressionMode // CompressionClear or CompressionGz
	ZipCollections  bool            // Create zip files instead of directories
}

// EncodeData performs a single-pass tar → optional compression → chunker → candidate record generation.
func EncodeData(cfg EncodeConfig) error {
	start := time.Now()
	if cfg.Verbose {
		log.Printf("ENCODE: Starting encode: InputDir=%s OutputDir=%s", cfg.InputDir, cfg.OutputDir)
		log.Printf("ENCODE: copies=%d, required=%d, Format=%s, CandidateBlockSize=%d", cfg.N, cfg.K, cfg.Format, cfg.ChunkSize)
		log.Printf("ENCODE: CompressionMode=%v", cfg.Compression)
	}
	if cfg.ChunkSize < 1 {
		return fmt.Errorf("invalid chunkSize: %d", cfg.ChunkSize)
	}
	cfg.Format = strings.ToLower(cfg.Format)
	if cfg.Format != "bin" && cfg.Format != "png" {
		return fmt.Errorf("format must be 'bin' or 'png', got %s", cfg.Format)
	}
	if cfg.Formatter == nil {
		if cfg.Format == "png" {
			cfg.Formatter = &PngFormatter{}
		} else {
			cfg.Formatter = &BinFormatter{}
		}
	}

	// Create collection directories with names in the format "<required><Letter><copies>" (e.g., "3C5").
	subdirs := make([]string, cfg.N)
	for i := 0; i < cfg.N; i++ {
		letter := collectionLetter(i)
		collName := fmt.Sprintf("%d%c%d", cfg.K, letter, cfg.N)
		sd := filepath.Join(cfg.OutputDir, collName)
		// These messages are intended for users.
		log.Printf("Creating collection subdirectory: %s", sd)
		if err := os.MkdirAll(sd, 0755); err != nil {
			return fmt.Errorf("failed to create collection subdirectory %s: %w", sd, err)
		}
		subdirs[i] = sd
	}

	// Start tar/compression pipeline.
	pr, pw := io.Pipe()
	var wg sync.WaitGroup
	var tarErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		if cfg.Verbose {
			log.Printf("ENCODE: Tar/compression goroutine started. Reading from %s...", cfg.InputDir)
		}
		var topWriter io.WriteCloser = pw
		if cfg.Compression == CompressionGz {
			gz := gzip.NewWriter(pw)
			topWriter = gz
		}
		tw := tar.NewWriter(topWriter)
		err := filepath.Walk(cfg.InputDir, func(path string, info os.FileInfo, wErr error) error {
			if wErr != nil {
				return wErr
			}
			realPath, e2 := filepath.EvalSymlinks(path)
			if e2 == nil {
				outAbs, e3 := filepath.EvalSymlinks(cfg.OutputDir)
				if e3 == nil {
					if realPath == outAbs || strings.HasPrefix(realPath, outAbs+string(os.PathSeparator)) {
						if cfg.Verbose {
							log.Printf("ENCODE: Skipping path inside output directory: %s", path)
						}
						if info.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
				}
			}
			if info.IsDir() {
				return nil
			}
			rel, e4 := filepath.Rel(cfg.InputDir, path)
			if e4 != nil {
				return e4
			}
			if cfg.Verbose {
				log.Printf("ENCODE: Adding file to tar: %s (size %d)", rel, info.Size())
			}
			hdr, e5 := tar.FileInfoHeader(info, "")
			if e5 != nil {
				return e5
			}
			hdr.Name = rel
			if err := tw.WriteHeader(hdr); err != nil {
				return fmt.Errorf("tar WriteHeader: %w", err)
			}
			f, e6 := os.Open(path)
			if e6 != nil {
				return fmt.Errorf("open file for tar: %w", e6)
			}
			defer f.Close()
			dr := &debugReaderEncode{
				base:    f,
				name:    rel,
				verbose: cfg.Verbose,
			}
			written, copyErr := io.Copy(tw, dr)
			if copyErr != nil {
				return fmt.Errorf("io.Copy to tar error: %w", copyErr)
			}
			if cfg.Verbose {
				log.Printf("ENCODE: Added file to tar: %s (wrote %d bytes)", rel, written)
			}
			return nil
		})
		if err != nil {
			tarErr = err
		}
		if cfg.Verbose {
			log.Printf("ENCODE: Closing tar")
		}
		twCloseErr := tw.Close()
		if twCloseErr != nil && tarErr == nil {
			tarErr = fmt.Errorf("tar close: %w", twCloseErr)
		}
		if cfg.Compression == CompressionGz {
			if gzWriter, ok := topWriter.(*gzip.Writer); ok {
				gzCloseErr := gzWriter.Close()
				if gzCloseErr != nil && tarErr == nil {
					tarErr = fmt.Errorf("gzip close: %w", gzCloseErr)
				}
			}
		}
		pwCloseErr := pw.Close()
		if pwCloseErr != nil && tarErr == nil {
			tarErr = fmt.Errorf("pipe writer close: %w", pwCloseErr)
		}
		if cfg.Verbose {
			log.Printf("ENCODE: Tar/compression pipeline finished with error: %v", tarErr)
		}
	}()
	combos := GenerateCombinations(cfg.N, cfg.K)
	R := len(combos)
	// Compute segment size S so that the candidate block (which is composed of R candidate records)
	// has total size cfg.ChunkSize.
	S := (cfg.ChunkSize/R - cfg.K) / (2 * cfg.N)
	if S <= 0 {
		return fmt.Errorf("configured chunk size %d is too small for the specified collections and required count", cfg.ChunkSize)
	}
	inputChunkSize := cfg.N * S
	if cfg.Verbose {
		log.Printf("ENCODE: Computed segment size S=%d, input chunk size=%d bytes, with %d candidate records per chunk", S, inputChunkSize, R)
	}
	buffer := make([]byte, 0, inputChunkSize)
	tmp := make([]byte, 32*1024)
	totalOrigBytes := 0
	chunkIndex := 1
	for {
		n, readErr := pr.Read(tmp)
		if n > 0 {
			buffer = append(buffer, tmp[:n]...)
			totalOrigBytes += n
			for len(buffer) >= inputChunkSize {
				chunkData := buffer[:inputChunkSize]
				if err := encodeOneChunk(cfg, subdirs, chunkIndex, chunkData, S, combos); err != nil {
					pr.Close()
					wg.Wait()
					return err
				}
				chunkIndex++
				buffer = buffer[inputChunkSize:]
			}
		}
		if readErr == io.EOF {
			break
		} else if readErr != nil {
			pr.Close()
			wg.Wait()
			return fmt.Errorf("pipe read error: %w", readErr)
		}
	}
	if len(buffer) > 0 {
		if cfg.Verbose {
			log.Printf("ENCODE: Final partial chunk of size %d at chunk index %d", len(buffer), chunkIndex)
		}
		padded := make([]byte, inputChunkSize)
		copy(padded, buffer)
		if err := encodeOneChunk(cfg, subdirs, chunkIndex, padded, S, combos); err != nil {
			pr.Close()
			wg.Wait()
			return err
		}
		chunkIndex++
	}
	pr.Close()
	wg.Wait()
	if tarErr != nil {
		return tarErr
	}
	// Write keychain record (chunk index 0) to every collection.
	keychain := fmt.Sprintf("copies=%d required=%d mode=Candidate totalData=%d", cfg.N, cfg.K, totalOrigBytes)
	if cfg.Verbose {
		log.Printf("ENCODE: Writing keychain to collections: %s", keychain)
	}
	for i, sd := range subdirs {
		if err := cfg.Formatter.WriteChunk(sd, i, 0, []byte(keychain)); err != nil {
			return fmt.Errorf("failed to write keychain for collection %c: %w", collectionLetter(i), err)
		}
	}
	elapsed := time.Since(start)
	
	// Create zip files for each collection if requested
	if cfg.ZipCollections {
		log.Printf("Creating zip files for each collection")
		for i, sd := range subdirs {
			letter := collectionLetter(i)
			collName := filepath.Base(sd)
			zipPath := filepath.Join(cfg.OutputDir, collName+".zip")
			
			if cfg.Verbose {
				log.Printf("ENCODE: Creating zip file for collection %c: %s", letter, zipPath)
			}
			
			// Create zip file
			zipFile, err := os.Create(zipPath)
			if err != nil {
				return fmt.Errorf("failed to create zip file %s: %w", zipPath, err)
			}
			
			zw := zip.NewWriter(zipFile)
			
			// Walk through collection directory and add files to zip
			err = filepath.Walk(sd, func(path string, info fs.FileInfo, err error) error {
				if err != nil {
					return err
				}
				
				// Skip the directory itself
				if info.IsDir() {
					return nil
				}
				
				// Create a relative path for the zip entry
				rel, err := filepath.Rel(sd, path)
				if err != nil {
					return fmt.Errorf("failed to get relative path: %w", err)
				}
				
				// Create a zip file header
				header, err := zip.FileInfoHeader(info)
				if err != nil {
					return fmt.Errorf("failed to create zip header: %w", err)
				}
				header.Name = rel
				header.Method = zip.Deflate
				
				// Create the file in the zip
				writer, err := zw.CreateHeader(header)
				if err != nil {
					return fmt.Errorf("failed to create zip entry: %w", err)
				}
				
				// Open the file to read its content
				file, err := os.Open(path)
				if err != nil {
					return fmt.Errorf("failed to open file %s: %w", path, err)
				}
				defer file.Close()
				
				// Copy the file content to the zip entry
				_, err = io.Copy(writer, file)
				if err != nil {
					return fmt.Errorf("failed to write file to zip: %w", err)
				}
				
				if cfg.Verbose {
					log.Printf("ENCODE: Added %s to zip file", rel)
				}
				
				return nil
			})
			
			if err != nil {
				zw.Close()
				zipFile.Close()
				return fmt.Errorf("error creating zip for collection %c: %w", letter, err)
			}
			
			// Close the zip writer and file
			if err := zw.Close(); err != nil {
				zipFile.Close()
				return fmt.Errorf("failed to close zip writer: %w", err)
			}
			if err := zipFile.Close(); err != nil {
				return fmt.Errorf("failed to close zip file: %w", err)
			}
			
			// Remove the original directory
			if err := os.RemoveAll(sd); err != nil {
				return fmt.Errorf("failed to remove original collection directory after zipping: %w", err)
			}
			
			if cfg.Verbose {
				log.Printf("ENCODE: Successfully created zip file for collection %c and removed original directory", letter)
			}
		}
		log.Printf("Created zip files for all collections")
	}
	
	elapsed = time.Since(start)
	log.Printf("Encode complete (%s) -copies %d -required %d -format %s", elapsed, cfg.N, cfg.K, cfg.Format)
	return nil
}

func encodeOneChunk(cfg EncodeConfig, subdirs []string, chunkIndex int, chunkData []byte, S int, combos [][]int) error {
	N := cfg.N
	Lp := N * S
	plaintext := make([]byte, Lp)
	copy(plaintext, chunkData)
	pad := make([]byte, Lp)
	n, err := cfg.RNG.Read(pad)
	if err != nil || n != Lp {
		if err != nil {
			return fmt.Errorf("RNG error: %w", err)
		}
		return fmt.Errorf("RNG short read: expected %d, got %d", Lp, n)
	}
	ciphertext := make([]byte, Lp)
	for i := 0; i < Lp; i++ {
		ciphertext[i] = plaintext[i] ^ pad[i]
	}
	padSegments := make([][]byte, N)
	cipherSegments := make([][]byte, N)
	for i := 0; i < N; i++ {
		padSegments[i] = pad[i*S : (i+1)*S]
		cipherSegments[i] = ciphertext[i*S : (i+1)*S]
	}
	combosCount := len(combos)
	candidateBlockSize := 4 + combosCount*(cfg.K+2*N*S)
	candidateBlock := make([]byte, 0, candidateBlockSize)
	countBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(countBytes, uint32(combosCount))
	candidateBlock = append(candidateBlock, countBytes...)
	for _, combo := range combos {
		candidateIDBytes := make([]byte, 0, cfg.K)
		comboMap := make(map[int]bool)
		for _, idx := range combo {
			candidateIDBytes = append(candidateIDBytes, byte('A'+idx))
			comboMap[idx] = true
		}
		leftHalf := make([]byte, 0, N*S)
		rightHalf := make([]byte, 0, N*S)
		for i := 0; i < N; i++ {
			if comboMap[i] {
				leftHalf = append(leftHalf, cipherSegments[i]...)
				rightHalf = append(rightHalf, padSegments[i]...)
			} else {
				leftHalf = append(leftHalf, padSegments[i]...)
				rightHalf = append(rightHalf, cipherSegments[i]...)
			}
		}
		record := append(candidateIDBytes, leftHalf...)
		record = append(record, rightHalf...)
		candidateBlock = append(candidateBlock, record...)
		if cfg.Verbose {
			log.Printf("ENCODE: Chunk %d: generated candidate record %s", chunkIndex, string(candidateIDBytes))
		}
	}
	for i, sd := range subdirs {
		letter := collectionLetter(i)
		if cfg.Verbose {
			log.Printf("ENCODE: For collection %c, writing chunk %d, candidate block size=%d", letter, chunkIndex, len(candidateBlock))
		}
		if err := cfg.Formatter.WriteChunk(sd, i, chunkIndex, candidateBlock); err != nil {
			return fmt.Errorf("failed to write chunk %d for collection %c: %w", chunkIndex, letter, err)
		}
	}
	if cfg.Verbose {
		log.Printf("ENCODE: Chunk %d processed: inputDataSize=%d", chunkIndex, Lp)
	}
	return nil
}

type debugReaderEncode struct {
	base    io.Reader
	name    string
	off     int64
	verbose bool
}

func (dr *debugReaderEncode) Read(p []byte) (int, error) {
	n, err := dr.base.Read(p)
	dr.off += int64(n)
	if dr.verbose {
		log.Printf("ENCODE: Read %d bytes from %s (offset %d)", n, dr.name, dr.off)
	}
	return n, err
}
