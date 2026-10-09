// Copyright 2025 Ray Ozzie. All rights reserved.

//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rayozzie/padlock/pkg/pad"
)

func TestEntropyFIFOWithoutProducerDoesNotHang(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entropy.fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := openEntropyWithTimeout(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	source := &fileEntropySource{name: "fifo", file: f, timeout: time.Second}
	want := bytes.Repeat([]byte{0xa5}, pad.InitialEntropyBytes)
	got := bytes.Clone(want)
	if err := source.Read(context.Background(), got); !errors.Is(err, io.EOF) || !bytes.Equal(got, want) {
		t.Fatalf("FIFO with no producer returned %v", err)
	}
}

func TestEntropyFIFOReadWithProducer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entropy.fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	// Hold both ends open for a deterministic producer/consumer test.
	producer, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()
	f, err := openEntropyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	want := bytes.Repeat([]byte{0x83}, 76)
	if _, err := producer.Write(want); err != nil {
		t.Fatal(err)
	}
	source := &fileEntropySource{name: "fifo", file: f, timeout: time.Second}
	got := make([]byte, len(want))
	if err := source.Read(context.Background(), got); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("FIFO read failed: %v", err)
	}
}

func entropyFIFOPair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "entropy.fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	producer, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	reader, err := openEntropyFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	return reader, producer
}

func entropyReadMustWait(t *testing.T, done <-chan error, duration time.Duration) {
	t.Helper()
	select {
	case err := <-done:
		t.Fatalf("read returned before enough bytes were available: %v", err)
	case <-time.After(duration):
	}
}

func TestEntropyFIFODelayedReads(t *testing.T) {
	reader, producer := entropyFIFOPair(t)
	source := &fileEntropySource{name: "delayed FIFO", file: reader, timeout: 2 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	offset := 0
	for _, size := range []int{pad.InitialEntropyBytes, 44, 44} {
		want := make([]byte, size)
		for i := range want {
			want[i] = byte(offset + i)
		}
		got := bytes.Repeat([]byte{0xa5}, size)
		done := make(chan error, 1)
		go func() { done <- source.Read(ctx, got) }()
		// No data at the first read, then two partial deliveries separated by
		// another empty-pipe interval. Repeat for later 44-byte reseed requests.
		entropyReadMustWait(t, done, 100*time.Millisecond)
		if _, err := producer.Write(want[:13]); err != nil {
			t.Fatal(err)
		}
		entropyReadMustWait(t, done, 50*time.Millisecond)
		if _, err := producer.Write(want[13:]); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("delayed read lost, repeated, or changed bytes: got %x, err=%v", got, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("read did not finish after the producer supplied all bytes")
		}
		offset += size
	}
}

func TestEntropyFIFOPartialReadFailures(t *testing.T) {
	for _, kind := range []string{"timeout", "cancel", "disconnect", "close"} {
		t.Run(kind, func(t *testing.T) {
			reader, producer := entropyFIFOPair(t)
			source := &fileEntropySource{name: "FIFO", file: reader, timeout: 2 * time.Second}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantErr := error(context.DeadlineExceeded)
			if kind == "timeout" {
				source.timeout = 150 * time.Millisecond
			}
			if _, err := producer.Write([]byte{1, 2, 3}); err != nil {
				t.Fatal(err)
			}
			want := bytes.Repeat([]byte{0xa5}, pad.InitialEntropyBytes)
			got := bytes.Clone(want)
			done := make(chan error, 1)
			go func() { done <- source.Read(ctx, got) }()
			entropyReadMustWait(t, done, 40*time.Millisecond)
			switch kind {
			case "cancel":
				wantErr = context.Canceled
				cancel()
			case "disconnect":
				wantErr = io.ErrUnexpectedEOF
				if err := producer.Close(); err != nil {
					t.Fatal(err)
				}
			case "close":
				wantErr = os.ErrClosed
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				if !errors.Is(err, wantErr) || !bytes.Equal(got, want) {
					t.Fatalf("want %v with unchanged caller buffer, got %v, %x", wantErr, err, got)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("failed read did not stop")
			}
			if _, err := reader.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Errorf("failed source retained its file handle: %v", err)
			}
			// Failed sources must remain failed, without a retry or changed bytes.
			if err := source.Read(context.Background(), got); !errors.Is(err, wantErr) || !bytes.Equal(got, want) {
				t.Fatalf("failed source was reused: %v, %x", err, got)
			}
		})
	}
}

type entropyReadFunc func([]byte) (int, error)

func (f entropyReadFunc) Read(p []byte) (int, error) { return f(p) }

func TestEntropyFileRetryErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  error
		want error
	}{
		{"complete", nil, nil},
		{"partial_EOF", io.EOF, io.ErrUnexpectedEOF},
		{"permanent_error", syscall.EIO, syscall.EIO},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			reader := entropyReadFunc(func(p []byte) (int, error) {
				calls++
				switch calls {
				case 1:
					return copy(p, []byte{1, 2}), &os.PathError{Op: "read", Path: "test", Err: syscall.EAGAIN}
				case 2:
					return 0, fmt.Errorf("wrapped: %w", syscall.EWOULDBLOCK)
				case 3:
					if tc.end != nil {
						return 0, tc.end
					}
					return copy(p, []byte{3}), nil
				default:
					t.Fatal("retried a completed read or a permanent failure")
					return 0, io.EOF
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got := make([]byte, 3)
			err := readFullEntropyFile(ctx, reader, got)
			if !errors.Is(err, tc.want) || calls != 3 {
				t.Fatalf("got %v after %d reads; want %v after 3", err, calls, tc.want)
			}
			if tc.want == nil && !bytes.Equal(got, []byte{1, 2, 3}) {
				t.Fatalf("lost partial progress across retries: %v", got)
			}
		})
	}
}

