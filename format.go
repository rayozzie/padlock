package padlock

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
)

// Formatter defines required methods for storing encoded chunk data.
type Formatter interface {
	InitCollection(collectionPath string, collectionIndex int) error
	WriteChunk(collectionPath string, collectionIndex int, chunkIndex int, data []byte) error
	FinalizeCollection(collectionPath string, collectionIndex int) error
}

// BinFormatter writes raw binary files.
type BinFormatter struct{}

func (bf *BinFormatter) InitCollection(collectionPath string, collectionIndex int) error {
	return os.MkdirAll(collectionPath, 0755)
}
func (bf *BinFormatter) WriteChunk(collectionPath string, collectionIndex int, chunkIndex int, data []byte) error {
	base := filepath.Base(collectionPath)
	fname := fmt.Sprintf("%s_%04d.bin", base, chunkIndex)
	fp := filepath.Join(collectionPath, fname)
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return fmt.Errorf("failed to create chunk directory: %w", err)
	}
	f, err := os.OpenFile(fp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open chunk file: %w", err)
	}
	defer f.Close()
	if _, werr := f.Write(data); werr != nil {
		return fmt.Errorf("failed to write chunk data: %w", werr)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync chunk file: %w", err)
	}
	return nil
}
func (bf *BinFormatter) FinalizeCollection(collectionPath string, collectionIndex int) error {
	return nil
}

// PngFormatter embeds chunk data in a PNG.
type PngFormatter struct{}

func (pf *PngFormatter) InitCollection(collectionPath string, collectionIndex int) error {
	return os.MkdirAll(collectionPath, 0755)
}
func (pf *PngFormatter) WriteChunk(collectionPath string, collectionIndex int, chunkIndex int, data []byte) error {
	base := filepath.Base(collectionPath)
	fname := fmt.Sprintf("IMG%s_%04d.PNG", base, chunkIndex)
	fp := filepath.Join(collectionPath, fname)
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		return fmt.Errorf("failed to create chunk directory: %w", err)
	}
	f, err := os.OpenFile(fp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open PNG file %s: %w", fp, err)
	}
	defer f.Close()

	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.Transparent)
	if err := encodePNGWithData(f, img, data); err != nil {
		f.Close()
		os.Remove(fp)
		return fmt.Errorf("failed to encode PNG with data for %s: %w", fp, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync PNG file: %w", err)
	}
	return nil
}
func (pf *PngFormatter) FinalizeCollection(collectionPath string, collectionIndex int) error {
	return nil
}

// encodePNGWithData injects 'data' into a custom 'rAWd' chunk in a minimal PNG.
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

func extractDataFromPNG(r io.Reader) ([]byte, error) {
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
