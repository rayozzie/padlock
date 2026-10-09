// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
)

func TestMalformedPNGLengthsCLI(t *testing.T) {
	for _, tc := range []struct {
		name, message string
		length        uint32
		truncateCRC   bool
	}{
		{"max_int32", "invalid PNG chunk length", 0x7fffffff, false},
		{"high_bit_set", "invalid PNG chunk length", 0x80000000, false},
		{"max_uint32", "invalid PNG chunk length", 0xffffffff, false},
		{"truncated_crc", "no CRC found", 0, true},
	} {
		for _, archive := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tar=%t", tc.name, archive), func(t *testing.T) {
				input, _, _ := cliFixture(t, 1)
				encoded := encodeBackupDirectory(t, input, file.FormatPNG, false)
				firstChunk := filepath.Join(encoded, "2A3", "IMG2A3_0001.PNG")
				png, err := os.ReadFile(firstChunk)
				if err != nil {
					t.Fatal(err)
				}
				typePos := bytes.Index(png, []byte("rAWd"))
				if typePos < 4 {
					t.Fatal("fixture has no rAWd chunk")
				}
				if tc.truncateCRC {
					length := binary.BigEndian.Uint32(png[typePos-4 : typePos])
					png = png[:typePos+4+int(length)+3]
				} else {
					binary.BigEndian.PutUint32(png[typePos-4:typePos], tc.length)
				}
				if err := os.WriteFile(firstChunk, png, 0600); err != nil {
					t.Fatal(err)
				}
				if archive {
					for _, name := range []string{"2A3", "2B3", "2C3"} {
						dir := filepath.Join(encoded, name)
						if _, err := file.TarCollection(context.Background(), dir); err != nil {
							t.Fatal(err)
						}
						if err := os.RemoveAll(dir); err != nil {
							t.Fatal(err)
						}
					}
				}
				for _, dryRun := range []bool{false, true} {
					t.Run(fmt.Sprintf("dryrun=%t", dryRun), func(t *testing.T) {
						restored := t.TempDir()
						marker := filepath.Join(restored, "keep.txt")
						if err := os.WriteFile(marker, []byte("preserve this"), 0600); err != nil {
							t.Fatal(err)
						}
						args := []string{"decode", encoded, restored, "-clear"}
						if dryRun {
							args = append(args, "-dryrun")
						}
						output, err := runCLI(t, args...)
						var exitErr *exec.ExitError
						if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || strings.Contains(output, "panic:") {
							t.Fatalf("expected normal decode failure, got %v:\n%s", err, output)
						}
						if !strings.Contains(output, tc.message) {
							t.Fatalf("missing malformed-PNG diagnostic:\n%s", output)
						}
						got, readErr := os.ReadFile(marker)
						entries, listErr := os.ReadDir(restored)
						if readErr != nil || string(got) != "preserve this" || listErr != nil || len(entries) != 1 {
							t.Fatalf("malformed PNG changed the destination: %q, %v, %v, %v", got, readErr, entries, listErr)
						}
					})
				}
			})
		}
	}
}
