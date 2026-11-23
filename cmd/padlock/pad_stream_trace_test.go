// Copyright 2026 Ray Ozzie. All rights reserved.

//go:build unix

package main

import (
	"bytes"
	"context"
	"fmt"
	mrand "math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/cryptotest"
	"time"

	"github.com/rayozzie/padlock/pkg/pad"
	"github.com/rayozzie/padlock/pkg/padlock"
)

func TestPadStreamBytesReachCollections(t *testing.T) {
	src := mrand.New(mrand.NewSource(99))
	stream := make([]byte, 1<<16)
	src.Read(stream)
	header := []byte("vendor header: 23 bytes")

	for _, tc := range []struct {
		n, k, chunk int
		seeded      bool
		fifo        bool
	}{
		{2, 2, 97, false, false},
		{5, 3, 96, true, false},
		{3, 2, 4, false, true}, // small reads straddle the prechecked startup bytes
	} {
		t.Run(fmt.Sprintf("%dof%d_chunk%d", tc.k, tc.n, tc.chunk), func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "input")
			if err := os.Mkdir(input, 0700); err != nil {
				t.Fatal(err)
			}
			content := make([]byte, 40)
			src.Read(content)
			if err := os.WriteFile(filepath.Join(input, "f"), content, 0600); err != nil {
				t.Fatal(err)
			}
			o := entropyOptions{timeout: 20 * time.Second}
			if tc.seeded {
				seed := filepath.Join(dir, "seed")
				if err := os.WriteFile(seed, bytes.Repeat([]byte{0xa5}, pad.InitialEntropyBytes), 0600); err != nil {
					t.Fatal(err)
				}
				o.seedFiles = []string{seed}
			}
			encode := func(o entropyOptions, out string) {
				t.Helper()
				cryptotest.SetGlobalRandom(t, 7) // identical OS draws for both runs
				ctx := context.Background()
				cfg := padlock.EncodeConfig{InputDir: input, OutputDir: out, OutputDirs: []string{out},
					N: tc.n, K: tc.k, Format: padlock.FormatBin, ChunkSize: tc.chunk, Compression: padlock.CompressionGzip}
				rng, closeSources, err := o.prepare(ctx, cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer closeSources()
				cfg.RNG = rng
				err = padlock.EncodeDirectory(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
			}
			local, mixed := filepath.Join(dir, "local"), filepath.Join(dir, "mixed")
			encode(o, local)

			path := filepath.Join(dir, "stream")
			device := append(bytes.Clone(header), stream...)
			if tc.fifo {
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
				producer, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer producer.Close()
				go func() { // short, uneven writes with pauses that empty the pipe
					w := mrand.New(mrand.NewSource(5))
					for data := device[:4096]; len(data) > 0; {
						n := min(1+w.Intn(7), len(data))
						if _, err := producer.Write(data[:n]); err != nil {
							return
						}
						data = data[n:]
						if w.Intn(4) == 0 {
							time.Sleep(time.Millisecond)
						}
					}
				}()
			} else if err := os.WriteFile(path, device, 0600); err != nil {
				t.Fatal(err)
			}
			o.padStream, o.padSkip = path, int64(len(header))
			encode(o, mixed)

			// Collection i stores its pads first: one piece for every combination
			// in which an earlier collection holds the XOR result.
			choose := func(n, k int) int {
				if k < 0 || k > n {
					return 0
				}
				r := 1
				for i := 0; i < k; i++ {
					r = r * (n - i) / (i + 1)
				}
				return r
			}
			pieces := choose(tc.n-1, tc.k-1)
			payload := func(out, coll string, chunk int) (int, []byte, bool) {
				data, err := os.ReadFile(filepath.Join(out, coll, fmt.Sprintf("%s_%04d.bin", coll, chunk)))
				if os.IsNotExist(err) {
					return 0, nil, false
				}
				if err != nil {
					t.Fatal(err)
				}
				fields := strings.Split(string(data[1:1+int(data[0])]), ":")
				size, err := strconv.Atoi(fields[2])
				if err != nil {
					t.Fatal(err)
				}
				return size, data[1+int(data[0]):], true
			}
			offset, chunk := 0, 1
			for ; ; chunk++ {
				found := false
				for i := 0; i < tc.n; i++ {
					coll := fmt.Sprintf("%d%c%d", tc.k, 'A'+i, tc.n)
					size, a, ok := payload(local, coll, chunk)
					_, b, ok2 := payload(mixed, coll, chunk)
					if ok != ok2 {
						t.Fatalf("chunk %d exists in only one run", chunk)
					}
					if !ok {
						continue
					}
					found = true
					padBytes := (pieces - choose(tc.n-i-1, tc.k-1)) * size
					for j := 0; j < padBytes; j++ {
						if a[j]^b[j] != stream[offset+j] {
							t.Fatalf("chunk %d collection %s pad byte %d is not stream[%d] XOR normal mixer", chunk, coll, j, offset+j)
						}
					}
					offset += padBytes
				}
				if !found {
					break
				}
			}
			want := (chunk - 1) * (tc.chunk / pieces) * (tc.k - 1) * choose(tc.n, tc.k)
			if offset == 0 || offset != want {
				t.Fatalf("verified %d stream bytes, expected %d", offset, want)
			}
		})
	}
}
