// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/pad"
)

// collectionChunkSizes encodes one input and returns the size of every chunk
// file in collection 2A3 together with the data length declared by the last
// chunk's header.
func collectionChunkSizes(t *testing.T, inputBytes int, chunkSize int) ([]int64, int) {
	t.Helper()
	root := t.TempDir()
	input, output := filepath.Join(root, "input"), filepath.Join(root, "output")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	// Incompressible content, so the compressed size tracks the input size.
	data := make([]byte, inputBytes)
	rand.New(rand.NewSource(int64(inputBytes))).Read(data)
	if err := os.WriteFile(filepath.Join(input, "source.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cfg := EncodeConfig{
		InputDir: input, OutputDir: output, N: 3, K: 2, Format: FormatBin, ChunkSize: chunkSize,
		RNG: pad.NewDefaultRand(ctx), Compression: CompressionGzip, ArchiveCollections: false,
	}
	if err := EncodeDirectory(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(output, "2A3"))
	if err != nil {
		t.Fatal(err)
	}
	var sizes []int64
	var last []byte
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, info.Size())
		if last, err = os.ReadFile(filepath.Join(output, "2A3", entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	header := string(last[1 : 1+int(last[0])])
	declared, err := strconv.Atoi(strings.Split(header, ":")[2])
	if err != nil {
		t.Fatalf("bad chunk header %q: %v", header, err)
	}
	return sizes, declared
}

func TestEncodeHidesExactCompressedLength(t *testing.T) {
	const chunkSize = 4096 // 2-of-3: 2048 input bytes per chunk
	inputChunk := chunkSize / 2
	// Inputs whose compressed archives differ in length but fit in the same
	// number of chunks must produce identical collection layouts, and every
	// chunk header must declare a full chunk.
	small, smallDeclared := collectionChunkSizes(t, 100, chunkSize)
	medium, mediumDeclared := collectionChunkSizes(t, 900, chunkSize)
	if fmt.Sprint(small) != fmt.Sprint(medium) {
		t.Fatalf("collection sizes expose the compressed length: %v vs %v", small, medium)
	}
	if smallDeclared != inputChunk || mediumDeclared != inputChunk {
		t.Fatalf("final chunk headers declare %d and %d bytes, want %d", smallDeclared, mediumDeclared, inputChunk)
	}
	if len(small) != 1 {
		t.Fatalf("expected a single chunk for small inputs, got %d", len(small))
	}
	// A larger input still grows by whole chunks only.
	large, largeDeclared := collectionChunkSizes(t, 5000, chunkSize)
	if len(large) != 3 || largeDeclared != inputChunk {
		t.Fatalf("5000 random bytes produced %d chunks (last declares %d), want 3 full chunks", len(large), largeDeclared)
	}
	for _, size := range large {
		if size != small[0] {
			t.Fatalf("chunk files differ in size: %v vs %v", large, small)
		}
	}
}
