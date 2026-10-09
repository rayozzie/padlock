# Padlock Implementation Details

This page provides a deeper look at the implementation details of the Padlock project, focusing on the code structure, key algorithms, and design decisions.

## Code Organization

Padlock is implemented in Go and organized into the following directory structure:

```
padlock/
├── cmd/
│   └── padlock/
│       └── main.go       # Command-line interface entry point
├── pkg/
│   ├── file/             # File system operations
│   │   ├── adapter.go    # Adapters for different I/O interfaces
│   │   ├── collection.go # Collection management
│   │   ├── compress.go   # Compression utilities
│   │   ├── directory.go  # Directory operations
│   │   ├── format.go     # Output format handling
│   │   ├── serialize.go  # Directory serialization
│   │   └── archive.go        # TAR archive support
│   ├── pad/              # Core XOR threshold splitting
│   │   ├── pad.go        # Threshold scheme implementation
│   │   └── rng.go        # Random number generation
│   ├── padlock/          # High-level orchestration
│   │   └── padlock.go    # Encoding and decoding coordination
│   └── trace/            # Logging and tracing
│       └── trace.go      # Context-based logging system
```

## Key Components

### Command-Line Interface (`cmd/padlock/main.go`)

The command-line interface is implemented in `main.go` and provides:

- Command-line argument parsing using the standard `flag` package
- Input validation and error handling
- Configuration of encoding and decoding parameters
- Coordination of the high-level encoding and decoding operations

The CLI supports two main commands:
- `encode`: Split input data across N collections with K-of-N threshold security
- `decode`: Reconstruct original data using K or more collections

### Core Pad Implementation (`pkg/pad/pad.go`)

The core of the threshold scheme is implemented in `pad.go`, which provides:

- Creation of the mathematical structure for the K-of-N threshold scheme
- Encoding of data chunks using one-time pads and XOR operations
- Decoding of data chunks from K or more collections
- Management of chunk metadata and collection information

Key algorithms in this file include:

1. **Pad Creation**: Stores collection names and the count C(N-1,K-1), without materializing all combinations
2. **Chunk Encoding**: Visits combinations in lexicographic order and fills one contiguous payload buffer per collection
3. **Chunk Decoding**: Calculates each required piece's position directly, using binomial coefficients

The combination helpers are in `pkg/pad/combinations.go`. The former `Pad.Permutations` and `Pad.Ciphers` fields and `UniqueSortedCombinations` helper have been removed. For restore, removing a collection's fixed letter from a selected combination preserves its order within that collection's list. Ranking the remaining K-1 letters among N-1 collections therefore gives the same position as the previous sorted-table implementation. The backup layout is unchanged.

Within each collection, combinations where its letter comes first form a suffix; these hold the XOR results. Earlier entries hold random pads. Encoding fills that pad prefix in one RNG request per collection, assigning a separate byte range to every pad, then computes the XOR results while visiting combinations. It consumes the same total number of pad bytes as before, with no reuse between combinations or chunks. RNG failures abort the chunk before its output writers are created.

### Random Number Generation (`pkg/pad/rng.go`)

`MultiRNG` XORs five generator outputs: `CryptoRand`, `MathRand`, `ChaCha20Rand`, `PCG64Rand`, and `MT19937Rand`. The generators have separate state, but the default output and seeds share one OS randomness source through Go's `crypto/rand`.

- Each seeded provider consumes separate seed bytes; the default does not derive all seeds from one seed value.
- PCG64 reads both 64-bit seed words from the OS source, with no clock-based seed component.
- MathRand, PCG64, and MT19937 are statistical PRNGs, not security fallbacks.
- Seed reads must fill the complete seed; initialization stops on failure with no fixed or partial fallback.
- ChaCha20 obtains a fresh key and nonce before reaching its 256 GiB stream limit.
- The mixer propagates any generator error and leaves the caller's output buffer unchanged.

The mixer uses XOR only. `NewRandWithEntropy` accepts optional sources implementing the `RNG` complete-read contract, consumes 76 bytes from each at initialization, and retains them for 44-byte ChaCha20 reseeds. The caller owns their lifetimes. The CLI adapters in `cmd/padlock/entropy.go` provide bounded file/device/pipe and HTTPS reads, plus validation before output clearing. No extra sources are used unless explicitly selected, and the mixer does not hash inputs. See the [Security Model](Security-Model) for the shared dependency and the assumptions needed for secrecy.

### File System Operations (`pkg/file/`)

The file package handles all interactions with the file system:

