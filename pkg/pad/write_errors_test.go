// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type failingChunkWriter struct {
	writes, closes int
	failWrite      int
	writeErr       error
	closeErr       error
}

func (w *failingChunkWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failWrite {
		return 0, w.writeErr
	}
	return len(p), nil
}

func (w *failingChunkWriter) Close() error {
	w.closes++
	return w.closeErr
}

func TestEncodePropagatesWriterErrors(t *testing.T) {
	writeErr := errors.New("injected write failure")
	closeErr := errors.New("injected flush failure")
	for _, tc := range []struct {
		name      string
		failWrite int
		writeErr  error
		closeErr  error
		want      error
	}{
		{name: "close", closeErr: closeErr, want: closeErr},
		{name: "header", failWrite: 1, writeErr: writeErr, want: writeErr},
		{name: "payload", failWrite: 2, writeErr: writeErr, want: writeErr},
		{name: "write_and_close", failWrite: 2, writeErr: writeErr, closeErr: closeErr, want: writeErr},
		{name: "short_header", failWrite: 1, want: io.ErrShortWrite},
		{name: "short_payload", failWrite: 2, want: io.ErrShortWrite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewPadForEncode(context.Background(), 2, 2)
			if err != nil {
				t.Fatal(err)
			}
			writer := &failingChunkWriter{failWrite: tc.failWrite, writeErr: tc.writeErr, closeErr: tc.closeErr}
			created := 0
			newChunk := func(string, int, string) (io.WriteCloser, error) {
				created++
				return writer, nil
			}
			err = p.Encode(context.Background(), 16, bytes.NewReader(bytes.Repeat([]byte("x"), 64)), NewTestRNG(0), newChunk, "bin")
			if !errors.Is(err, tc.want) {
				t.Errorf("Encode error = %v, want %v", err, tc.want)
			}
			if tc.closeErr != nil && !errors.Is(err, tc.closeErr) {
				t.Errorf("close error was lost: %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "2A2") {
				t.Errorf("error lacks collection context: %v", err)
			}
			if created != 1 {
				t.Errorf("encoding continued after failure: created %d writers", created)
			}
			if writer.closes != 1 {
				t.Errorf("writer closed %d times, want exactly once", writer.closes)
			}
		})
	}
}
