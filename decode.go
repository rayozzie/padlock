package padlock

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DecodeConfig holds configuration for decoding.
type DecodeConfig struct {
	InputDir    string
	OutputDir   string
	RNG         RNG
	Verbose     bool
	Compression CompressionMode // Must match the encoding mode.
}

func DecodeData(cfg DecodeConfig) error {
	start := time.Now()
	if cfg.Verbose {
		log.Printf("DECODE: Starting decode: InputDir=%s OutputDir=%s", cfg.InputDir, cfg.OutputDir)
		log.Printf("DECODE: CompressionMode=%v", cfg.Compression)
	}
	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	dirs, err := os.ReadDir(cfg.InputDir)
	if err != nil {
		return fmt.Errorf("failed to read input directory: %w", err)
	}
	var collDirs []string
	for _, d := range dirs {
		if d.IsDir() {
			collDirs = append(collDirs, filepath.Join(cfg.InputDir, d.Name()))
		}
	}
	if len(collDirs) == 0 {
		return fmt.Errorf("no collection subdirectories found in %s", cfg.InputDir)
	}
	sort.Strings(collDirs)
	if cfg.Verbose {
		log.Printf("DECODE: Available collections (directories): %v", collDirs)
	}

	// Use the first collection to determine file naming format.
	firstColl := collDirs[0]
	cfiles, err := os.ReadDir(firstColl)
	if err != nil {
		return fmt.Errorf("failed to read first collection directory: %w", err)
	}
	var format string
	for _, f := range cfiles {
		nm := f.Name()
		if !f.IsDir() {
			if strings.HasPrefix(nm, "IMG") {
				format = "png"
				break
			} else if strings.HasPrefix(nm, "chunk") {
				format = "bin"
				break
			}
		}
	}
	if format == "" {
		return fmt.Errorf("unable to determine file format from first collection directory")
	}
	if cfg.Verbose {
		log.Printf("DECODE: Detected file format: %s", format)
	}

	// Read keychain record (chunk index 0) from the first collection.
	var keychainRaw []byte
	keychainPath := ""
	collectionID := filepath.Base(firstColl) // e.g., "3C5"
	if format == "png" {
		keychainPath = filepath.Join(firstColl, fmt.Sprintf("IMG%s_%04d.PNG", collectionID, 0))
		f, err := os.Open(keychainPath)
		if err != nil {
			return fmt.Errorf("failed to open keychain PNG file: %w", err)
		}
		defer f.Close()
		kc, err := extractDataFromPNG(f)
		if err != nil {
			return fmt.Errorf("failed to extract keychain from PNG: %w", err)
		}
		keychainRaw = kc
	} else {
		keychainPath = filepath.Join(firstColl, fmt.Sprintf("%s_%04d.bin", collectionID, 0))
		data, err := os.ReadFile(keychainPath)
		if err != nil {
			return fmt.Errorf("failed to read keychain binary file: %w", err)
		}
		keychainRaw = data
	}
	keychainStr := string(keychainRaw)
	var copies, required, dataSize int
	// Expected keychain format: "copies=%d required=%d mode=Candidate totalData=%d"
	parts := strings.Split(keychainStr, " ")
	for _, p := range parts {
		if strings.HasPrefix(p, "copies=") {
			copies, _ = strconv.Atoi(strings.TrimPrefix(p, "copies="))
		} else if strings.HasPrefix(p, "required=") {
			required, _ = strconv.Atoi(strings.TrimPrefix(p, "required="))
		} else if strings.HasPrefix(p, "totalData=") {
			dataSize, _ = strconv.Atoi(strings.TrimPrefix(p, "totalData="))
		}
	}
	if copies < 2 || required < 2 || dataSize < 1 {
		return fmt.Errorf("invalid keychain information: %s", keychainStr)
	}
	// This keychain summary is intended for users.
	if cfg.Verbose {
		log.Printf("DECODE: Keychain info: copies=%d, required=%d, totalData=%d", copies, required, dataSize)
	}

	// Determine available collection letters from subdirectory names.
	var availableLetters []string
	for _, d := range collDirs {
		base := filepath.Base(d)
		// Expect format "<required><Letter><copies>" e.g., "3C5"
		if len(base) >= 3 {
			letter := string(base[1])
			availableLetters = append(availableLetters, letter)
		}
	}
	sort.Strings(availableLetters)
	if cfg.Verbose {
		log.Printf("DECODE: Collections available (letters): %s", strings.Join(availableLetters, ""))
	}
	if len(availableLetters) < required {
		return fmt.Errorf("insufficient collections: found %d but require %d", len(availableLetters), required)
	}
	desiredCandidateID := strings.Join(availableLetters[:required], "")
	if cfg.Verbose {
		log.Printf("DECODE: Using candidate record: %s", desiredCandidateID)
	}

	// Gather candidate chunk files from the first collection (skip keychain file).
	var candFiles []string
	for _, f := range cfiles {
		nm := f.Name()
		if !f.IsDir() {
			if format == "png" {
				if nm != fmt.Sprintf("IMG%s_%04d.PNG", collectionID, 0) && strings.HasPrefix(nm, "IMG") {
					candFiles = append(candFiles, nm)
				}
			} else {
				if nm != fmt.Sprintf("%s_%04d.bin", collectionID, 0) && strings.HasPrefix(nm, "chunk") {
					candFiles = append(candFiles, nm)
				}
			}
		}
	}
	sort.Strings(candFiles)
	totalChunks := len(candFiles)
	if totalChunks == 0 {
		return fmt.Errorf("no candidate chunk files found in the first collection")
	}
	if cfg.Verbose {
		log.Printf("DECODE: Found %d candidate chunk files", totalChunks)
	}

	var reassembled []byte
	for i := 1; i <= totalChunks; i++ {
		var chunkFile string
		if format == "png" {
			chunkFile = fmt.Sprintf("IMG%s_%04d.PNG", collectionID, i)
		} else {
			chunkFile = fmt.Sprintf("%s_%04d.bin", collectionID, i)
		}
		path := filepath.Join(firstColl, chunkFile)
		var raw []byte
		if format == "png" {
			f, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("failed to open candidate chunk PNG file: %w", err)
			}
			data, err := extractDataFromPNG(f)
			f.Close()
			if err != nil {
				return fmt.Errorf("failed to extract candidate chunk from PNG: %w", err)
			}
			raw = data
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("failed to read candidate chunk file: %w", err)
			}
			raw = data
		}
		if len(raw) < 4 {
			return fmt.Errorf("candidate block in file %s is too short", path)
		}
		candidateCount := int(binary.BigEndian.Uint32(raw[0:4]))
		recordsData := raw[4:]
		if candidateCount == 0 {
			return fmt.Errorf("no candidate records found in file %s", path)
		}
		recLen := len(recordsData) / candidateCount
		if recLen*candidateCount != len(recordsData) {
			return fmt.Errorf("inconsistent candidate record size in file %s", path)
		}
		if recLen < required {
			return fmt.Errorf("candidate record in file %s is too short", path)
		}
		// Compute segment size: S = (recLen - required) / (2 * copies)
		S := (recLen - required) / (2 * copies)
		if (recLen - required) != 2*copies*S {
			return fmt.Errorf("candidate record size inconsistency in file %s", path)
		}
		var foundRecord []byte
		for j := 0; j < candidateCount; j++ {
			record := recordsData[j*recLen : (j+1)*recLen]
			candidateID := string(record[:required])
			if candidateID == desiredCandidateID {
				foundRecord = record
				break
			}
		}
		if foundRecord == nil {
			return fmt.Errorf("candidate record %s not found in chunk %d", desiredCandidateID, i)
		}
		leftHalf := foundRecord[required : required+copies*S]
		rightHalf := foundRecord[required+copies*S : required+2*copies*S]
		if len(leftHalf) != copies*S || len(rightHalf) != copies*S {
			return fmt.Errorf("invalid candidate record halves in chunk %d", i)
		}
		recovered := make([]byte, copies*S)
		for j := 0; j < copies*S; j++ {
			recovered[j] = leftHalf[j] ^ rightHalf[j]
		}
		reassembled = append(reassembled, recovered...)
		if cfg.Verbose {
			log.Printf("DECODE: Chunk %d: recovered %d bytes using candidate record %s", i, len(recovered), desiredCandidateID)
		}
	}
	if len(reassembled) >= 16 && cfg.Verbose {
		log.Printf("DECODE: Reassembled data header: %x", reassembled[:16])
	}
	// This summary message is intended for users.
	if cfg.Verbose {
		log.Printf("DECODE: Untarring data (total size=%d) with compression mode=%v", len(reassembled), cfg.Compression)
	}
	if cfg.Compression == CompressionClear {
		if rem := len(reassembled) % 512; rem != 0 {
			newLen := len(reassembled) - rem
			if cfg.Verbose {
				log.Printf("DECODE: Trimming reassembled data from %d to %d bytes (to obtain a multiple of 512)", len(reassembled), newLen)
			}
			reassembled = reassembled[:newLen]
		}
	}
	var decompressed io.Reader = bytes.NewReader(reassembled)
	if cfg.Compression == CompressionGz {
		gz, err := gzip.NewReader(decompressed)
		if err != nil {
			log.Printf("Error creating gzip reader: %v", err)
			return fmt.Errorf("gzip reader creation failed: %w", err)
		}
		defer gz.Close()
		decompressed = gz
	}
	tr := tar.NewReader(decompressed)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar header read error: %w", err)
		}
		fpath := filepath.Join(cfg.OutputDir, hdr.Name)
		if hdr.Typeflag == tar.TypeDir {
			os.MkdirAll(fpath, 0755)
			continue
		}
		if cfg.Verbose {
			log.Printf("DECODE Extracting %s (size=%d)", hdr.Name, hdr.Size)
		}
		if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
			return fmt.Errorf("failed to create parent directory for %s: %w", fpath, err)
		}
		f, err := os.OpenFile(fpath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, hdr.FileInfo().Mode())
		if err != nil {
			return fmt.Errorf("failed to open file %s: %w", fpath, err)
		}
		written, err := io.Copy(f, tr)
		f.Close()
		if err != nil {
			return fmt.Errorf("file copy error for %s: %w", fpath, err)
		}
		if cfg.Verbose {
			log.Printf("DECODE: Wrote %d bytes to %s", written, fpath)
		}
		count++
	}
	if cfg.Verbose {
		log.Printf("DECODE: Untar extracted %d files", count)
	}
	elapsed := time.Since(start)
	log.Printf("Decode complete (%v)", elapsed)
	return nil
}
