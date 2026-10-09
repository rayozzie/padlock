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

	"github.com/rayozzie/padlock/pkg/file"
)

func TestDecodeRejectsSpecialCollectionInputCLI(t *testing.T) {
	for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
		input, _, _ := cliFixture(t, 1)
		backup := encodeBackupDirectory(t, input, format, false)
		for _, kind := range []string{"fifo", "connected_fifo", "socket", "symlink_fifo"} {
			for _, location := range []string{"archive", "chunk"} {
				for _, multiple := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/multiple=%t", format, kind, location, multiple), func(t *testing.T) {
						// Keep Unix socket names under sockaddr_un's small limit.
						root, err := os.MkdirTemp("", "pl-r3-")
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = os.RemoveAll(root) })
						encoded := filepath.Join(root, "in")
						if err := os.Mkdir(encoded, 0700); err != nil {
							t.Fatal(err)
						}
						for _, name := range []string{"2A3", "2B3", "2C3"} {
							copyBackupCollection(t, backup, encoded, name, false)
						}
						path := filepath.Join(encoded, "stray.tar")
						if location == "chunk" {
							filename := "2A3_0099.bin"
							if format == file.FormatPNG {
								filename = "IMG2A3_0099.PNG"
							}
							path = filepath.Join(encoded, "2A3", filename)
						}
						if kind == "socket" {
							listener, err := net.Listen("unix", path)
							if err != nil {
								t.Fatal(err)
							}
							defer listener.Close()
						} else {
							fifo := path
							if kind == "symlink_fifo" {
								fifo = filepath.Join(root, "pipe")
							}
							if err := syscall.Mkfifo(fifo, 0600); err != nil {
								t.Fatal(err)
							}
							if kind == "symlink_fifo" {
								if err := os.Symlink(fifo, path); err != nil {
									t.Fatal(err)
								}
							}
							if kind == "connected_fifo" {
								producer, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
								if err != nil {
									t.Fatal(err)
								}
								defer producer.Close()
							}
						}
						for _, dry := range []bool{false, true} {
							output := filepath.Join(root, fmt.Sprintf("out-%t", dry))
							if err := os.Mkdir(output, 0700); err != nil {
								t.Fatal(err)
							}
							marker := filepath.Join(output, "keep")
							if err := os.WriteFile(marker, []byte("keep me"), 0600); err != nil {
								t.Fatal(err)
							}
							inputs := []string{encoded}
							if multiple {
								// A later bad input must not be skipped just because
								// an earlier input already meets the threshold.
								inputs = []string{backup, encoded}
							}
							args := append([]string{"decode"}, inputs...)
							args = append(args, output, "-clear")
							if dry {
								args = append(args, "-dryrun")
							}
							executable, err := os.Executable()
							if err != nil {
								t.Fatal(err)
							}
							ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							cmd := exec.CommandContext(ctx, executable, args...)
							cmd.Env = append(os.Environ(), "PADLOCK_CLI_TEST_HELPER=1")
							out, err := cmd.CombinedOutput()
							deadline := ctx.Err()
							cancel()
							if deadline != nil {
								t.Fatalf("decode hung on %s: %v\n%s", path, deadline, out)
							}
							var exitErr *exec.ExitError
							if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(string(out), path) || !strings.Contains(string(out), "not a regular file") {
								t.Fatalf("missing special-input rejection: %v\n%s", err, out)
							}
							if got, err := os.ReadFile(marker); err != nil || string(got) != "keep me" {
								t.Fatalf("failure cleared destination: %q, %v", got, err)
							}
							if entries, err := os.ReadDir(output); err != nil || len(entries) != 1 {
								t.Fatalf("failure wrote output: %v, %v", entries, err)
							}
						}
					})
				}
			}
		}
	}
}
