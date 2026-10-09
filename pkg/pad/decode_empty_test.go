// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func TestDecodeRejectsNoChunks(t *testing.T) {
	for _, count := range []int{0, 2} {
		p, err := NewPadForDecode(context.Background(), 2)
		if err != nil {
			t.Fatal(err)
		}
		readers := make([]io.Reader, count)
		for i := range readers {
			readers[i] = bytes.NewReader(nil)
		}
		var output bytes.Buffer
		if err := p.Decode(context.Background(), readers, &output); err == nil {
			t.Errorf("decoding %d empty collections reported success", count)
		}
		if output.Len() != 0 {
			t.Errorf("decoding empty collections wrote %d bytes", output.Len())
		}
	}
}
