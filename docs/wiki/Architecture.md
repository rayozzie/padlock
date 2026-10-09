# Padlock Architecture

Padlock is built with a modular architecture consisting of several key components that work together to provide secure data encoding and decoding.

## System Components

### 1. Command-Line Interface

The CLI, implemented in `cmd/padlock/main.go`, serves as the entry point for user interaction with the Padlock system. It handles:

- Command-line argument parsing and validation
- Parameter configuration and validation
- Coordination of high-level encoding and decoding operations
- Error reporting and user feedback

The CLI supports two main commands:
- `encode`: Split input data across N collections with K-of-N threshold security
- `decode`: Reconstruct original data using K or more collections

### 2. Threshold Splitting Engine

The threshold splitting engine, primarily implemented in the `pkg/pad` package, is responsible for the mathematical operations that provide the threshold security. Key components include:

#### Pad Creation (`pkg/pad/pad.go`)

This component generates the mathematical structure for distributing data across collections. It:
- Counts the pieces per collection without allocating combination tables
- Visits combinations in sorted order during encoding and calculates their positions directly during decoding
- Ensures that any K collections can reconstruct the data
- Manages the distribution of encoded chunks across collections

#### Random Number Generation (`pkg/pad/rng.go`)

The RNG component XORs output from five generators: `CryptoRand`, `MathRand`, `ChaCha20Rand`, `PCG64Rand`, and `MT19937Rand`. `CryptoRand` calls Go's `crypto/rand` for every output buffer; the other generators consume separate seed bytes from `crypto/rand.Reader`.

By default, all five share the OS randomness source. Separate state and algorithms provide algorithm diversity, not independently supplied entropy. MathRand, PCG64, and MT19937 are statistical PRNGs, not security fallbacks. Any reported generator error stops mixing; it does not silently reduce the set of generators.

The encode CLI can supplement seeds with repeatable file/device/pipe and HTTPS inputs. `pad.NewRandWithEntropy` XORs fresh contributions with OS seed bytes and retains the selected sources for later ChaCha20 reseeds. Sources are initialized before output clearing. See the [Security Model](Security-Model) for the independence assumption required by XOR mixing and the limits of seeded software generators.

#### XOR Operations

The splitting operation uses XOR. The ideal scheme provides perfect secrecy only with uniform, independent, secret pads that are never reused. The default RNG does not establish these information-theoretic assumptions.

### 3. File System Layer

The file system layer, implemented in the `pkg/file` package, handles all interactions with the file system:

#### Collection Management

This component creates and manages collections as directories or streaming TAR archives:
- `file/collection.go`: Defines the structure and operations for collections
- `file/directory.go`: Handles directory operations for collections
- `file/archive.go`: Provides TAR archive support for collections
- `file/collection_discovery.go`: Identifies named and renamed TAR collections from metadata without extracting them
- `file/extract.go`: Confines restored files beneath an opened output root and restores directory modes after writing

#### Format Handling

Padlock supports multiple output formats for storing encoded data:
- `file/format.go`: Defines the interface for different formats
- Binary format: Stores data chunks directly as binary files
- PNG format: Stores data chunks as PNG images with CRC validation

#### Serialization

The serialization components convert directories to/from tar streams for processing:
- `file/serialize.go`: Implements directory serialization and deserialization
- `file/compress.go`: Provides compression and decompression functionality

### 4. Process Orchestration

The process orchestration layer, implemented in `pkg/padlock/padlock.go`, coordinates the overall encoding and decoding processes:

#### Streaming Pipeline

Sets up the data processing pipeline that:
1. Serializes input directories to tar streams
2. Optionally compresses the serialized data
3. Processes the data through the one-time pad encoder/decoder
4. Distributes encoded chunks across collections or reconstructs original data

#### Chunk Management

Manages the chunking of data for processing, ensuring that:
- Chunks are of appropriate size for efficient processing
- Chunk metadata is properly maintained
- Chunks are correctly distributed across collections

#### Error Handling

Provides robust error detection and reporting throughout the pipeline, with context-aware error messages that help diagnose issues.

## Data Flow

### Encoding Process

1. **Input Validation**: Check input readability and supported file types, parameters, and all input/output and output/output path relationships before creating or clearing outputs
2. **Pad Creation**: Configure threshold splitting with specified K-of-N parameters
3. **Collection Setup**: Create collection directories for encoded data
4. **Serialization**: Convert input directory to tar stream
5. **Compression**: Optionally compress serialized data
6. **Chunk Processing**: Process data in chunks through the encoder
   - Generate random one-time pads for each chunk
   - XOR input data with pads to create ciphertext
   - Distribute results across collections
7. **Output Formatting**: Write chunks to collections in specified format
8. **Finalization**: Finish and close the default TAR collection streams; `-files` writes loose chunks instead

### Decoding Process

1. **Input Validation**: Verify input and output directories
2. **Collection Discovery**: Locate and load available collections
3. **Reader Setup**: Create readers for each collection
4. **Backup Check and Preparation**: Index regular collection TARs in numeric chunk order; check the first headers, backup identifiers, K/N parameters, distinct collection letters, and duplicate first-chunk payloads before creating or clearing the destination
5. **Chunk Processing**: Process collections through the decoder
   - Read chunks from collections and check each backup identifier against the first
   - Compare every duplicate collection payload and combine K distinct collections according to the threshold scheme
   - Reconstruct original data
6. **Decompression**: Optionally decompress data
7. **Deserialization**: Restore regular files and directories, reject unsafe paths and unsupported types, and wait for permission cleanup before returning, including on failure or cancellation

The decoder returns entry and filesystem causes together with reconstruction errors. It has no fixed post-decode completion timeout. Later failures can leave partial output. Dry runs reconstruct and validate the stream while discarding restored content; they do not create or clear the destination. TAR readers stop at an entry read failure and retain that failure, so subsequent reads cannot restart the archive.

## Component Interactions

The components interact through well-defined interfaces:

- **CLI → Padlock**: The CLI calls the high-level `EncodeDirectory` and `DecodeDirectory` functions in the padlock package.
- **Padlock → Pad**: The padlock package uses the pad package for the core XOR threshold splitting.
- **Padlock → File**: The padlock package uses the file package for file system operations.
- **Pad → File**: The pad package uses callback functions provided by the padlock package to write chunks to the file system.

This modular design allows for:
- Clear separation of concerns
- Easier testing and maintenance
- Potential for future extensions (e.g., new output formats, compression methods)
