// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rayozzie/padlock/internal/testarchive"
	"github.com/rayozzie/padlock/pkg/file"
)

func TestVerifyRejectsSparseTAR(t *testing.T) {
	for _, version := range []string{"gnu", "pax0.0", "pax0.1", "pax1.0"} {
		for _, entryName := range []string{"IMG2A2_0001.PNG", "ignored.txt"} {
			t.Run(version+"/"+entryName, func(t *testing.T) {
				archive := filepath.Join(t.TempDir(), "2A2.tar")
				if err := os.WriteFile(archive, testarchive.Sparse(entryName, 1<<20, version), 0600); err != nil {
					t.Fatal(err)
				}
				err := VerifyCollectionIntegrity(context.Background(), []file.Collection{
					{Name: "2A2", Path: archive, Format: FormatPNG},
				}, FormatPNG)
				if !errors.Is(err, file.ErrSparseTarEntry) {
					t.Fatalf("sparse archive accepted or wrong error: %v", err)
				}
			})
		}
	}
}
