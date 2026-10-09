// Copyright 2025 Ray Ozzie. All rights reserved.

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rayozzie/padlock/pkg/pad"
)

type entropySpec struct {
	kind, value string
}

type entropyOptions struct {
	specs   []entropySpec
	timeout time.Duration
}

func registerEntropyFlags(flags *flag.FlagSet) *entropyOptions {
	opts := &entropyOptions{}
	flags.Func("entropy-file", "additional seed bytes from file/device/pipe; '-' reads stdin (repeatable, encode only)", func(value string) error {
		if value == "" {
			return fmt.Errorf("entropy path must not be empty")
		}
		opts.specs = append(opts.specs, entropySpec{"file", value})
		return nil
	})
	flags.Func("entropy-url", "additional seed bytes from a fresh HTTPS application/octet-stream response (repeatable, encode only)", func(value string) error {
		opts.specs = append(opts.specs, entropySpec{"https", value})
		return nil
	})
	flags.DurationVar(&opts.timeout, "entropy-timeout", 10*time.Second, "time limit per external source open/read (encode only)")
	return opts
}

// URL errors deliberately omit the URL: its query may contain an API token.
func entropyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("entropy URL must be HTTPS with a host and no userinfo or fragment")
	}
	return u, nil
}

func newEntropyHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.DisableKeepAlives = true
	transport.MaxResponseHeaderBytes = 64 * 1024
	return &http.Client{
		Transport: transport,
		// Do not redirect to a different provider or downgrade to HTTP.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// prepare validates all selections before reading any seed bytes. Dry runs
// validate syntax but do not open files, read stdin, or contact URLs.
func (o *entropyOptions) prepare(ctx context.Context, input string, outputs []string, dryRun bool, stdin *os.File) ([]pad.RNG, func(), error) {
	closeSources := func() {}
	if o.timeout <= 0 {
		return nil, closeSources, fmt.Errorf("entropy-timeout must be greater than zero")
	}
	seen := make(map[string]bool)
	for i, spec := range o.specs {
		key := spec.kind + ":" + spec.value
		if spec.kind == "https" {
			u, err := entropyURL(spec.value)
			if err != nil {
				return nil, closeSources, fmt.Errorf("entropy source %d: %w", i+1, err)
			}
			u.Host = strings.ToLower(u.Host)
			key = u.String()
		}
		if seen[key] {
			return nil, closeSources, fmt.Errorf("entropy source %d duplicates an earlier source", i+1)
		}
		seen[key] = true
	}
	if dryRun || len(o.specs) == 0 {
		return nil, closeSources, nil
	}

	var sources []pad.RNG
	var files []*os.File
	var infos []os.FileInfo
	client := newEntropyHTTPClient()
	closeSources = func() {
		for _, f := range files {
			f.Close()
		}
		client.CloseIdleConnections()
	}
	fail := func(err error) ([]pad.RNG, func(), error) {
		closeSources()
		return nil, func() {}, err
	}
	for i, spec := range o.specs {
		name := fmt.Sprintf("entropy source %d (%s)", i+1, spec.kind)
		if spec.kind == "https" {
			sources = append(sources, &httpEntropySource{name: name, address: spec.value, timeout: o.timeout, client: client})
			continue
		}
		f := stdin
		if spec.value != "-" {
			var err error
			f, err = openEntropyWithTimeout(ctx, spec.value, o.timeout)
			if err != nil {
				return fail(fmt.Errorf("open %s: %w", name, err))
			}
		}
		files = append(files, f)
		info, err := f.Stat()
		if err != nil {
			return fail(fmt.Errorf("stat %s: %w", name, err))
		}
		if info.IsDir() {
			return fail(fmt.Errorf("%s is a directory", name))
		}
		for _, other := range infos {
			if os.SameFile(info, other) {
				return fail(fmt.Errorf("%s refers to the same file/device/pipe as an earlier source", name))
			}
		}
		infos = append(infos, info)
		sources = append(sources, &fileEntropySource{name: name, file: f, timeout: o.timeout})
	}
	// Checking identities also catches hard links, symlinks to files outside the
	// archive, and differently spelled paths. Do not archive or clear seed inputs.
	if len(infos) > 0 {
		for _, root := range append([]string{input}, outputs...) {
			if err := checkEntropyOutsideDirectory(root, infos); err != nil {
				return fail(err)
			}
		}
	}
	return sources, closeSources, nil
}

func checkEntropyOutsideDirectory(root string, sources []os.FileInfo) error {
	resolved, err := filepath.EvalSymlinks(root)
	if os.IsNotExist(err) {
		return nil // A nonexistent output cannot contain an existing entropy file.
	}
	if err != nil {
		return fmt.Errorf("check entropy input separation: %w", err)
	}
	return filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("check entropy input separation: %w", walkErr)
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		for _, source := range sources {
			if os.SameFile(info, source) {
				return fmt.Errorf("entropy input must be outside the backup input and output directories (including hard links)")
			}
		}
		return nil
	})
}

