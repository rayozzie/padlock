// Package file provides utilities for file and directory management,
// including format-specific handling (binary, PNG), ZIP operations, and
// directory validation.
package file

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/rayozzie/padlock/pkg/trace"
)

// Format represents the output format used by padlock
type Format string

const (
	// FormatBin is a binary format
	FormatBin Format = "bin"
	// FormatPNG is a PNG format
	FormatPNG Format = "png"
)

// Formatter defines required methods for storing encoded chunk data
type Formatter interface {
	// WriteChunk writes a chunk of data to a file in the specified collection
	WriteChunk(ctx context.Context, collectionPath string, collectionIndex int, chunkNumber int, data []byte) error

	// ReadChunk reads a chunk of data from a file in the specified collection
	ReadChunk(ctx context.Context, collectionPath string, collectionIndex int, chunkNumber int) ([]byte, error)
}

// BinFormatter writes and reads raw binary files
type BinFormatter struct{}

// WriteChunk writes a chunk to a binary file
func (bf *BinFormatter) WriteChunk(ctx context.Context, collectionPath string, collectionIndex int, chunkNumber int, data []byte) error {
	log := trace.FromContext(ctx).WithPrefix("BIN-FORMATTER")

	base := filepath.Base(collectionPath)
	fname := fmt.Sprintf("%s_%04d.bin", base, chunkNumber)
	fp := filepath.Join(collectionPath, fname)

	log.Debugf("Writing chunk %d to binary file: %s", chunkNumber, fp)

	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		log.Error(fmt.Errorf("failed to create chunk directory: %w", err))
		return fmt.Errorf("failed to create chunk directory: %w", err)
	}

	f, err := os.OpenFile(fp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Error(fmt.Errorf("failed to open chunk file: %w", err))
		return fmt.Errorf("failed to open chunk file: %w", err)
	}
	defer f.Close()

	if _, werr := f.Write(data); werr != nil {
		log.Error(fmt.Errorf("failed to write chunk data: %w", werr))
		return fmt.Errorf("failed to write chunk data: %w", werr)
	}

	if err := f.Sync(); err != nil {
		log.Error(fmt.Errorf("failed to sync chunk file: %w", err))
		return fmt.Errorf("failed to sync chunk file: %w", err)
	}

	log.Debugf("Successfully wrote %d bytes to chunk file", len(data))
	return nil
}

// ReadChunk reads a chunk from a binary file
func (bf *BinFormatter) ReadChunk(ctx context.Context, collectionPath string, collectionIndex int, chunkNumber int) ([]byte, error) {
	log := trace.FromContext(ctx).WithPrefix("BIN-FORMATTER")

	base := filepath.Base(collectionPath)
	fname := fmt.Sprintf("%s_%04d.bin", base, chunkNumber)
	fp := filepath.Join(collectionPath, fname)

	log.Debugf("Reading chunk %d from binary file: %s", chunkNumber, fp)

	// Check if the file exists
	if _, err := os.Stat(fp); os.IsNotExist(err) {
		log.Debugf("Chunk file does not exist: %s", fp)
		return nil, fmt.Errorf("chunk file not found: %s", fp)
	}

	data, err := os.ReadFile(fp)
	if err != nil {
		log.Error(fmt.Errorf("failed to read chunk file: %w", err))
		return nil, fmt.Errorf("failed to read chunk file: %w", err)
	}

	log.Debugf("Successfully read %d bytes from chunk file", len(data))
	return data, nil
}

// PngFormatter embeds chunk data in a PNG
type PngFormatter struct{}

// WriteChunk writes a chunk to a PNG file
func (pf *PngFormatter) WriteChunk(ctx context.Context, collectionPath string, collectionIndex int, chunkNumber int, data []byte) error {
	log := trace.FromContext(ctx).WithPrefix("PNG-FORMATTER")

	base := filepath.Base(collectionPath)
	fname := fmt.Sprintf("IMG%s_%04d.PNG", base, chunkNumber)
	fp := filepath.Join(collectionPath, fname)

	log.Debugf("Writing chunk %d to PNG file: %s", chunkNumber, fp)

	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		log.Error(fmt.Errorf("failed to create chunk directory: %w", err))
		return fmt.Errorf("failed to create chunk directory: %w", err)
	}

	f, err := os.OpenFile(fp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.Error(fmt.Errorf("failed to open PNG file %s: %w", fp, err))
		return fmt.Errorf("failed to open PNG file %s: %w", fp, err)
	}
	defer f.Close()

	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.Transparent)
	if err := encodePNGWithData(f, img, data); err != nil {
		f.Close()
		os.Remove(fp)
		log.Error(fmt.Errorf("failed to encode PNG with data for %s: %w", fp, err))
		return fmt.Errorf("failed to encode PNG with data for %s: %w", fp, err)
	}

	if err := f.Sync(); err != nil {
		log.Error(fmt.Errorf("failed to sync PNG file: %w", err))
		return fmt.Errorf("failed to sync PNG file: %w", err)
	}

	log.Debugf("Successfully wrote %d bytes to PNG file", len(data))
	return nil
}

