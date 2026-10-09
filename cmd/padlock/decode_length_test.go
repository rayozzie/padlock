// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rayozzie/padlock/pkg/file"
)

func TestMalformedChunkLengthsCLI(t *testing.T) {
	ctx := context.Background()
	maxInt := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name, size, message string
		total               int
	}{
		{"overflow", strconv.Itoa(maxInt), "invalid chunk size", 3},
		{"missing_payload", strconv.Itoa(16 * 1024 * 1024), "unexpected EOF", 2},
		{"maximum_length", strconv.Itoa(maxInt), "unexpected EOF", 2},
		{"out_of_range", strconv.FormatUint(uint64(maxInt)+1, 10), "invalid chunk header", 3},
	} {
		for _, format := range []file.Format{file.FormatBin, file.FormatPNG} {
			for _, archive := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/tar=%t", tc.name, format, archive), func(t *testing.T) {
					root := t.TempDir()
					encoded, restored := filepath.Join(root, "encoded"), filepath.Join(root, "restored")
					for _, letter := range []string{"A", "B"} {
						name := fmt.Sprintf("2%s%d", letter, tc.total)
						header := name + ":1:" + tc.size
						data := append([]byte{byte(len(header))}, header...)
						dir := filepath.Join(encoded, name)
						if err := file.WriteNamedChunk(ctx, file.GetFormatter(format), dir, name, 1, data); err != nil {
							t.Fatal(err)
						}
						if archive {
							if _, err := file.TarCollection(ctx, dir); err != nil {
								t.Fatal(err)
							}
							if err := os.RemoveAll(dir); err != nil {
								t.Fatal(err)
							}
						}
					}
					output, err := runCLI(t, "decode", encoded, restored)
					var exitErr *exec.ExitError
					if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || strings.Contains(output, "panic:") {
						t.Fatalf("expected normal decode failure, got %v:\n%s", err, output)
					}
					if !strings.Contains(output, tc.message) || !strings.Contains(output, "2A") {
						t.Fatalf("missing malformed-header diagnostic:\n%s", output)
					}
					entries, err := os.ReadDir(restored)
					if (err != nil && !os.IsNotExist(err)) || len(entries) != 0 {
						t.Fatalf("malformed header wrote restored files: %v, err=%v", entries, err)
					}
				})
			}
		}
	}
}
