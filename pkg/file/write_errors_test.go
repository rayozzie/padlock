// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type tarWriteFailure struct{ err error }

func (w tarWriteFailure) Write([]byte) (int, error) { return 0, w.err }

func TestTarFinalizationFailureClosesFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "2A2.tar")
	writer, err := NewTarChunkWriter(ctx, path, "2A2", FormatBin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = FinalizeAllTarWriters(ctx) })
	failure := errors.New("injected archive flush failure")
	writer.tarWriter = tar.NewWriter(tarWriteFailure{failure})
	if err := writer.FinalizeTar(); !errors.Is(err, failure) {
		t.Errorf("FinalizeTar error = %v, want %v", err, failure)
	}
	if _, err := writer.tarFile.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Errorf("archive file remained open after failure: %v", err)
	}
	if _, err := NewTarChunkWriter(ctx, path, "2A2", FormatBin); !errors.Is(err, os.ErrExist) {
		t.Errorf("failed archive writer remained cached: %v", err)
	}
}

func TestFinalizeAllReportsErrorsAndClosesEveryFile(t *testing.T) {
	ctx := context.Background()
	failures := []error{errors.New("first archive failed"), errors.New("second archive failed"), nil}
	var writers []*TarChunkWriter
	t.Cleanup(func() { _ = FinalizeAllTarWriters(ctx) })
	for i, name := range []string{"2A3", "2B3", "2C3"} {
		writer, err := NewTarChunkWriter(ctx, filepath.Join(t.TempDir(), name+".tar"), name, FormatBin)
		if err != nil {
			t.Fatal(err)
		}
		if failures[i] != nil {
			writer.tarWriter = tar.NewWriter(tarWriteFailure{failures[i]})
		}
		writers = append(writers, writer)
	}
	err := FinalizeAllTarWriters(ctx)
	for _, failure := range failures[:2] {
		if !errors.Is(err, failure) {
			t.Errorf("finalization lost error %v: %v", failure, err)
		}
	}
	for _, writer := range writers {
		if _, err := writer.tarFile.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("archive %s remained open: %v", writer.TarPath, err)
		}
	}
}
