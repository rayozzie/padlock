// Copyright 2026 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPadStreamHealthAcrossReads(t *testing.T) {
	var runs padStreamHealth
	if err := runs.check(bytes.Repeat([]byte{0}, 12)); err != nil {
		t.Fatal(err)
	}
	if err := runs.check([]byte{0}); err == nil || !strings.Contains(err.Error(), "repeated byte") {
		t.Fatalf("13th identical byte: %v", err)
	}

	// Concentrate 35 occurrences at the end of an otherwise balanced window.
	// The next occurrence can only be detected by spanning the window boundary.
	first := make([]byte, padWindow)
	for i := range first {
		first[i] = byte(1 + i%255)
	}
	for i := 0; i < 35; i++ {
		first[padWindow-2*(i+1)] = 0
	}
	var frequency padStreamHealth
	if err := frequency.check(first); err != nil {
		t.Fatal(err)
	}
	if err := frequency.check([]byte{0}); err == nil || !strings.Contains(err.Error(), "byte frequency") {
		t.Fatalf("36th occurrence across boundary: %v", err)
	}
	// Move one zero to the byte being evicted before adding the new zero.
	first[0], first[padWindow-2] = first[padWindow-2], first[0]
	frequency = padStreamHealth{}
	if err := frequency.check(append(first, 0)); err != nil {
		t.Fatalf("expired sample was still counted: %v", err)
	}
}

func TestPadStreamBitBalanceCutoffs(t *testing.T) {
	var three, four []byte
	for i := 0; i < 256; i++ {
		switch bits.OnesCount8(byte(i)) {
		case 3:
			three = append(three, byte(i))
		case 4:
			four = append(four, byte(i))
		}
	}
	for _, ones := range []int{1680, 1681} {
		for _, mask := range []byte{0, 0xff} {
			window := make([]byte, padWindow)
			for i := range window {
				b := three[i%len(three)]
				if i < ones-3*padWindow {
					b = four[i%len(four)]
				}
				window[i] = b ^ mask
			}
			var h padStreamHealth
			if err := h.check(window[:padWindow-1]); err != nil {
				t.Fatal(err)
			}
			err := h.check(window[padWindow-1:])
			if ones == 1681 && err != nil || ones == 1680 && (err == nil || !strings.Contains(err.Error(), "bit imbalance")) {
				t.Fatalf("ones=%d, mask=%d: %v", ones, mask, err)
			}
		}
	}
}

func TestPadStreamStartupRejectsHeaderBeforeClear(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	if err := os.Mkdir(outputs[0], 0700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(outputs[0], "previous-backup")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ data, skip, want string }{
		{"\x89PNG\r\n\x1a\n", "0", "recognized file or protocol header"},
		{"12345PK\x03\x04abcd", "5", "recognized file or protocol header"},
		{"partial", "10000000000", "skipping pad stream header"},
	} {
		out, err := runCLIWithStdin(t, strings.NewReader(tc.data), "encode", input, outputs[0], "-clear", "-pad-stream", "-", "-pad-skip", tc.skip)
		if err == nil || !strings.Contains(out, tc.want) {
			t.Fatalf("skip=%s: %v\n%s", tc.skip, err, out)
		}
		if got, err := os.ReadFile(keep); err != nil || string(got) != "keep" {
			t.Fatalf("previous output changed: %v", err)
		}
	}
}

func TestPadStreamHealthAbortsBeforeAffectedChunk(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	stream := make([]byte, 96)
	for i := range stream {
		stream[i] = byte(i)
	}
	stream = append(stream, bytes.Repeat([]byte{0xa5}, 13)...)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close() // Keep the pipe open so a reader waiting for more bytes would time out.
	if _, err := w.Write(stream); err != nil {
		t.Fatal(err)
	}
	out, err := runCLIWithStdin(t, r, "encode", input, outputs[0], "-files", "-format", "bin", "-chunk", "96", "-pad-stream", "-", "-entropy-timeout", "2s")
	if err == nil || !strings.Contains(out, "health check failed: repeated byte") || strings.Contains(out, "Encode complete") {
		t.Fatalf("bad stream was not stopped at the failure: %v\n%s", err, out)
	}
	dir := filepath.Join(outputs[0], "2A2")
	if _, err := os.Stat(filepath.Join(dir, "2A2_0001.bin")); err != nil {
		t.Fatalf("missing earlier chunk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "2A2_0002.bin")); !os.IsNotExist(err) {
		t.Fatalf("failing chunk was written: %v", err)
	}
}