func openEntropyWithTimeout(ctx context.Context, path string, timeout time.Duration) (*os.File, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		file *os.File
		err  error
	}
	done := make(chan result)
	go func() {
		f, err := openEntropyFile(path)
		select {
		case done <- result{f, err}:
		case <-ctx.Done():
			if f != nil {
				f.Close()
			}
		}
	}()
	select {
	case result := <-done:
		return result.file, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type fileEntropySource struct {
	name    string
	file    *os.File
	timeout time.Duration
	mu      sync.Mutex
	failed  error
}

func (s *fileEntropySource) Name() string { return s.name }
func (s *fileEntropySource) Read(ctx context.Context, p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Keep the caller's buffer private from a read that finishes after timeout.
	buf := make([]byte, len(p))
	done := make(chan error)
	go func() {
		err := readFullEntropyFile(ctx, s.file, buf)
		select {
		case done <- err:
		case <-ctx.Done():
			clear(buf)
		}
	}()
	select {
	case err := <-done:
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			copy(p, buf)
			clear(buf)
			return nil
		}
		clear(buf)
		s.failed = err
	case <-ctx.Done():
		s.failed = ctx.Err()
	}
	// Every attempted read failure permanently disables this source. Closing
	// interrupts pollable reads and releases the handle even if the retry loop
	// reports cancellation before this goroutine selects ctx.Done(). A worker
	// in a non-interruptible OS read retains only its own private buffer.
	s.file.Close()
	return s.failed
}

// readFullEntropyFile preserves partial progress when a nonblocking file cannot
// supply more bytes yet. Darwin's named FIFOs do not use Go's runtime poller, so
// their EAGAIN/EWOULDBLOCK needs an explicit, cancellable wait. The context is
// shared across every retry; progress never renews the per-read timeout.
func readFullEntropyFile(ctx context.Context, reader io.Reader, buf []byte) error {
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(reader, buf[total:])
		total += n
		if !entropyReadWouldBlock(err) {
			if err == io.EOF && total > 0 {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type httpEntropySource struct {
	name, address string
	timeout       time.Duration
	client        *http.Client
}

func (s *httpEntropySource) Name() string { return s.name }
func (s *httpEntropySource) Read(ctx context.Context, p []byte) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.address, nil)
	if err != nil {
		return fmt.Errorf("cannot construct HTTPS entropy request")
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store, no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("X-Padlock-Entropy-Bytes", strconv.Itoa(len(p)))
	resp, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// url.Error includes the complete URL, including query credentials.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("HTTPS entropy request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTPS entropy source returned status %d (redirects are not followed)", resp.StatusCode)
	}
	kind, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || kind != "application/octet-stream" {
		return fmt.Errorf("HTTPS entropy source must return application/octet-stream raw bytes")
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return fmt.Errorf("HTTPS entropy source must return uncompressed bytes")
	}
	if age := resp.Header.Get("Age"); age != "" && age != "0" {
		return fmt.Errorf("HTTPS entropy source returned a cached response")
	}
	buf := make([]byte, len(p))
	defer clear(buf)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		return fmt.Errorf("read HTTPS entropy bytes: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	copy(p, buf)
	return nil
}
