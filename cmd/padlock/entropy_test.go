// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayozzie/padlock/pkg/pad"
)

func TestEntropyHTTPSReadsFreshRawBytes(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "GET" || len(body) != 0 || r.URL.RawQuery != "token=private" || r.Header.Get("Cache-Control") != "no-store, no-cache" {
			t.Errorf("unexpected entropy request: method=%s body=%d query=%q", r.Method, len(body), r.URL.RawQuery)
		}
		var n int
		fmt.Sscan(r.Header.Get("X-Padlock-Entropy-Bytes"), &n)
		if n != pad.InitialEntropyBytes && n != 44 {
			t.Errorf("unexpected requested seed size %d", n)
		}
		value := byte(requests.Add(1))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(bytes.Repeat([]byte{value}, n+100)) // Extra bytes must not be reused.
	}))
	defer server.Close()
	client := trustedEntropyClient(server)
	defer client.CloseIdleConnections()
	source := &httpEntropySource{name: "test HTTPS", address: server.URL + "?token=private", timeout: time.Second, client: client}
	for i, size := range []int{pad.InitialEntropyBytes, 44, 44} {
		got := make([]byte, size)
		if err := source.Read(context.Background(), got); err != nil || !bytes.Equal(got, bytes.Repeat([]byte{byte(i + 1)}, size)) {
			t.Fatalf("read %d reused a response or failed: %v", i, err)
		}
	}
	if requests.Load() != 3 {
		t.Fatalf("got %d HTTPS requests, want 3", requests.Load())
	}
}

func trustedEntropyClient(server *httptest.Server) *http.Client {
	client := newEntropyHTTPClient()
	client.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	return client
}

func TestEntropyHTTPSRejectsBadResponses(t *testing.T) {
	for _, kind := range []string{"status", "html", "json", "missing_type", "gzip", "cached", "short", "timeout", "redirect", "untrusted_tls"} {
		t.Run(kind, func(t *testing.T) {
			var redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
			defer target.Close()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				switch kind {
				case "status":
					w.WriteHeader(503)
				case "html", "json":
					w.Header().Set("Content-Type", "text/"+kind)
				case "missing_type":
					w.Header()["Content-Type"] = nil
				case "gzip":
					w.Header().Set("Content-Encoding", "gzip")
				case "cached":
					w.Header().Set("Age", "12")
				case "short":
					w.Write([]byte{1, 2, 3})
					return
				case "timeout":
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				case "redirect":
					w.Header().Set("Location", target.URL+"?private-token")
					w.WriteHeader(302)
				}
				w.Write(bytes.Repeat([]byte("DO-NOT-LOG-RESPONSE"), 10))
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			defer server.Close()
			client := trustedEntropyClient(server)
			if kind == "untrusted_tls" {
				client = newEntropyHTTPClient()
			}
			defer client.CloseIdleConnections()
			timeout := time.Second
			if kind == "timeout" {
				timeout = 50 * time.Millisecond
			}
			source := &httpEntropySource{name: "source", address: server.URL + "?private-token", timeout: timeout, client: client}
			want := bytes.Repeat([]byte{0xa5}, pad.InitialEntropyBytes)
			got := bytes.Clone(want)
			err := source.Read(context.Background(), got)
			if err == nil || !bytes.Equal(got, want) || redirected.Load() != 0 {
				t.Fatalf("bad response accepted, changed output, or followed redirect: %v", err)
			}
			if strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "DO-NOT-LOG-RESPONSE") {
				t.Fatalf("error exposes source credentials/response: %v", err)
			}
		})
	}
}

