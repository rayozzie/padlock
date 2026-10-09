// Copyright 2025 Ray Ozzie. All rights reserved.

package file

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rayozzie/padlock/pkg/trace"
)

// SerializeDirectoryToStream resolves the input directory path and streams its
// contents as TAR. Symlinks within that directory are skipped.
func SerializeDirectoryToStream(ctx context.Context, inputDir string) (io.ReadCloser, error) {
	log := trace.FromContext(ctx).WithPrefix("serialize")
	log.Debugf("Serializing directory to tar stream: %s", inputDir)
	if err := ValidateInputDirectory(ctx, inputDir); err != nil {
		return nil, err
	}
	// Walk does not follow a symlink at its root. Resolve the supplied path for
	// every caller, including dry runs, without cleaning away link/.. first.
	root, err := filepath.EvalSymlinks(inputDir)
	if err != nil {
		return nil, fmt.Errorf("resolve input directory %q: %w", inputDir, err)
	}
	inputDir = root
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()

		log.Debugf("Creating tar writer")
		tw := tar.NewWriter(pw)

		fileCount := 0
		totalBytes := int64(0)

		// Walk through the directory
		err := filepath.Walk(inputDir, func(path string, info os.FileInfo, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				log.Error(fmt.Errorf("error walking path %s: %w", path, walkErr))
				return walkErr
			}

			// Skip the input directory itself
			if path == inputDir {
				return nil
			}

			// Skip symlinks
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}

			// Validate the actual open file before emitting its header or reading
			// any bytes. A special file must never enter a successful backup.
			var f *os.File
			if !info.IsDir() {
				var err error
				f, info, err = openRegularSource(path, info)
				if err != nil {
					return err
				}
				defer f.Close()
			}

			// Get the relative path for the tar entry
			rel, err := filepath.Rel(inputDir, path)
			if err != nil {
				log.Error(fmt.Errorf("failed to determine relative path: %w", err))
				return err
			}

			// Create a tar header
			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				log.Error(fmt.Errorf("tar FileInfoHeader for %s: %w", path, err))
				return err
			}
			header.Name = rel

			// Write the header to the tar stream
			if err := tw.WriteHeader(header); err != nil {
				log.Error(fmt.Errorf("tar WriteHeader for %s: %w", rel, err))
				return err
			}

			// For directories, we're done after writing the header
			if info.IsDir() {
				return nil
			}

			// Copy the file data to the tar stream
			n, err := io.Copy(tw, f)
			if err != nil {
				log.Error(fmt.Errorf("io.Copy to tar for %s: %w", rel, err))
				return err
			}

			fileCount++
			totalBytes += n
			log.Infof("%s (%d bytes)", rel, n)

			return nil
		})

		if err != nil {
			log.Error(fmt.Errorf("error during directory serialization: %w", err))
			pw.CloseWithError(fmt.Errorf("error during directory serialization: %w", err))
			return
		}

		// Finalization can fail if a source file became shorter after its TAR
		// header was written. Report that error before closing the pipe as EOF.
		if err := tw.Close(); err != nil {
			streamErr := fmt.Errorf("error finalizing directory archive: %w", err)
			log.Error(streamErr)
			pw.CloseWithError(streamErr)
			return
		}

		log.Debugf("Directory serialization complete: %d files, %d bytes", fileCount, totalBytes)
	}()

	return pr, nil
}

// DeserializeDirectoryFromStream takes a tar stream and extracts its contents
// to the specified output directory. It returns errors encountered during extraction.
func DeserializeDirectoryFromStream(ctx context.Context, outputDir string, r io.Reader, clearIfNotEmpty bool) error {
	log := trace.FromContext(ctx).WithPrefix("deserialize")
	log.Debugf("Deserializing to directory: %s", outputDir)
	if err := ctx.Err(); err != nil {
		return err
	}

	// Ensure the output directory can be written to
	if err := prepareOutputDirectory(ctx, outputDir, clearIfNotEmpty); err != nil {
		log.Error(fmt.Errorf("failed to clear directory: %w", err))
		return err
	}

	// Create the output directory if it doesn't exist
	if err := os.MkdirAll(outputDir, 0700); err != nil {
		log.Error(fmt.Errorf("failed to create output directory: %w", err))
		return err
	}
	outputRoot, err := os.OpenRoot(outputDir)
	if err != nil {
		return fmt.Errorf("failed to open extraction directory: %w", err)
	}
	defer outputRoot.Close()

	extractor := newTarExtractor(outputRoot)
	extractErr := processDirectoryTar(ctx, r, extractor.extract, log)
	return errors.Join(extractErr, extractor.finish(), ctx.Err())
}

// ValidateDirectoryStream checks a directory archive without writing files.
// Like restore, it accepts TAR or gzip-compressed TAR and requires a complete
// archive, including both TAR ending blocks and the end of the input stream.
func ValidateDirectoryStream(ctx context.Context, r io.Reader) error {
	log := trace.FromContext(ctx).WithPrefix("validate-archive")
	return processDirectoryTar(ctx, r, func(_ *tar.Header, contents io.Reader) (int64, error) {
		return io.Copy(io.Discard, contents)
	}, log)
}

// tarSourceReader prevents archive/tar from treating a physical EOF as a
// complete archive. Only tar.Reader's own two-zero-block terminator may end
// iteration successfully. Retain source errors even if a full header read
// consumes the accompanying bytes and io.ReadFull suppresses the error.
type tarSourceReader struct {
	reader io.Reader
	err    error
}

func (r *tarSourceReader) Read(p []byte) (int, error) {
	var n int
	if r.err == nil {
		n, r.err = r.reader.Read(p)
	}
	if r.err == io.EOF {
		return n, fmt.Errorf("incomplete tar archive: missing end-of-archive blocks: %w", io.ErrUnexpectedEOF)
	}
	return n, r.err
}

