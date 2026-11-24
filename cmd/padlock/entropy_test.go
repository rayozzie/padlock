// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rayozzie/padlock/pkg/pad"
)

func TestEntropyFileReadsAndTimeout(t *testing.T) {
	t.Run("fresh bytes and exhaustion", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "seed")
		data := make([]byte, pad.InitialEntropyBytes+44)
		for i := range data {
			data[i] = byte(i)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		f, err := openEntropyFile(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		source := &fileEntropySource{name: "seed file", file: f, timeout: time.Second}
		offset := 0
		for _, size := range []int{pad.InitialEntropyBytes, 44} {
			got := make([]byte, size)
			if err := source.Read(context.Background(), got); err != nil || !bytes.Equal(got, data[offset:offset+size]) {
				t.Fatalf("file read reused or changed data: %v", err)
			}
			offset += size
		}
		for range 2 {
			got := []byte{0xa5}
			if err := source.Read(context.Background(), got); !errors.Is(err, io.EOF) || got[0] != 0xa5 {
				t.Fatalf("exhausted file returned data or was reopened: %v", err)
			}
		}
	})
	t.Run("pipe timeout", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		source := &fileEntropySource{name: "pipe", file: r, timeout: 25 * time.Millisecond}
		want := bytes.Repeat([]byte{0xa5}, 76)
		got := bytes.Clone(want)
		if err := source.Read(context.Background(), got); !errors.Is(err, context.DeadlineExceeded) || !bytes.Equal(got, want) {
			t.Fatalf("blocked pipe did not time out cleanly: %v", err)
		}
		if err := source.Read(context.Background(), got); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("failed source was reused: %v", err)
		}
	})
}

func TestEntropyFilesRoundTripCLI(t *testing.T) {
	for _, format := range []string{"bin", "png"} {
		for _, files := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/files=%t", format, files), func(t *testing.T) {
				input, outputs, want := cliFixture(t, 1)
				args := []string{"encode", input, outputs[0], "-format", format, "-copies", "3", "-required", "2"}
				if files {
					args = append(args, "-files")
				}
				var seedPaths []string
				for i := 0; i < 2; i++ {
					path := filepath.Join(t.TempDir(), "seed")
					if err := os.WriteFile(path, bytes.Repeat([]byte{byte(i + 1)}, pad.InitialEntropyBytes), 0600); err != nil {
						t.Fatal(err)
					}
					seedPaths = append(seedPaths, path)
					args = append(args, "-entropy-file", path)
				}
				if out, err := runCLI(t, args...); err != nil || !strings.Contains(out, "2 additional source(s)") {
					t.Fatalf("external entropy encode failed: %v\n%s", err, out)
				}
				for _, path := range seedPaths {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				restored := filepath.Join(t.TempDir(), "restored")
				if out, err := runCLI(t, "decode", outputs[0], restored); err != nil {
					t.Fatalf("decode required external seed inputs: %v\n%s", err, out)
				}
				got, err := os.ReadFile(filepath.Join(restored, "source.bin"))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("restored data differs: %v", err)
				}
			})
		}
	}
}

func TestEntropyStdinCLI(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "encode", input, outputs[0], "-format", "bin", "-entropy-file", "-")
	cmd.Env = append(os.Environ(), "PADLOCK_CLI_TEST_HELPER=1")
	cmd.Stdin = bytes.NewReader(bytes.Repeat([]byte{0x37}, pad.InitialEntropyBytes))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stdin entropy encode failed: %v\n%s", err, out)
	}
}

func TestEntropyFailurePreservesOutputCLI(t *testing.T) {
	for _, kind := range []string{"short", "missing", "duplicate", "hardlink_duplicate", "pad_stream_alias", "inside_input", "inside_output", "input_hardlink", "removed_url", "bad_timeout"} {
		t.Run(kind, func(t *testing.T) {
			input, outputs, _ := cliFixture(t, 1)
			if err := os.Mkdir(outputs[0], 0700); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(outputs[0], "keep")
			if err := os.WriteFile(keep, []byte("existing backup"), 0600); err != nil {
				t.Fatal(err)
			}
			seed := filepath.Join(t.TempDir(), "seed")
			if err := os.WriteFile(seed, make([]byte, pad.InitialEntropyBytes), 0600); err != nil {
				t.Fatal(err)
			}
			flags := []string{"-entropy-file", seed}
			switch kind {
			case "short":
				if err := os.Truncate(seed, pad.InitialEntropyBytes-1); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(seed); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				flags = append(flags, "-entropy-file", seed)
			case "hardlink_duplicate", "pad_stream_alias", "input_hardlink":
				alias := filepath.Join(t.TempDir(), "alias")
				if kind == "input_hardlink" {
					alias = filepath.Join(input, "seed")
				}
				if err := os.Link(seed, alias); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
				if kind == "hardlink_duplicate" {
					flags = append(flags, "-entropy-file", alias)
				} else if kind == "pad_stream_alias" {
					flags = append(flags, "-pad-stream", alias)
				}
			case "inside_input":
				flags = []string{"-entropy-file", filepath.Join(input, "source.bin")}
			case "inside_output":
				flags = []string{"-entropy-file", keep}
			case "removed_url":
				flags = []string{"-entropy-url=https://example.invalid/?SECRET-TOKEN"}
			case "bad_timeout":
				flags = append(flags, "-entropy-timeout", "0")
			}
			args := append([]string{"encode", input, outputs[0], "-clear", "-format", "bin"}, flags...)
			out, err := runCLI(t, args...)
			if err == nil || !strings.Contains(out, "entropy") || strings.Contains(out, "panic:") || strings.Contains(out, "SECRET-TOKEN") {
				t.Fatalf("invalid entropy was not rejected cleanly: %v\n%s", err, out)
			}
			if kind == "removed_url" && !strings.Contains(out, "flag provided but not defined: -entropy-url") {
				t.Fatalf("expected removed flag to be rejected: %s", out)
			}
			if kind == "pad_stream_alias" && !strings.Contains(out, "same file/device/pipe") {
				t.Fatalf("expected source identity check: %s", out)
			}
			got, err := os.ReadFile(keep)
			if err != nil || string(got) != "existing backup" {
				t.Fatalf("entropy failure cleared existing output: %v", err)
			}
		})
	}
}

func TestEntropyDryRunDoesNotAccessSourcesCLI(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	args := []string{"encode", input, outputs[0], "-dryrun", "-entropy-file", filepath.Join(t.TempDir(), "missing"), "-entropy-file", "-"}
	if out, err := runCLI(t, args...); err != nil || !strings.Contains(out, "not opened or consumed") {
		t.Fatalf("dry run tried to consume an external source: %v\n%s", err, out)
	}
	if _, err := os.Stat(outputs[0]); !os.IsNotExist(err) {
		t.Fatalf("dry run created output directory: %v", err)
	}
}