func TestEntropyFileReadsAndTimeout(t *testing.T) {
	t.Run("fresh bytes and exhaustion", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "seed")
		data := make([]byte, pad.InitialEntropyBytes+44)
		for i := range data {
			data[i] = byte(i)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		f, err := openEntropyFile(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		source := &fileEntropySource{name: "seed file", file: f, timeout: time.Second}
		offset := 0
		for _, size := range []int{pad.InitialEntropyBytes, 44} {
			got := make([]byte, size)
			if err := source.Read(context.Background(), got); err != nil || !bytes.Equal(got, data[offset:offset+size]) {
				t.Fatalf("file read reused or changed data: %v", err)
			}
			offset += size
		}
		for range 2 {
			got := []byte{0xa5}
			if err := source.Read(context.Background(), got); !errors.Is(err, io.EOF) || got[0] != 0xa5 {
				t.Fatalf("exhausted file returned data or was reopened: %v", err)
			}
		}
	})
	t.Run("pipe timeout", func(t *testing.T) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		defer w.Close()
		source := &fileEntropySource{name: "pipe", file: r, timeout: 25 * time.Millisecond}
		want := bytes.Repeat([]byte{0xa5}, 76)
		got := bytes.Clone(want)
		if err := source.Read(context.Background(), got); !errors.Is(err, context.DeadlineExceeded) || !bytes.Equal(got, want) {
			t.Fatalf("blocked pipe did not time out cleanly: %v", err)
		}
		if err := source.Read(context.Background(), got); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("failed source was reused: %v", err)
		}
	})
}

func TestEntropyFilesRoundTripCLI(t *testing.T) {
	for _, format := range []string{"bin", "png"} {
		for _, files := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/files=%t", format, files), func(t *testing.T) {
				input, outputs, want := cliFixture(t, 1)
				args := []string{"encode", input, outputs[0], "-format", format, "-copies", "3", "-required", "2"}
				if files {
					args = append(args, "-files")
				}
				var seedPaths []string
				for i := 0; i < 2; i++ {
					path := filepath.Join(t.TempDir(), "seed")
					if err := os.WriteFile(path, bytes.Repeat([]byte{byte(i + 1)}, pad.InitialEntropyBytes), 0600); err != nil {
						t.Fatal(err)
					}
					seedPaths = append(seedPaths, path)
					args = append(args, "-entropy-file", path)
				}
				if out, err := runCLI(t, args...); err != nil || !strings.Contains(out, "2 additional source(s)") {
					t.Fatalf("external entropy encode failed: %v\n%s", err, out)
				}
				for _, path := range seedPaths {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				restored := filepath.Join(t.TempDir(), "restored")
				if out, err := runCLI(t, "decode", outputs[0], restored); err != nil {
					t.Fatalf("decode required external seed inputs: %v\n%s", err, out)
				}
				got, err := os.ReadFile(filepath.Join(restored, "source.bin"))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("restored data differs: %v", err)
				}
			})
		}
	}
}

func TestEntropyStdinCLI(t *testing.T) {
	input, outputs, _ := cliFixture(t, 1)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "encode", input, outputs[0], "-format", "bin", "-entropy-file", "-")
	cmd.Env = append(os.Environ(), "PADLOCK_CLI_TEST_HELPER=1")
	cmd.Stdin = bytes.NewReader(bytes.Repeat([]byte{0x37}, pad.InitialEntropyBytes))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stdin entropy encode failed: %v\n%s", err, out)
	}
}

func TestEntropyFailurePreservesOutputCLI(t *testing.T) {
	for _, kind := range []string{"short", "missing", "duplicate", "hardlink_duplicate", "inside_input", "inside_output", "input_hardlink", "http", "bad_timeout"} {
		t.Run(kind, func(t *testing.T) {
			input, outputs, _ := cliFixture(t, 1)
			if err := os.Mkdir(outputs[0], 0700); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(outputs[0], "keep")
			if err := os.WriteFile(keep, []byte("existing backup"), 0600); err != nil {
				t.Fatal(err)
			}
			seed := filepath.Join(t.TempDir(), "seed")
			if err := os.WriteFile(seed, make([]byte, pad.InitialEntropyBytes), 0600); err != nil {
				t.Fatal(err)
			}
			flags := []string{"-entropy-file", seed}
			switch kind {
			case "short":
				if err := os.Truncate(seed, pad.InitialEntropyBytes-1); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(seed); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				flags = append(flags, "-entropy-file", seed)
			case "hardlink_duplicate", "input_hardlink":
				alias := filepath.Join(t.TempDir(), "alias")
				if kind == "input_hardlink" {
					alias = filepath.Join(input, "seed")
				}
				if err := os.Link(seed, alias); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
				if kind == "hardlink_duplicate" {
					flags = append(flags, "-entropy-file", alias)
				}
			case "inside_input":
				flags = []string{"-entropy-file", filepath.Join(input, "source.bin")}
			case "inside_output":
				flags = []string{"-entropy-file", keep}
			case "http":
				flags = []string{"-entropy-url", "http://localhost/?SECRET-TOKEN"}
			case "bad_timeout":
				flags = append(flags, "-entropy-timeout", "0")
			}
			args := append([]string{"encode", input, outputs[0], "-clear", "-format", "bin"}, flags...)
			out, err := runCLI(t, args...)
			if err == nil || !strings.Contains(out, "entropy") || strings.Contains(out, "panic:") || strings.Contains(out, "SECRET-TOKEN") {
				t.Fatalf("invalid entropy was not rejected cleanly: %v\n%s", err, out)
			}
			got, err := os.ReadFile(keep)
			if err != nil || string(got) != "existing backup" {
				t.Fatalf("entropy failure cleared existing output: %v", err)
			}
		})
	}
}