func (r *tarSourceReader) finish() error {
	if r.err == io.EOF {
		return nil
	}
	if r.err != nil {
		return r.err
	}
	// Consume trailing TAR padding and check the actual stream ending. For
	// gzip this also reads and validates its footer; Close alone does not.
	_, err := io.Copy(io.Discard, r.reader)
	return err
}

// directoryContextReader checks cancellation even when a decompressor has
// buffered enough data to keep extracting after its input pipe is closed.
// It cannot interrupt an underlying Read or filesystem operation in progress.
type directoryContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r directoryContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

// processDirectoryTar shares archive parsing and completion checks between
// restoration and dry runs. The handler consumes each entry's contents.
func processDirectoryTar(ctx context.Context, r io.Reader, processEntry func(*tar.Header, io.Reader) (int64, error), log *trace.Tracer) error {
	stream, err := DecompressStreamToStream(ctx, directoryContextReader{ctx, r})
	if err != nil {
		return fmt.Errorf("read directory archive: %w", err)
	}
	if closer, ok := stream.(io.Closer); ok {
		defer closer.Close()
	}
	source := &tarSourceReader{reader: directoryContextReader{ctx, stream}}
	tr := tar.NewReader(source)
	fileCount := 0
	totalBytes := int64(0)
	progressInterval := 100 // Log progress every N files
	progressCounter := 0
	lastProgressTime := time.Now()
	progressUpdateInterval := 5 * time.Second // Minimum time between progress updates

	// Iterate through tar entries
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if err == io.EOF {
			// A complete archive may contain only directories or no entries.
			// tarSourceReader requires both ending blocks; finish below also
			// validates the underlying stream ending, including any gzip footer.
			break
		}
		if err != nil {
			log.Error(fmt.Errorf("tar header read error: %w", err))
			return fmt.Errorf("tar header read error: %w", err)
		}
		// Cancellation may have arrived while the header read was in progress.
		// Do not create another entry using the bytes that read returned.
		if err := ctx.Err(); err != nil {
			return err
		}

		// Apply the same path/type rules to restore and dry-run validation,
		// before either extraction or draining can consume unsupported entries.
		if _, err := validateTarEntryHeader(header); err != nil {
			return err
		}

		n, err := processEntry(header, tr)
		if err != nil {
			log.Error(err)
			return err
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}

		fileCount++
		totalBytes += n

		// Progress logging - don't spam the logs too much for large archives
		progressCounter++
		if progressCounter >= progressInterval || time.Since(lastProgressTime) > progressUpdateInterval {
			log.Infof("Archive progress: %d files (%s)", fileCount, formatByteSize(totalBytes))
			progressCounter = 0
			lastProgressTime = time.Now()
		} else {
			log.Infof("Processed: %s (%d bytes)", header.Name, n)
		}
	}

	if err := source.finish(); err != nil {
		return fmt.Errorf("read tar archive ending: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	log.Infof("Directory archive complete: %d files (%s)", fileCount, formatByteSize(totalBytes))
	return nil
}

// formatByteSize formats size in bytes to a human-readable string with units
func formatByteSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// prepareOutputDirectory ensures the output directory is empty for deserialization
func prepareOutputDirectory(ctx context.Context, dirPath string, clearIfNotEmpty bool) error {
	log := trace.FromContext(ctx).WithPrefix("deserialize")
	log.Debugf("Preparing output directory: %s (clear=%v)", dirPath, clearIfNotEmpty)

	// Create the directory if it doesn't exist
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		log.Debugf("Creating directory: %s", dirPath)
		if err := os.MkdirAll(dirPath, 0700); err != nil {
			log.Error(fmt.Errorf("failed to create directory: %w", err))
			return err
		}
		return nil
	}

	// Check if the directory is empty
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		log.Error(fmt.Errorf("failed to read directory: %w", err))
		return err
	}

	// If not empty, check if we should clear it
	if len(entries) > 0 {
		log.Debugf("Directory %s is not empty (%d entries)", dirPath, len(entries))
		if !clearIfNotEmpty {
			return nil
		}

		// Remove all entries
		log.Debugf("Removing %d entries from directory: %s", len(entries), dirPath)
		var clearErrors []string
		for _, entry := range entries {
			entryPath := filepath.Join(dirPath, entry.Name())
			log.Debugf("Removing: %s", entryPath)
			if err := os.RemoveAll(entryPath); err != nil {
				errMsg := fmt.Sprintf("failed to remove %s: %v", entryPath, err)
				log.Error(fmt.Errorf("%s", errMsg))
				clearErrors = append(clearErrors, errMsg)
			}
		}

		// Check if any errors occurred during clearing
		if len(clearErrors) > 0 {
			if len(clearErrors) <= 3 {
				log.Error(fmt.Errorf("failed to fully clear directory: %v", clearErrors))
				return fmt.Errorf("failed to fully clear directory: %v", clearErrors)
			}
			log.Error(fmt.Errorf("failed to fully clear directory: %v and %d more errors",
				clearErrors[:3], len(clearErrors)-3))
			return fmt.Errorf("failed to fully clear directory: %v and %d more errors",
				clearErrors[:3], len(clearErrors)-3)
		}

		// Verify the directory is now empty
		entries, err = os.ReadDir(dirPath)
		if err != nil {
			log.Error(fmt.Errorf("failed to recheck directory after clearing: %w", err))
			return err
		}
		if len(entries) > 0 {
			log.Error(fmt.Errorf("directory not empty after clearing, manual intervention required"))
			return fmt.Errorf("directory not empty after clearing, manual intervention required")
		}
	}

	log.Debugf("Directory %s is prepared", dirPath)
	return nil
}
