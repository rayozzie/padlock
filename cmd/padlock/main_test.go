// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run the real CLI in a subprocess so flag parsing and log.Fatal/os.Exit behave
// as they do in the installed executable, without terminating the test runner.
func TestMain(m *testing.M) {
	if os.Getenv("PADLOCK_CLI_TEST_HELPER") == "1" {
		// Trust only the local test server's certificate for HTTPS CLI tests.
		// This hook exists solely in the test binary; production uses OS roots.
		if path := os.Getenv("PADLOCK_CLI_TEST_CA"); path != "" {
			pem, err := os.ReadFile(path)
			roots := x509.NewCertPool()
			if err != nil || !roots.AppendCertsFromPEM(pem) {
				panic("invalid test TLS root")
			}
			http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
		}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = append(os.Environ(), "PADLOCK_CLI_TEST_HELPER=1")
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("CLI timed out: %v\n%s", args, output)
	}
	return string(output), err
}

func cliFixture(t *testing.T, outputCount int) (string, []string, []byte) {
	t.Helper()
	root := t.TempDir()
	input := filepath.Join(root, "input")
	if err := os.Mkdir(input, 0700); err != nil {
		t.Fatal(err)
	}
	data := append([]byte("data recoverable from the requested number of collections\n"), 0, 1, 127, 128, 255)
	if err := os.WriteFile(filepath.Join(input, "source.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	outputs := make([]string, outputCount)
	for i := range outputs {
		outputs[i] = filepath.Join(root, fmt.Sprintf("output%d", i+1))
	}
	return input, outputs, data
}

func TestEncodeRequiredFlag(t *testing.T) {
	for _, tc := range []struct {
		name        string
		outputCount int
		flags       []string
		copies      int
		required    int
	}{
		{"multiple_explicit_two", 3, []string{"-required", "2"}, 3, 2},
		{"multiple_equals_two", 3, []string{"-required=2"}, 3, 2},
		{"multiple_double_dash_two", 3, []string{"--required=2"}, 3, 2},
		{"multiple_last_value_two", 3, []string{"-required", "3", "-required", "2"}, 3, 2},
		{"multiple_invalid_then_valid", 3, []string{"-required", "1", "-required", "2"}, 3, 2},
		{"multiple_explicit_three", 4, []string{"-required", "3"}, 4, 3},
		{"multiple_omitted", 3, nil, 3, 3},
		{"two_outputs_omitted", 2, nil, 2, 2},
		{"single_omitted", 1, []string{"-copies", "3"}, 3, 2},
		{"single_explicit_two", 1, []string{"-copies", "3", "-required", "2"}, 3, 2},
		{"single_invalid_then_valid", 1, []string{"-copies", "3", "-required", "0", "-required", "2"}, 3, 2},
		{"copies_multiple_two", 2, []string{"-copies", "2"}, 2, 2},
		{"copies_multiple_three", 3, []string{"-copies=3", "-required", "2"}, 3, 2},
		{"copies_multiple_last_value_three", 3, []string{"-copies", "2", "-copies", "3"}, 3, 3},
		{"copies_single_two", 1, []string{"-copies", "2"}, 2, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, outputs, _ := cliFixture(t, tc.outputCount)
			args := append([]string{"encode", input}, outputs...)
			args = append(args, "-format", "bin")
			args = append(args, tc.flags...)
			if output, err := runCLI(t, args...); err != nil {
				t.Fatalf("encode failed: %v\n%s", err, output)
			}
			count := 0
			for _, dir := range outputs {
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				count += len(entries)
			}
			if count != tc.copies {
				t.Fatalf("created %d collections, want %d", count, tc.copies)
			}
			for i := 0; i < tc.copies; i++ {
				dir := outputs[0]
				if tc.outputCount > 1 {
					dir = outputs[i]
				}
				name := fmt.Sprintf("%d%c%d.tar", tc.required, 'A'+i, tc.copies)
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Errorf("expected %d-of-%d collection %s: %v", tc.required, tc.copies, name, err)
				}
			}
		})
	}
}

