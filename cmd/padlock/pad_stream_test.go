// Copyright 2026 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

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
			} else if err := os.WriteFile(path, deviceOutput, 0600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{"encode", input, outputs[0], "-pad-stream", source}, options...)
			if out, err := runCLIWithStdin(t, stdin, args...); err != nil {
				t.Fatalf("encode with exactly %d bytes: %v\n%s", count, err, out)
			}
			if tc.copies == "2" {
				// In a 2-of-2 backup, the B collection contains the pad verbatim.
				// Check every chunk to catch seed expansion, rewinds, or mixing.
				var pads []byte
				for i := 1; len(pads) < count; i++ {
					frame, err := os.ReadFile(filepath.Join(outputs[0], "2B2", fmt.Sprintf("2B2_%04d.bin", i)))
					if err != nil || len(frame) == 0 || int(frame[0])+1 >= len(frame) {
						t.Fatalf("read chunk %d: %v", i, err)
					}
					pads = append(pads, frame[1+int(frame[0]):]...)
				}
				if !bytes.Equal(pads, stream) {
					t.Fatal("collection pads differ from the supplied stream")
				}
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
		{[]string{"-pad-stream", "-", "-entropy-file", "-"}, "cannot be combined"},
		{[]string{"-entropy-url", "https://example.org", "-pad-stream", "-", "-dryrun"}, "cannot be combined"},
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
