// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"context"
	"errors"
	"io"
)

// runDecodePipeline connects reconstruction to archive consumption. Keep the
// stage errors separate so the caller can report the decoding context. Always
// join the consumer before returning, including after failure or cancellation:
// closing a pipe does not mean buffered extraction and cleanup have finished.
func runDecodePipeline(ctx context.Context, decode func(io.Writer) error, consume func(io.Reader) error) (error, error) {
	if err := ctx.Err(); err != nil {
		return err, nil
	}
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	// Wake either end of a blocked pipe on cancellation. Filesystem operations
	// already in progress still have to return; do not abandon their goroutine.
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		defer close(cancelDone)
		pr.CloseWithError(ctx.Err())
		pw.CloseWithError(ctx.Err())
	})
	defer func() {
		if !stopCancel() {
			<-cancelDone
		}
	}()

	done := make(chan struct{})
	var consumeErr error
	go func() {
		defer close(done)
		defer pr.Close()
		consumeErr = consume(pr)
	}()
	decodeErr := decode(pw)
	pw.CloseWithError(decodeErr)
	<-done
	if err := ctx.Err(); err != nil {
		// A pipe close can surface as ErrClosedPipe; retain the cancellation or
		// deadline cause even if the consumer had already reached stream EOF.
		decodeErr = errors.Join(decodeErr, err)
	}
	return decodeErr, consumeErr
}
