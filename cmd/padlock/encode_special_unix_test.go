// Copyright 2025 Ray Ozzie. All rights reserved.

//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEncodeRejectsSpecialInputBeforeClearCLI(t *testing.T) {
	for _, kind := range []string{"fifo", "connected_fifo", "socket"} {
		for _, format := range []string{"bin", "png"} {
			for _, files := range []bool{false, true} {
				for _, dryRun := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/files=%t/dryrun=%t", kind, format, files, dryRun), func(t *testing.T) {
						// Keep the Unix socket path within sockaddr_un's small limit.
						base, err := os.MkdirTemp("", "pl3-")
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = os.RemoveAll(base) })
						input := filepath.Join(base, "in")
						nested := filepath.Join(input, "nested")
						if err := os.MkdirAll(nested, 0700); err != nil {
							t.Fatal(err)
						}
						path := filepath.Join(nested, "special")
						if kind == "socket" {
							listener, err := net.Listen("unix", path)
							if err != nil {
								t.Fatal(err)
							}
							defer listener.Close()
						} else {
							if err := syscall.Mkfifo(path, 0600); err != nil {
								t.Fatal(err)
							}
							if kind == "connected_fifo" {
								producer, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
								if err != nil {
									t.Fatal(err)
								}
								defer producer.Close()
							}
						}
						output, missing := filepath.Join(base, "out"), filepath.Join(base, "missing")
						if err := os.Mkdir(output, 0700); err != nil {
							t.Fatal(err)
						}
						marker := filepath.Join(output, "keep.txt")
						if err := os.WriteFile(marker, []byte("previous backup"), 0600); err != nil {
							t.Fatal(err)
						}
						// Multiple destinations: the first must not be cleared and
						// the second must not be created when preflight fails.
						args := []string{"encode", input, output, missing, "-required", "2", "-format", format, "-clear"}
						if files {
							args = append(args, "-files")
						}
						if dryRun {
							args = append(args, "-dryrun")
						}
						executable, err := os.Executable()
						if err != nil {
							t.Fatal(err)
						}
						// A subprocess deadline bounds the test even if FIFO open
						// becomes blocking again; no blocked goroutine survives it.
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						cmd := exec.CommandContext(ctx, executable, args...)
						cmd.Env = append(os.Environ(), "PADLOCK_CLI_TEST_HELPER=1")
						log, err := cmd.CombinedOutput()
						if ctx.Err() != nil {
							t.Fatalf("encode hung on %s: %v\n%s", kind, ctx.Err(), log)
						}
						var exitErr *exec.ExitError
						if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 ||
							!strings.Contains(string(log), "unsupported source file type") || !strings.Contains(string(log), path) {
							t.Errorf("expected clear unsupported-source failure: %v\n%s", err, log)
						}
						for _, success := range []string{"Encode complete", "Encoding completed successfully", "DRY RUN SIZE REPORT"} {
							if strings.Contains(string(log), success) {
								t.Errorf("reported success despite unsupported input: %s", log)
							}
						}
						if data, err := os.ReadFile(marker); err != nil || string(data) != "previous backup" {
							t.Errorf("preflight cleared previous backup: %q, %v", data, err)
						}
						if entries, err := os.ReadDir(output); err != nil || len(entries) != 1 {
							t.Errorf("preflight changed existing output: %v, %v", entries, err)
						}
						if _, err := os.Stat(missing); !os.IsNotExist(err) {
							t.Errorf("preflight created missing output: %v", err)
						}
					})
				}
			}
		}
	}
}