// ReadChunk reads a chunk from a PNG file
func (pf *PngFormatter) ReadChunk(ctx context.Context, collectionPath string, collectionIndex int, chunkNumber int) ([]byte, error) {
	log := trace.FromContext(ctx).WithPrefix("PNG-FORMATTER")

	base := filepath.Base(collectionPath)
	fname := fmt.Sprintf("IMG%s_%04d.PNG", base, chunkNumber)
	fp := filepath.Join(collectionPath, fname)

	log.Debugf("Reading chunk %d from PNG file: %s", chunkNumber, fp)

	// Check if the file exists
	if _, err := os.Stat(fp); os.IsNotExist(err) {
		log.Debugf("Chunk file does not exist: %s", fp)
		return nil, fmt.Errorf("chunk file not found: %s", fp)
	}

	f, err := os.Open(fp)
	if err != nil {
		log.Error(fmt.Errorf("failed to open PNG file: %w", err))
		return nil, fmt.Errorf("failed to open PNG file: %w", err)
	}
	defer f.Close()

	data, err := ExtractDataFromPNG(f)
	if err != nil {
		log.Error(fmt.Errorf("failed to extract data from PNG: %w", err))
		return nil, fmt.Errorf("failed to extract data from PNG: %w", err)
	}

	log.Debugf("Successfully read %d bytes from PNG file", len(data))
	return data, nil
}

// GetFormatter returns a Formatter for the specified format
func GetFormatter(format Format) Formatter {
	switch format {
	case FormatPNG:
		return &PngFormatter{}
	case FormatBin:
		return &BinFormatter{}
	default:
		return &BinFormatter{} // Default to binary format
	}
}

// encodePNGWithData injects 'data' into a custom 'rAWd' chunk in a minimal PNG
func encodePNGWithData(w io.Writer, img image.Image, data []byte) error {
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&buf, img); err != nil {
		return fmt.Errorf("PNG encode error: %w", err)
	}
	pngBytes := buf.Bytes()

	if len(pngBytes) < 12 {
		return fmt.Errorf("invalid PNG (too short)")
	}
	iendPos := bytes.Index(pngBytes, []byte("IEND"))
	if iendPos == -1 || iendPos < 4 {
		return fmt.Errorf("invalid PNG, IEND not found")
	}
	iendPos -= 4

	if _, err := w.Write(pngBytes[:iendPos]); err != nil {
		return fmt.Errorf("writing PNG prefix: %w", err)
	}

	chunkType := []byte("rAWd")
	length := uint32(len(data))
	var lengthBytes [4]byte
	binary.BigEndian.PutUint32(lengthBytes[:], length)
	if _, err := w.Write(lengthBytes[:]); err != nil {
		return fmt.Errorf("writing chunk length: %w", err)
	}
	if _, err := w.Write(chunkType); err != nil {
		return fmt.Errorf("writing chunk type: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("writing chunk data: %w", err)
	}
	crc := crc32.NewIEEE()
	crc.Write(chunkType)
	crc.Write(data)
	var crcBytes [4]byte
	binary.BigEndian.PutUint32(crcBytes[:], crc.Sum32())
	if _, err := w.Write(crcBytes[:]); err != nil {
		return fmt.Errorf("writing chunk CRC: %w", err)
	}

	if _, err := w.Write(pngBytes[iendPos:]); err != nil {
		return fmt.Errorf("writing IEND: %w", err)
	}
	return nil
}

// ExtractDataFromPNG extracts data from a PNG's 'rAWd' chunk
func ExtractDataFromPNG(r io.Reader) ([]byte, error) {
	all, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read PNG data: %w", err)
	}
	chunkType := []byte("rAWd")
	chunkPos := bytes.Index(all, chunkType)
	if chunkPos == -1 {
		return nil, fmt.Errorf("'rAWd' chunk not found")
	}
	if chunkPos < 4 {
		return nil, fmt.Errorf("invalid structure, chunk at offset <4")
	}
	lengthBuf := all[chunkPos-4 : chunkPos]
	length := binary.BigEndian.Uint32(lengthBuf)
	dataStart := chunkPos + len(chunkType)
	dataEnd := dataStart + int(length)
	if dataEnd > len(all) {
		return nil, fmt.Errorf("invalid PNG chunk length, out of range")
	}
	extracted := all[dataStart:dataEnd]
	crcPos := dataEnd
	if crcPos+4 > len(all) {
		return nil, fmt.Errorf("invalid chunk: no CRC found")
	}
	expectedCRC := binary.BigEndian.Uint32(all[crcPos : crcPos+4])
	crcCalc := crc32.NewIEEE()
	crcCalc.Write(chunkType)
	crcCalc.Write(extracted)
	if crcCalc.Sum32() != expectedCRC {
		return nil, fmt.Errorf("CRC mismatch in 'rAWd' chunk")
	}
	return extracted, nil
}