func TestEntropyFileRetryDeadlineDoesNotReset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	calls := 0
	reader := entropyReadFunc(func(p []byte) (int, error) {
		calls++
		p[0] = byte(calls)
		return 1, syscall.EAGAIN // Constant partial progress, never a complete seed.
	})
	err := readFullEntropyFile(ctx, reader, make([]byte, 1000))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("partial progress reset the deadline: %v", err)
	}
	// The 5 ms wait should prevent busy-spinning through the entire buffer.
	if calls > 40 {
		t.Fatalf("expected bounded retries with partial progress, got %d calls", calls)
	}
}

func TestEntropyFIFODelayedProducerCLI(t *testing.T) {
	for _, format := range []string{"bin", "png"} {
		for _, files := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/files=%t", format, files), func(t *testing.T) {
				input, outputs, want := cliFixture(t, 1)
				reader, producer := entropyFIFOPair(t)
				path := reader.Name()
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					// Feed one initial seed request in three delayed fragments.
					for i, size := range []int{13, 17, pad.InitialEntropyBytes - 30} {
						delay := 50 * time.Millisecond
						if i == 0 {
							delay = 300 * time.Millisecond
						}
						select {
						case <-ctx.Done():
							done <- ctx.Err()
							return
						case <-time.After(delay):
						}
						if _, err := producer.Write(bytes.Repeat([]byte{byte(i + 1)}, size)); err != nil {
							done <- err
							return
						}
					}
					done <- nil
				}()
				args := []string{"encode", input, outputs[0], "-format", format, "-entropy-file", path, "-entropy-timeout", "2s"}
				if files {
					args = append(args, "-files")
				}
				if out, err := runCLI(t, args...); err != nil {
					t.Fatalf("delayed entropy encode failed: %v\n%s", err, out)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if err := producer.Close(); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				restored := filepath.Join(t.TempDir(), "restored")
				if out, err := runCLI(t, "decode", outputs[0], restored); err != nil {
					t.Fatalf("restore needed entropy producer: %v\n%s", err, out)
				}
				if data, err := os.ReadFile(filepath.Join(restored, "source.bin")); err != nil || !bytes.Equal(data, want) {
					t.Fatalf("restored data differs: %v", err)
				}
			})
		}
	}
}

func TestEntropyFIFOTimeoutPreservesOutputCLI(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	if err := os.Mkdir(outputs[0], 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(outputs[0], "keep")
	if err := os.WriteFile(marker, []byte("previous backup"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, producer := entropyFIFOPair(t)
	path := reader.Name()
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := producer.Write([]byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, "encode", input, outputs[0], "-clear", "-entropy-file", path, "-entropy-timeout", "100ms")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(out, "context deadline exceeded") {
		t.Fatalf("expected entropy timeout, got %v\n%s", err, out)
	}
	if strings.Contains(out, "Encode complete") || strings.Contains(out, "Encoding completed successfully") {
		t.Fatalf("failed entropy source reported success: %s", out)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "previous backup" {
		t.Fatalf("entropy timeout cleared output: %q, %v", data, err)
	}
	if entries, err := os.ReadDir(outputs[0]); err != nil || len(entries) != 1 {
		t.Fatalf("entropy timeout wrote output: %v, %v", entries, err)
	}
}