func TestEncodeCopiesMismatchLeavesOutputsUntouched(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flags  []string
		copies int
	}{
		{"explicit_two", []string{"-copies", "2"}, 2},
		{"equals_two", []string{"-copies=2"}, 2},
		{"double_dash_two", []string{"--copies=2"}, 2},
		{"last_value_two", []string{"-copies", "3", "-copies", "2"}, 2},
		{"nondefault_four", []string{"-copies", "4"}, 4},
	} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dryrun=%t", tc.name, dryRun), func(t *testing.T) {
				input, outputs, _ := cliFixture(t, 3)
				// Include both existing and nonexistent output directories.
				for i, dir := range outputs {
					if i == 1 {
						continue
					}
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("existing backup"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				args := append([]string{"encode", input}, outputs...)
				args = append(args, "-clear", "-format", "bin")
				args = append(args, tc.flags...)
				if dryRun {
					args = append(args, "-dryrun")
				}
				output, err := runCLI(t, args...)
				var exitErr *exec.ExitError
				message := fmt.Sprintf("Number of output directories (3) does not match -copies value (%d)", tc.copies)
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(output, message) || strings.Contains(output, "panic:") {
					t.Fatalf("conflicting -copies was not rejected clearly: %v\n%s", err, output)
				}
				for i, dir := range outputs {
					if i == 1 {
						if _, err := os.Stat(dir); !os.IsNotExist(err) {
							t.Fatalf("mismatched -copies created an output directory: %v", err)
						}
						continue
					}
					data, err := os.ReadFile(filepath.Join(dir, "keep.txt"))
					if err != nil || string(data) != "existing backup" {
						t.Fatalf("mismatched -copies cleared existing output: %v", err)
					}
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 1 {
						t.Fatalf("mismatched -copies wrote output: %v; %v", entries, err)
					}
				}
			})
		}
	}
}

func TestRequiredTwoRestoresWithoutThirdCollection(t *testing.T) {
	for _, individualFiles := range []bool{false, true} {
		t.Run(fmt.Sprintf("files=%t", individualFiles), func(t *testing.T) {
			input, outputs, want := cliFixture(t, 3)
			args := append([]string{"encode", input}, outputs...)
			args = append(args, "-required", "2", "-format", "bin")
			if individualFiles {
				args = append(args, "-files")
			}
			if output, err := runCLI(t, args...); err != nil {
				t.Fatalf("encode failed: %v\n%s", err, output)
			}
			// Remove collection B; the requested threshold must permit A + C.
			if err := os.RemoveAll(outputs[1]); err != nil {
				t.Fatal(err)
			}
			restored := filepath.Join(filepath.Dir(input), "restored")
			if output, err := runCLI(t, "decode", outputs[0], outputs[2], restored); err != nil {
				t.Fatalf("two collections could not restore a requested 2-of-3 backup: %v\n%s", err, output)
			}
			got, err := os.ReadFile(filepath.Join(restored, "source.bin"))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("restored data differs: got %q, want %q; err=%v", got, want, err)
			}
		})
	}
}

func TestRequiredTwoDryRun(t *testing.T) {
	input, outputs, _ := cliFixture(t, 3)
	args := append([]string{"encode", input}, outputs...)
	args = append(args, "-required", "2", "-dryrun")
	output, err := runCLI(t, args...)
	if err != nil || !strings.Contains(output, "with 3 output directories -required 2") {
		t.Fatalf("dry run did not use the requested threshold: %v\n%s", err, output)
	}
	for _, dir := range outputs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("dry run created output directory %s: %v", dir, err)
		}
	}
}

func TestDecodeCollectionOrderCLI(t *testing.T) {
	for _, format := range []string{"bin", "png"} {
		for _, individualFiles := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/files=%t", format, individualFiles), func(t *testing.T) {
				input, outputs, want := cliFixture(t, 3)
				args := append([]string{"encode", input}, outputs...)
				args = append(args, "-required", "2", "-format", format, "-chunk", "128")
				if individualFiles {
					args = append(args, "-files")
				}
				if output, err := runCLI(t, args...); err != nil {
					t.Fatalf("encode failed: %v\n%s", err, output)
				}
				for _, order := range [][]int{{2, 0}, {2, 1, 0}} {
					t.Run(fmt.Sprint(order), func(t *testing.T) {
						restored := filepath.Join(t.TempDir(), "restored")
						args := []string{"decode"}
						for _, i := range order {
							args = append(args, outputs[i])
						}
						args = append(args, restored)
						if output, err := runCLI(t, args...); err != nil {
							t.Fatalf("decode failed with collection order %v: %v\n%s", order, err, output)
						}
						got, err := os.ReadFile(filepath.Join(restored, "source.bin"))
						if err != nil || !bytes.Equal(got, want) {
							t.Fatalf("restored data differs: got %q, want %q; err=%v", got, want, err)
						}
					})
				}
			})
		}
	}
}

func TestRequiredAboveCopiesIsRejected(t *testing.T) {
	input, outputs, _ := cliFixture(t, 3)
	args := append([]string{"encode", input}, outputs...)
	args = append(args, "-required", "4")
	output, err := runCLI(t, args...)
	if err == nil || !strings.Contains(output, "cannot be greater than number of collections") {
		t.Fatalf("invalid threshold was not rejected: %v\n%s", err, output)
	}
	for _, dir := range outputs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("invalid threshold created output directory %s: %v", dir, err)
		}
	}
}
