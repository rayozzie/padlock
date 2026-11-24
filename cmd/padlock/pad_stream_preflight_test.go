// Copyright 2026 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"encoding/base64"
	mrand "math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A healthy source that never stalls must not be stopped because
// one chunk-sized request takes longer than -entropy-timeout.
func TestPadStreamSteadySlowSourceCompletes(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	go func() {
		// 256 bytes every 50 ms: the 4,096-byte pad request needs about 0.8 s,
		// but the source is never idle for longer than 50 ms.
		src := mrand.New(mrand.NewSource(1))
		piece := make([]byte, 256)
		for i := 0; i < 16; i++ {
			src.Read(piece)
			if _, err := w.Write(piece); err != nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	out, err := runCLIWithStdin(t, r, "encode", input, outputs[0], "-files", "-format", "bin",
		"-chunk", "4096", "-pad-stream", "-", "-entropy-timeout", "300ms")
	if err != nil || !strings.Contains(out, "Encode complete") {
		t.Fatalf("steady source was stopped: %v\n%s", err, out)
	}
}

// A dead or text-encoded source must be rejected before -clear
// removes an existing backup.
func TestPadStreamUnhealthyStartRejectedBeforeClear(t *testing.T) {
	// Zeros fail the repetition test at byte 13. Octal text fails the byte
	// concentration test at byte 281. Both lie inside the first 512-byte window
	// and inside the 4,096 pad bytes that this backup needs anyway.
	for name, data := range map[string][]byte{
		"zeros":      make([]byte, 1<<16),
		"octal text": []byte(strings.Repeat("01234567", 1<<13)),
	} {
		t.Run(name, func(t *testing.T) {
			input, outputs, _ := cliFixture(t, 1)
			if err := os.Mkdir(outputs[0], 0700); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(outputs[0], "previous-backup")
			if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := runCLIWithStdin(t, bytes.NewReader(data), "encode", input, outputs[0], "-clear", "-chunk", "4096", "-pad-stream", "-")
			if err == nil || !strings.Contains(out, "health check failed") {
				t.Fatalf("unhealthy stream accepted: %v\n%s", err, out)
			}
			if got, err := os.ReadFile(keep); err != nil || string(got) != "keep" {
				t.Fatalf("previous output was cleared before the stream was checked: %v", err)
			}
		})
	}
}

// Constant bit positions must stop the stream at the first full window,
// whichever way they are stuck. Base64 text keeps bit 7 at zero forever.
func TestPadStreamHealthRejectsConstantBitPositions(t *testing.T) {
	raw := make([]byte, 48*1024)
	mrand.New(mrand.NewSource(3)).Read(raw)
	text := []byte(base64.StdEncoding.EncodeToString(raw))
	balanced := bytes.Clone(raw)
	for i := range balanced {
		balanced[i] = balanced[i]&0x3f | 0x40 // bit 7 stuck at 0, bit 6 stuck at 1
	}
	for name, data := range map[string][]byte{"base64": text, "two opposite stuck bits": balanced} {
		var h padStreamHealth
		if err := h.check(data[:padWindow]); err == nil {
			t.Errorf("%s: %d bytes with constant bit positions passed", name, padWindow)
		}
	}
	// Uniform bytes must still pass.
	var h padStreamHealth
	if err := h.check(raw); err != nil {
		t.Fatalf("uniform bytes rejected: %v", err)
	}
}

func TestPadStreamStartupStallPreservesOutput(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	if err := os.Mkdir(outputs[0], 0700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(outputs[0], "previous")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	// Pass the signature probe, then leave the pipe open without more bytes.
	if _, err := w.Write([]byte("abcdefghijk")); err != nil {
		t.Fatal(err)
	}
	out, err := runCLIWithStdin(t, r, "encode", input, outputs[0], "-clear", "-pad-stream", "-", "-entropy-timeout", "200ms")
	if err == nil || !strings.Contains(out, "deadline exceeded") {
		t.Fatalf("stalled preflight: %v\n%s", err, out)
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "keep" {
		t.Fatalf("previous output changed: %v", err)
	}
}
