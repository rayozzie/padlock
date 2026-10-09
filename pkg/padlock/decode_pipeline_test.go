// Copyright 2025 Ray Ozzie. All rights reserved.

package padlock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rayozzie/padlock/pkg/trace"
)

func TestDecodePipelineWaitsForSlowCompletion(t *testing.T) {
	for _, environment := range []string{"normal", "GO_TEST", "tracer_prefix"} {
		for _, decodeFails := range []bool{false, true} {
			for _, cleanupFails := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/decode_error=%t/cleanup_error=%t", environment, decodeFails, cleanupFails), func(t *testing.T) {
					t.Setenv("GO_TEST", "")
					if environment == "GO_TEST" {
						t.Setenv("GO_TEST", "1")
					}
					synctest.Test(t, func(t *testing.T) {
						ctx := context.Background()
						if environment == "tracer_prefix" {
							ctx = trace.WithContext(ctx, trace.NewTracer("TEST-restore", trace.LogLevelNormal))
						}
						var sourceErr, cleanupErr error
						if decodeFails {
							sourceErr = errors.New("collection failed after decoded data")
						}
						if cleanupFails {
							cleanupErr = errors.New("permission restoration failed")
						}
						delay := 35 * time.Second
						if environment != "normal" {
							delay = 5 * time.Second
						}
						finished := make(chan struct{})
						decodeErr, consumeErr := runDecodePipeline(ctx, func(w io.Writer) error {
							if _, err := io.WriteString(w, "decoded archive"); err != nil {
								return err
							}
							return sourceErr
						}, func(r io.Reader) error {
							defer close(finished)
							data, err := io.ReadAll(r)
							if string(data) != "decoded archive" || !errors.Is(err, sourceErr) {
								t.Errorf("archive input = %q, %v; want complete data and %v", data, err, sourceErr)
							}
							// Model buffered extraction or directory permission cleanup
							// after reconstruction stops. synctest advances fake time.
							time.Sleep(delay)
							return errors.Join(err, cleanupErr)
						})
						select {
						case <-finished:
						default:
							t.Error("decode returned while archive cleanup was still running")
						}
						if !errors.Is(decodeErr, sourceErr) {
							t.Errorf("decode error = %v, want %v", decodeErr, sourceErr)
						}
						if cleanupErr != nil && !errors.Is(consumeErr, cleanupErr) {
							t.Errorf("archive error = %v, want late cleanup error %v", consumeErr, cleanupErr)
						}
						if sourceErr == nil && cleanupErr == nil && consumeErr != nil {
							t.Errorf("successful archive reported %v", consumeErr)
						}
						// Even the failing regression must finish its delayed goroutine.
						<-finished
					})
				})
			}
		}
	}
}

func TestDecodePipelineCancellationUnblocksPipeAndJoinsCleanup(t *testing.T) {
	for _, blocked := range []string{"reader", "writer"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%t", blocked, deadline), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					want := context.Canceled
					if deadline {
						cancel()
						ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
						want = context.DeadlineExceeded
					}
					defer cancel()
					unblocked, releaseDecode := make(chan struct{}), make(chan struct{})
					releaseCleanup, finished, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
					var decodeErr error
					go func() {
						defer close(returned)
						decodeErr, _ = runDecodePipeline(ctx, func(w io.Writer) error {
							if blocked == "reader" {
								<-releaseDecode
								return ctx.Err()
							}
							defer close(unblocked)
							_, err := io.WriteString(w, "blocked output")
							return err
						}, func(r io.Reader) error {
							defer close(finished)
							if blocked == "reader" {
								_, _ = io.Copy(io.Discard, r)
								close(unblocked)
							} else {
								<-ctx.Done()
							}
							<-releaseCleanup
							return ctx.Err()
						})
					}()
					synctest.Wait() // The selected pipe end is now blocked.
					if deadline {
						time.Sleep(5 * time.Second)
					} else {
						cancel()
					}
					synctest.Wait()
					select {
					case <-unblocked:
					default:
						t.Error("cancellation did not unblock pipe I/O")
					}
					close(releaseDecode)
					synctest.Wait()
					select {
					case <-returned:
						t.Error("canceled pipeline returned before consumer cleanup")
					default:
					}
					close(releaseCleanup)
					<-returned
					if !errors.Is(decodeErr, want) {
						t.Errorf("decode error = %v, want %v", decodeErr, want)
					}
					select {
					case <-finished:
					default:
						t.Error("consumer survived return")
					}
				})
			})
		}
	}
}

func TestDecodePipelineDeadlineDuringCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		finished := make(chan struct{})
		decodeErr, consumeErr := runDecodePipeline(ctx, func(w io.Writer) error {
			_, err := io.WriteString(w, "complete archive")
			return err
		}, func(r io.Reader) error {
			defer close(finished)
			if _, err := io.Copy(io.Discard, r); err != nil {
				return err
			}
			// EOF has already been received. Cancellation must not abandon a
			// permission-restoration pass that cannot be interrupted safely.
			<-ctx.Done()
			time.Sleep(35 * time.Second)
			return nil
		})
		if !errors.Is(decodeErr, context.DeadlineExceeded) || consumeErr != nil {
			t.Errorf("errors = %v, %v; want deadline failure after cleanup", decodeErr, consumeErr)
		}
		select {
		case <-finished:
		default:
			t.Error("deadline abandoned cleanup")
		}
		<-finished
	})
}

func TestDecodePipelineConsumerFailureUnblocksDecoder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("archive rejected before reading")
		decodeErr, consumeErr := runDecodePipeline(context.Background(), func(w io.Writer) error {
			_, err := io.WriteString(w, "decoded data")
			return err
		}, func(io.Reader) error { return failure })
		if decodeErr == nil || !errors.Is(consumeErr, failure) {
			t.Fatalf("errors = %v, %v; want stopped writer and original consumer error", decodeErr, consumeErr)
		}
	})
}

func TestDecodeAlreadyCanceledLeavesOutputsUntouched(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	decodeErr, _ := runDecodePipeline(ctx, func(io.Writer) error {
		t.Error("started reconstruction after cancellation")
		return nil
	}, func(io.Reader) error {
		t.Error("started extraction after cancellation")
		return nil
	})
	if !errors.Is(decodeErr, context.Canceled) {
		t.Fatalf("pipeline returned %v", decodeErr)
	}
	cfg := outputErrorConfig(t)
	if err := EncodeDirectory(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, dryRun := range []bool{false, true} {
		for _, exists := range []bool{false, true} {
			t.Run(fmt.Sprintf("dry=%t/existing=%t", dryRun, exists), func(t *testing.T) {
				root := t.TempDir()
				dest := filepath.Join(root, "restored")
				if exists {
					writePathFixture(t, filepath.Join(dest, "keep"), "previous contents")
				}
				err := DecodeDirectory(ctx, DecodeConfig{InputDir: cfg.OutputDir, OutputDir: dest, ClearIfNotEmpty: true, SizeOnly: dryRun})
				if !errors.Is(err, context.Canceled) {
					t.Errorf("canceled restore returned %v", err)
				}
				if exists {
					data, err := os.ReadFile(filepath.Join(dest, "keep"))
					entries, readErr := os.ReadDir(dest)
					if err != nil || string(data) != "previous contents" || readErr != nil || len(entries) != 1 {
						t.Error("canceled restore modified existing output")
					}
				} else if _, err := os.Stat(dest); !os.IsNotExist(err) {
					t.Errorf("canceled restore created destination: %v", err)
				}
			})
		}
	}
}