func TestEntropyDryRunDoesNotAccessSourcesCLI(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	input, outputs, _ := cliFixture(t, 1)
	args := []string{"encode", input, outputs[0], "-dryrun", "-entropy-file", filepath.Join(t.TempDir(), "missing"), "-entropy-file", "-", "-entropy-url", server.URL}
	if out, err := runCLI(t, args...); err != nil || !strings.Contains(out, "not opened or consumed") {
		t.Fatalf("dry run tried to consume an external source: %v\n%s", err, out)
	}
	if requests.Load() != 0 {
		t.Fatal("dry run contacted the HTTPS source")
	}
	if _, err := os.Stat(outputs[0]); !os.IsNotExist(err) {
		t.Fatalf("dry run created output directory: %v", err)
	}
}

func TestEntropyHTTPSAndFileCLI(t *testing.T) {
	for _, mode := range []string{"success", "short", "status"} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/octet-stream")
				if mode == "status" {
					w.WriteHeader(503)
				}
				size := pad.InitialEntropyBytes
				if mode == "short" {
					size--
				}
				w.Write(bytes.Repeat([]byte{0x8c}, size))
			}))
			defer server.Close()
			cert := filepath.Join(t.TempDir(), "root.pem")
			if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PADLOCK_CLI_TEST_CA", cert)
			input, outputs, want := cliFixture(t, 1)
			if err := os.Mkdir(outputs[0], 0700); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(outputs[0], "keep")
			if err := os.WriteFile(keep, []byte("existing backup"), 0600); err != nil {
				t.Fatal(err)
			}
			seed := filepath.Join(t.TempDir(), "seed")
			if err := os.WriteFile(seed, bytes.Repeat([]byte{0x17}, pad.InitialEntropyBytes), 0600); err != nil {
				t.Fatal(err)
			}
			out, err := runCLI(t, "encode", input, outputs[0], "-clear", "-format", "bin", "-entropy-file", seed, "-entropy-url", server.URL+"?PRIVATE-TOKEN")
			if strings.Contains(out, "PRIVATE-TOKEN") || requests.Load() != 1 {
				t.Fatalf("unexpected HTTPS requests or exposed credentials: count=%d\n%s", requests.Load(), out)
			}
			if mode != "success" {
				if err == nil || strings.Contains(out, "panic:") {
					t.Fatalf("failed HTTPS source was ignored: %v\n%s", err, out)
				}
				got, err := os.ReadFile(keep)
				if err != nil || string(got) != "existing backup" {
					t.Fatalf("HTTPS failure cleared output: %v", err)
				}
				return
			}
			if err != nil || !strings.Contains(out, "2 additional source(s)") {
				t.Fatalf("HTTPS + file encode failed: %v\n%s", err, out)
			}
			server.Close() // Restoration must not depend on the server or seed file.
			if err := os.Remove(seed); err != nil {
				t.Fatal(err)
			}
			restored := filepath.Join(t.TempDir(), "restored")
			if out, err := runCLI(t, "decode", outputs[0], restored); err != nil {
				t.Fatalf("decode failed without external entropy sources: %v\n%s", err, out)
			}
			got, err := os.ReadFile(filepath.Join(restored, "source.bin"))
			if err != nil || !bytes.Equal(got, want) || requests.Load() != 1 {
				t.Fatalf("restored data differs or decoding contacted source: %v", err)
			}
		})
	}
}
