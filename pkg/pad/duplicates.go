// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"io"
)

// compareDuplicatePayload consumes exactly the expected payload, comparing it
// without another whole-chunk allocation. The caller reuses a nonempty scratch
// buffer across all duplicates. Headers and chunk boundaries are checked by Decode.
func compareDuplicatePayload(ctx context.Context, reader io.Reader, expected, scratch []byte) (bool, error) {
	emptyReads := 0
	for len(expected) > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, err := reader.Read(scratch[:min(len(scratch), len(expected))])
		// Preserve read failures even when returned alongside the final bytes.
		if err != nil && err != io.EOF {
			return false, err
		}
		if !bytes.Equal(scratch[:n], expected[:n]) {
			return false, nil
		}
		expected = expected[n:]
		if err == io.EOF && len(expected) > 0 {
			return false, io.ErrUnexpectedEOF
		}
		if n > 0 {
			emptyReads = 0
		} else {
			emptyReads++
			if emptyReads >= 100 {
				return false, io.ErrNoProgress
			}
		}
	}
	return true, nil
}
