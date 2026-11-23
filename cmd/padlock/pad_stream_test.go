// Copyright 2026 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/cryptotest"
	"time"

	"github.com/rayozzie/padlock/pkg/pad"
)

func TestPadStreamXORWithNormalMixer(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	stream := make([]byte, 1024)
	for i := range stream {
		stream[i] = byte(i % 251)
	}
	path := filepath.Join(t.TempDir(), "stream")
	if err := os.WriteFile(path, append([]byte("header"), stream...), 0600); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(t.TempDir(), "seed")
	if err := os.WriteFile(seed, bytes.Repeat([]byte{0xa5}, pad.InitialEntropyBytes), 0600); err != nil {
		t.Fatal(err)
	}
	for _, seedFiles := range [][]string{nil, {seed}} {
		read := func(o entropyOptions) []byte {
			t.Helper()
			// Reproduce the entire local mixer, including its direct OS output.
			// The oracle is the ordinary CLI configuration with the same OS draws.
			cryptotest.SetGlobalRandom(t, 42)
			rng, closeSources, err := o.prepare(context.Background(), input, outputs, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer closeSources()
			got := make([]byte, len(stream))
			for _, p := range [][]byte{got[:3], got[3:500], got[500:]} {
				if err := rng.Read(context.Background(), p); err != nil {
					t.Fatal(err)
				}
			}
			return got
		}
		o := entropyOptions{seedFiles: seedFiles, timeout: time.Second}
		local := read(o)
		o.padStream, o.padSkip = path, 6
		mixed := read(o)
		for i, b := range stream {
			if mixed[i] != local[i]^b {
				t.Fatalf("wrong XOR at byte %d with %d seed sources", i, len(seedFiles))
			}
		}
	}
}

func TestPadStreamCLI(t *testing.T) {
	for _, tc := range []struct {
		copies, required string
		stdin            bool
	}{
		{"2", "2", false},
		{"5", "3", true},
	} {
		t.Run(tc.required+"of"+tc.copies, func(t *testing.T) {
			input, outputs, original := cliFixture(t, 1)
			path := filepath.Join(t.TempDir(), "pad.bin")
			options := []string{"-copies", tc.copies, "-required", tc.required, "-chunk", "96", "-files", "-format", "bin"}
			header := []byte("device header v1")
			options = append(options, "-pad-skip", fmt.Sprintf("%03d", len(header)))
			dryArgs := append([]string{"encode", input, "-dryrun", "-pad-stream", path}, options...)
			report, err := runCLI(t, dryArgs...)
			match := regexp.MustCompile(`Pad stream required:\s+([\d,]+) bytes`).FindStringSubmatch(report)
			if err != nil || len(match) != 2 {
				t.Fatalf("dry run with absent stream: %v\n%s", err, report)
			}
			count, err := strconv.Atoi(strings.ReplaceAll(match[1], ",", ""))
			if err != nil || count <= 96 || count > 1<<20 {
				t.Fatalf("unexpected pad size %q", match[1])
			}
			stream := make([]byte, count)
			for i := range stream {
				stream[i] = byte(i % 251)
			}
			deviceOutput := append(header, stream...)

			// Truncation on the very last pad byte must fail, even across chunks.
			shortArgs := append([]string{"encode", input, filepath.Join(t.TempDir(), "short"), "-pad-stream", "-"}, options...)
			out, err := runCLIWithStdin(t, bytes.NewReader(deviceOutput[:len(deviceOutput)-1]), shortArgs...)
			if err == nil || !strings.Contains(out, "EOF") || strings.Contains(out, "Encode complete") {
				t.Fatalf("short pad stream: %v\n%s", err, out)
			}

			source := path
			var stdin io.Reader
			if tc.stdin {
				source = "-"
				stdin = bytes.NewReader(deviceOutput)
				seed := filepath.Join(t.TempDir(), "seed")
				if err := os.WriteFile(seed, bytes.Repeat([]byte{0xa5}, pad.InitialEntropyBytes), 0600); err != nil {
					t.Fatal(err)
				}
				options = append(options, "-entropy-file", seed)
			} else if err := os.WriteFile(path, deviceOutput, 0600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"encode", input, outputs[0], "-pad-stream", source}, options...)
			if out, err := runCLIWithStdin(t, stdin, args...); err != nil {
				t.Fatalf("encode with exactly %d bytes: %v\n%s", count, err, out)
			}
			if !tc.stdin {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			restore := filepath.Join(t.TempDir(), "restored")
			if out, err := runCLI(t, "decode", outputs[0], restore); err != nil {
				t.Fatalf("restore without pad source: %v\n%s", err, out)
			}
			got, err := os.ReadFile(filepath.Join(restore, "source.bin"))
			if err != nil || !bytes.Equal(original, got) {
				t.Fatalf("restored file differs: %v", err)
			}
		})
	}
}

func TestPadStreamRejectsUnsafeInputsCLI(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	for _, tc := range []struct {
		flags []string
		want  string
	}{
		{[]string{"-pad-stream", filepath.Join(input, "source.bin")}, "outside"},
		{[]string{"-pad-stream", "-", "-entropy-file", "-"}, "duplicates"},
		{[]string{"-entropy-file", "-", "-pad-stream", "-", "-dryrun"}, "duplicates"},
		{[]string{"-pad-stream", "-", "-pad-stream", "-"}, "exactly one"},
		{[]string{"-pad-stream="}, "exactly one"},
		{[]string{"-pad-stream", "-", "-pad-skip", "-1"}, "nonnegative"},
		{[]string{"-pad-skip", "8", "-dryrun"}, "requires -pad-stream"},
		{[]string{"-pad-stream", "-", "extra", "-pad-skip", "64"}, "unexpected argument"},
	} {
		out, err := runCLI(t, append([]string{"encode", input, outputs[0]}, tc.flags...)...)
		if err == nil || !strings.Contains(out, tc.want) {
			t.Fatalf("%v: %v\n%s", tc.flags, err, out)
		}
		if _, err := os.Stat(outputs[0]); !os.IsNotExist(err) {
			t.Fatalf("%v created output: %v", tc.flags, err)
		}
	}
}