- `collection.go`: Defines the structure and operations for collections
- `directory.go`: Handles directory operations for collections
- `format.go`: Defines interfaces for different output formats (binary and PNG)
- `serialize.go`: Implements directory serialization and deserialization
- `compress.go`: Provides compression and decompression functionality
- `archive.go`: Streams collection TARs and finalizes buffered writes
- `collection_discovery.go`: Probes collection metadata without temporary extraction
- `collection_index.go`: Indexes chunk names and byte offsets for numerically ordered TAR reads
- `extract.go`: Restores regular files/directories beneath an `os.Root` and finalizes directory modes

### High-Level Orchestration (`pkg/padlock/padlock.go`)

The padlock package coordinates the overall encoding and decoding processes:

- `EncodeDirectory`: Orchestrates the encoding process
- `DecodeDirectory`: Orchestrates the decoding process

These functions set up the processing pipeline, coordinate the different components, and handle error reporting.

## Key Algorithms

### K-of-N Threshold Scheme

The K-of-N threshold scheme is implemented using a combination of one-time pads and XOR operations:

1. For each input chunk and each combination of K collection letters:
   - Request K-1 fresh pads of the same size as the input chunk
   - XOR the input chunk with those pads to form the remaining piece
   - Distribute the K pieces across that combination's K collections

2. During decoding:
   - Validate every supplied chunk and compare copies sharing the same collection letter
   - Select K distinct collection letters and calculate the matching piece position in each
   - XOR those K pieces together to recover the input chunk

### Streaming Pipeline

Both encoding and decoding operate as streaming pipelines:

1. **Encoding Pipeline**:
   ```
   Input Directory → Serialization → Compression → Chunk Processing → Collection Output
   ```

2. **Decoding Pipeline**:
   ```
   Collections → Chunk Processing → Decompression → Deserialization → Output Directory
   ```

This streaming approach allows processing of large datasets without loading everything into memory at once.

## Design Decisions

### Choice of Go

Padlock is implemented in Go for several reasons:

1. **Strong Standard Library**: Go provides robust libraries for cryptography, file I/O, and concurrency
2. **Cross-Platform Support**: Go applications can be compiled for multiple platforms
3. **Performance**: Go offers good performance for both CPU-bound and I/O-bound operations
4. **Simplicity**: Go's straightforward syntax and memory model reduce the risk of security bugs

### Chunk-Based Processing

Data is processed in chunks rather than as a whole for several reasons:

1. **Memory Use**: Payload buffers depend on the chunk size and number of collections rather than the total input size
2. **Streaming Operation**: Allows for pipeline-style processing of data
3. **Parallelization Potential**: Different chunks can be processed in parallel

The default chunk limit is 2 MiB of encoded payload per collection, before headers and container overhead. The input block size is the integer quotient of that limit and C(N-1,K-1).

Initialization uses O(N) storage. The encoder keeps at most N times the configured collection chunk size in current payload buffers, plus its input buffer, compression workspace, RNG workspace, and output-format buffers. Decoding buffers one payload per supplied collection for the current chunk, with additional space for buffer growth, file-format processing, and reconstructed data. Directory listings, TAR chunk indexes, and the Go runtime also consume memory; these are not hard process-memory limits. Large thresholds still increase CPU work and output size: 13-of-26 has 10,400,600 combinations, and each input byte produces 5,200,300 payload bytes per collection. Chunk size controls buffering, not this expansion.

Dry-run encoding uses the same streaming TAR and optional gzip pipeline as actual encoding. Counting readers measure the serialized input before compression and the complete compressed stream, including its trailer, as bytes pass through. The counters retain no payload data. Counts are reported only after successful encoding reaches the end of the stream, which also ensures the compressor has finished updating its input count. Uncompressed dry runs count the TAR stream too; input counts include archive headers and padding. The collection counters measure encoded frames before PNG or collection-TAR overhead, so they are not exact on-disk space estimates.

Every encoding run draws a fresh public 128-bit backup identifier from the OS, separately from the secret pad stream. Each chunk begins with a one-byte header length followed by the text `collection:chunkNumber:dataBytes:backupID`, where the identifier is 32 hexadecimal characters. BIN files and PNG payloads carry the same header. A new identifier is generated for every `Pad.Encode` call, even when the caller reuses the `Pad` instance.

`pad.CheckBackupIDs` compares the first header from every collection and the first payload of every duplicate before restore destination preparation, preserving consumed bytes for decoding. Filesystem adapters expose the chunks already buffered for header reads, so preflight compares their payloads without copying them. Generic stream readers retain at most one first payload per duplicated collection letter and use a reusable comparison buffer; duplicate readers replay the agreed bytes. First chunk numbers must be 1. `Pad.Decode` checks every subsequent header against the first identifier, including collections beyond the K used for reconstruction. Different identifiers abort the operation. The identifier is public consistency metadata; it does not authenticate the payload.

### PNG Output Format

PNG stores the encoded frame in a custom `rAWd` chunk. Its payload CRC catches some accidental corruption; it is not authentication or a concealment guarantee. Image transformations can remove or change the custom payload, so keep the original PNG bytes. BIN stores the same encoded frame directly, with less container overhead.

### TAR Collection Support

The CLI streams one uncompressed outer TAR per collection by default. The serialized source directory is gzip-compressed before splitting; it is a separate stream from the outer collection containers. Library callers can choose uncompressed source serialization. `-files` writes loose collection chunks instead.

Discovery identifies named or renamed TARs from metadata, without temporary extraction. When opening a regular TAR for decoding, the reader scans headers, rejects sparse entries before skipping their bodies, and indexes chunk names with 64-bit offsets and sizes. It skips ordinary bodies with seeks, checking their physical extent, and sorts chunk records numerically without collapsing duplicate names or numbers. Payload reads use bounded sections of the same open file. Memory for the index grows with the number and length of chunk names, not their payload sizes. This adds a metadata pass before decoding; an index is not a snapshot or an authenticity check. Loose files use the same numeric ordering.

Chunk selection uses `[IMG]<collectionID>_<digits>[decoration]` basenames with case-insensitive IMG prefixes, collection letters, and BIN/PNG extensions. Ordering uses the initial decimal digit run after the collection label, ignoring copy suffixes and their digits. Comparison uses normalized digit strings rather than machine integers. AppleDouble `._` sidecars and unrelated names are ignored; other BIN/PNG names are summarized at normal logging level. All matching names are retained, even if their label or format differs. Embedded headers and payloads still determine validity.

Collection archives, loose chunk files, and matching TAR members must be regular files. Loose-reader initialization checks every candidate's type before returning the first chunk, so pre-existing special files fail before destination preparation. File opens recheck type and identity against `Lstat`; Unix opens are nonblocking and do not follow leaf symlinks, preventing a substituted FIFO from hanging during open. Directory aliases and hard links to regular files remain usable. This is not a filesystem snapshot or a guarantee against concurrent in-place changes. Read failures preserve the original cause with archive/member context; TAR read failures close the archive and remain terminal. Header and extent failures found during indexing occur before destination preparation; later body/PNG failures can still leave partial output.

Restoration accepts regular files and directories and rejects sparse entries and unsupported types. Paths are confined beneath an opened `os.Root`, and files are created exclusively. New destination roots and their missing parents use `0700`; existing directory modes remain unchanged. Newly extracted subdirectories have their archived modes applied after extraction, children first, including on failure. Restored file modes are subject to the filesystem and umask; ownership and timestamps are not restored. These are filesystem rules, not authentication of the archived metadata.

`pkg/padlock/decode_pipeline.go` joins reconstruction with archive consumption and waits for extraction/permission cleanup to finish. Cancellation closes the pipe and waits for in-progress operations; it cannot force a blocked filesystem call to return. There is no fixed completion timeout. Partial output can remain on failure.

### Randomness Checks and Seed Lifetime

Output writers serialize chunks without statistical quality warnings. Generator behavior and calibrated sample statistics are tested during development; those tests do not certify unpredictable or independent pads. Reported RNG errors still abort encoding.

Temporary seed and XOR-mixing buffers are cleared on success and failure where owned by Padlock. ChaCha's provider keeps its active cipher state without redundant key/nonce slices. Active state and possible runtime copies remain; clearing buffers is best-effort hygiene, not guaranteed process-memory erasure or forward secrecy.

## Performance Considerations

Padlock is designed with performance in mind:

1. **Streaming Architecture**: Minimizes memory usage for large datasets
2. **Efficient Operations**: XOR operations are computationally inexpensive
3. **Parallelization**: The design allows for potential parallel processing of chunks
4. **Buffered I/O**: Uses buffered I/O for efficient file operations

## Testing Strategy

Padlock includes comprehensive tests:

1. **Unit Tests**: Test individual components in isolation
2. **Integration Tests**: Test the interaction between components
3. **End-to-End Tests**: Test the complete encoding and decoding process
4. **Property-Based Tests**: Verify mathematical properties of the threshold scheme
5. **Randomness Tests**: Check seed handling, failures, stream limits, and sample statistics; these do not prove independence or unpredictability

## Future Enhancements

Potential areas for future enhancement include:

1. **Parallel Processing**: Implement parallel processing of chunks for better performance
2. **Additional Output Formats**: Support for more output formats beyond binary and PNG
3. **Web Interface**: A web-based interface for easier use
4. **Cloud Storage Integration**: Direct integration with cloud storage providers
