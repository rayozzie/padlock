# Padlock: A secret-splitting utility for backups & border-crossings

**Padlock** splits a directory into N collections. Any K distinct, intact collections from the same backup can restore it. For example, a 2-of-3 backup stored in three separate locations can tolerate the loss of any one location. Protection with fewer than K collections depends on how the pads are generated. [Quick start](#quick-start) · [Security model](#security) · [Security review](#security-review)

## Download

Pre-built binaries are available for the following platforms. Go is not required
to run them. Choose ARM64 for Apple Silicon Macs and AMD64 for Intel Macs or
64-bit Intel/AMD PCs; choose the Windows ARM64 build for Windows on Arm.

| Platform | Architecture | Download | SHA256 |
|----------|-------------|----------|--------|
| macOS | ARM64 | [padlock](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/macos-arm64/padlock) | [SHA256](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/macos-arm64/padlock.sha256.txt) |
| macOS | AMD64 | [padlock](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/macos-amd64/padlock) | [SHA256](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/macos-amd64/padlock.sha256.txt) |
| Windows | ARM64 | [padlock.exe](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/windows-arm64/padlock.exe) | [SHA256](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/windows-arm64/padlock.exe.sha256.txt) |
| Windows | AMD64 | [padlock.exe](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/windows-amd64/padlock.exe) | [SHA256](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/windows-amd64/padlock.exe.sha256.txt) |
| Linux | ARM64 | [padlock](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/linux-arm64/padlock) | [SHA256](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/linux-arm64/padlock.sha256.txt) |
| Linux | AMD64 | [padlock](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/linux-amd64/padlock) | [SHA256](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/linux-amd64/padlock.sha256.txt) |
| Linux | ARMv7 | [padlock](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/linux-armv7/padlock) | [SHA256](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/linux-armv7/padlock.sha256.txt) |

Download the executable and its SHA256 file into the same directory, keeping their
original names. Verify the checksum before running:

```bash
# macOS
shasum -a 256 -c padlock.sha256.txt

# Linux
sha256sum -c padlock.sha256.txt
```

On macOS/Linux, run `chmod +x padlock`, then invoke `./padlock`. On Windows,
compare the output of `Get-FileHash .\padlock.exe -Algorithm SHA256` with the
SHA256 file, then invoke `.\padlock.exe` in PowerShell. macOS builds are not
Developer ID-signed or notarized.

The download links track `master`. Keep the executable and matching checksum
together when saving a particular build. Checksums detect changed download bytes;
they are not publisher signatures.

## Key Features

- **Threshold Security:**  
  The data is split into N collections, where any K intact collections (with 2 ≤ K ≤ N ≤ 26) from the same backup can reconstruct the original content. Confidentiality with fewer collections depends on the pad randomness described below.

- **Stream-Pipelined Processing:**  
  Encoding, decoding, and dry runs stream file contents chunk by chunk, including gzip compression. Payload buffering depends on the chunk size and number of collections, rather than the total bytes in the backup. Large K-of-N thresholds still cause combinatorial growth in storage and work.

- **XOR Threshold Splitting:**
  Padlock uses a one-time-pad style threshold scheme. For each input chunk:
  - For each combination of K collections, the system:
    - Generates K-1 random pads and XORs them with the data to create a masked piece
    - Distributes the random pads and the masked piece across the K collections in that combination
  - Each collection contains multiple pieces from different combinations
  - When K or more collections are combined, the original data can be reconstructed
  - The ideal scheme hides the data from fewer than K collections when all pads are uniform, independent, secret, and never reused; the default RNG does not establish these information-theoretic assumptions

- **Local hardware randomness.** Supply fresh raw bytes from a device, pipe, stdin, or single-use file with `-pad-stream`. The encoder mixes one checked hardware byte into every pad byte. A dry run reports the exact stream length the input and threshold require. [Hardware stream examples](#using-a-hardware-random-stream-for-every-pad-byte)

- **Flexible Output Formats:**  
  Chunks use either of two formats inside the default TAR collections, or as individual files with `-files`:
  - **PNG Files:** Files are named using the pattern  
    `IMG<collectionID>_<chunkNumber>.PNG`  
    (for example, if the collection directory is "3C5", the first chunk file is named `IMG3C5_0001.PNG`).
  - **Raw Binary Files (.bin):** Files are named with the format  
    `<collectionID>_<chunkNumber>.bin`

- **User-Friendly Messaging and Error Handling:**  
  Messages intended for users (such as summaries and error notifications) are always displayed. Detailed trace and debug messages, with component-specific prefixes (like "PADLOCK:", "FILE:", etc.), appear only when the `-verbose` flag is set.

## How It Works

### Overview

1. **Encoding Process:**
   - **Archive & Compress:**  
     The CLI archives the input directory as TAR and compresses that stream using gzip. Library callers can choose uncompressed serialization.
   - **Chunking:**  
     The compressed stream is divided so each collection's encoded payload fits the configured chunk limit, before headers and container overhead.
   - **Threshold Splitting:**
     For each chunk, the system:
     - Generates random one-time pads for each combination of K collections
     - XORs the input data with these pads to create masked pieces
     - Distributes the data across collections according to combinatorial mathematics
   - **Collection Organization:**  
     Collections can be stored as directories or as TAR archives. Each collection is named with a pattern that includes the required number (K), a collection letter, and the total number of copies (N) - for example, "3A5" for the first collection in a 3-of-5 scheme.

2. **Decoding Process:**
   - **Collection Discovery:**  
     The available collection directories and TAR files are identified. TAR discovery examines archive headers without extracting files or creating temporary directories; archives with no recognizable collection are ignored. A renamed TAR can be identified by its BIN/PNG chunk filenames. Regular TAR files are indexed by chunk name and byte offset, then read directly in numeric chunk order, including user-packed archives whose members are out of order. Loose collections use the same numeric ordering. macOS AppleDouble `._` sidecars and unrelated filenames are ignored; chunk candidates have the form `[IMG]<collectionID>_<digits>[decoration].bin` or `.png`. The optional IMG prefix, collection letter, and extension accept either case, and copy suffixes such as `IMG2A3_0001 (1).PNG` are supported. Preserve the collection label and initial chunk number; arbitrary prefixes or renumbering are not supported. Ignored non-sidecar BIN/PNG names are summarized at normal logging level. Every matching candidate is retained for validation, including duplicate numbers and different labels or formats. Decoding validates embedded chunk headers and payloads.
   - **Combination Selection:**
     The system determines which combination to use based on the available collections. If fewer than K collections are present, the decoder reports an error.
   - **Data Reconstruction:**  
     For each chunk, the appropriate combination is used to combine pieces from K collections. The XOR operation reconstructs the original data from the distributed pieces.
   - **Extraction:**  
     The reassembled data is decompressed (if needed) and untarred to rebuild the original directory structure and files.

## Security

Padlock's splitting operation uses XOR and combinatorial distribution. The ideal one-time-pad scheme has perfect secrecy only when every pad is uniformly random, independent of the data and all other pads, secret from the attacker, and never reused. This protects the encoded contents, not public metadata such as threshold parameters and the chunk count; see [Visible Metadata](#visible-metadata) for what sizes reveal.

The default generator uses the operating system's random generator and seeded software generators. Those are practical randomness mechanisms, not proof of the ideal assumptions. Padlock therefore does not claim information-theoretic secrecy or unconditional resistance to future classical or quantum attacks for its default implementation.

### Random Number Generation

For every pad byte, the local mixer XORs one byte from each of five generators. The OS supplies the direct contribution and seeds the other four. A failure of that shared source can affect the whole mixer.

| Generator | Input and role |
|---|---|
| `CryptoRand` | Reads Go’s `crypto/rand` for every output buffer. |
| `ChaCha20Rand` | Uses a 32-byte key and 12-byte nonce to generate pad material. It draws a fresh key and nonce before exceeding 256 GiB under one key and nonce. |
| `MathRand` | Uses a separate eight-byte seed. Go’s `math/rand` seed mapping has fewer than 2³¹ initial states. |
| `PCG64Rand` | Uses a 16-byte seed, read as two 64-bit words. |
| `MT19937Rand` | Uses a separate eight-byte seed for Mersenne Twister. |

MathRand, PCG64, and MT19937 are statistical generators. The default mixer's computational protection relies on the OS generator and ChaCha20. Separate seed reads give the generators separate state, while their common OS dependency remains. [Go OS randomness](https://pkg.go.dev/crypto/rand) · [Go statistical generators](https://pkg.go.dev/math/rand/v2)

ChaCha20 generates pad bytes during encoding. Restore reconstructs the archive by XORing collection pieces and requires only the collections. SHA-256 checksums verify the executable download. The public backup ID uses a noncryptographic checksum. [Visible metadata](#visible-metadata)

### Using a hardware random stream for every pad byte

`-pad-stream PATH` takes raw binary output from a local device, pipe, or single-use file. Use `-pad-stream -` for stdin. For every pad byte, Padlock computes **checked stream byte XOR local mixer byte**. The stream supplies one byte per pad byte and supplies none of the software seeds.

First, size the stream for your input and threshold. A dry run reads the input directory without opening the entropy source.

```bash
padlock encode ./input -copies 3 -required 2 -dryrun
```

Find **Pad stream required** in the report. Encode with the same input, threshold, and chunk size. On Linux, if your hardware generator exposes raw output at `/dev/hwrng`, use the following commands.

```bash
padlock encode ./input ./collections -copies 3 -required 2 -pad-stream /dev/hwrng
padlock decode ./collections ./restored
```

| Threshold | Stream bytes for 1 GiB of compressed, chunk-padded archive |
|---|---:|
| 2 of 2 | 1 GiB |
| 2 of 3 | 3 GiB |
| 3 of 5 | 20 GiB |

**Choosing the source.** XOR preserves a secret, uniform contribution when it is independent of the other contributions and of the attacker’s knowledge. A fresh hardware stream with those properties protects pad bytes even if the local mixer becomes predictable. Where hardware bytes are known, protection depends on the OS generator and ChaCha20 in the independent local mixer. Identical streams cancel under XOR.

Use a device mode that takes fresh physical measurements throughout the encode. Keep its output private and use fresh bytes for **every attempt**, including retries. A regular file starts at byte zero each time it is opened. Anyone who supplies, publishes, or records the stream knows its contribution. Keep the stream separate from seed files and outside the backup input and output directories.

On Unix, Padlock rejects `/dev/random` and `/dev/urandom` as pad streams, including symlinks and stdin redirected from those devices. They share the OS generator used by the mixer. A file, pipe, or virtual device can have the same underlying source. Verify where its bytes originate.

**Device headers.** Select raw binary mode. For one fixed header with a documented length, supply that length via `-pad-skip`. For example, this command skips a 64-byte header once and uses the remaining payload.

```bash
padlock encode ./input ./collections -pad-stream ./fresh-device-output.bin -pad-skip 64
```

Budget 64 extra bytes beyond **Pad stream required** for this example. A documented upper bound also works when all subsequent bytes are random. After the skip, Padlock checks the first eight bytes for common file and protocol signatures, including PNG, ZIP, gzip, PDF, and HTTP/1. For unknown-length headers, repeated packet framing, or text such as Base64, hex, or JSON, use the device’s parser to extract the raw payload.

**Startup and rolling health checks.** Before creating or clearing output, Padlock checks up to 512 usable stream bytes, limited to the pad volume the backup is guaranteed to need. They count toward the dry-run total and enter the mixer exactly once. Checks continue on the incoming bytes, before XOR, across reads and backup chunks.

| Check | Encoding stops when |
|---|---|
| Repetition | 13 consecutive bytes have the same value. |
| Byte concentration | Any value occurs at least 36 times in the last 512 bytes, including the initial partial window. |
| Overall bit balance | A full 512-byte window contains at most 1,680 or at least 2,416 ones out of 4,096 bits. |
| Individual bit positions | Any one of the eight bit positions is one in at most 120 or at least 392 bytes of a full window. |

The per-position check catches a stuck bit even when an opposite fault masks it in the overall count. For independent, uniform bytes, the four rolling checks together have a false-alarm probability below 2 in 10¹⁷ per TiB. The signature check can also reject an accidental match. Predictable or replayed binary data can pass these checks. A successful run tells an observer that the rejected patterns were absent. The hardware generator’s own noise-source tests remain important. [NIST SP 800-90B, section 4](https://doi.org/10.6028/NIST.SP.800-90B)

**Timeouts and failed checks.** The default timeout is 10 seconds **without new pad bytes**, including while skipping a header. A device that keeps delivering bytes can take longer to fill a chunk. Set `-entropy-timeout 60s` for a device that pauses longer between deliveries. A stall, exhausted source, read error, or failed health check stops encoding. Diagnostics identify the error without printing pad bytes.

A startup failure leaves existing output intact. After startup, `-clear` can erase an existing backup. On a later failure, earlier chunks can already be on disk, including part of the window that triggered the health check. Discard the entire partial backup and retry with fresh stream bytes. Use a fresh output directory when you need to preserve an existing backup.

### Optional External Entropy

Use `-entropy-file PATH` to supplement the software generators’ **seeds** with fresh local bytes. Repeat the option for multiple sources. Each source can be a file, device, named pipe, or stdin (`-`). Padlock reads these sources sequentially and has no built-in network entropy source.

```bash
padlock encode ./input ./collections -copies 3 -required 2 \
  -entropy-file /media/rng/fresh-seed.bin
```

Each selected source supplies **76 bytes at startup**. Padlock XORs the contributions with 76 fresh OS bytes and assigns separate ranges to MathRand (8 bytes), ChaCha20 (44), PCG64 (16), and MT19937 (8). The direct OS generator also contributes to every pad byte. Each ChaCha20 reseed consumes **44 additional bytes from each seed source and the OS**. The 256 GiB reseed limit measures generated ChaCha20 output.

You can combine `-entropy-file` and `-pad-stream` with separate sources. Independent, secret seed material can improve a software seed. A finite seed expanded by a PRNG provides computational protection. Use a full-length physical stream to contribute fresh hardware randomness at every byte. [Hardware stream sizing and use](#using-a-hardware-random-stream-for-every-pad-byte)

Keep seed files outside the backup directories. Padlock checks file identities to reject duplicate inputs and hard links into the backup input or output tree. Supply fresh contents for each encode. Connect named-pipe producers before starting. `-entropy-timeout` limits each source open and each **complete seed read** to 10 seconds by default. A short read, timeout, or error stops encoding. Initial seeding completes before output clearing. Dry runs leave the sources untouched, and restore requires only the collections.

### Failures and Validation

- The encoder requires a complete read from every selected source. If any generator fails, the mixer returns the error before releasing bytes for that request. The encoder writes a chunk only after it obtains all of that chunk’s pads.
- The CLI reports setup failures for external sources. The standalone generator constructors panic on an incomplete seed, and a fatal failure of Go’s OS random source can terminate the process.
- Generator locks protect shared state during concurrent reads. Padlock clears temporary seed and mixing buffers it owns. Active generator state, runtime copies, and the encoding host remain within the trust boundary.

### Threshold Scheme and Its Assumptions

For N collections with threshold K, there are C(N,K) combinations, and each collection participates in C(N-1,K-1) combinations. For example, a 3-of-5 backup has ten combinations, and each collection participates in six.

Padlock generates combinations as needed and calculates restore positions directly, without keeping all combinations in memory. The storage cost remains: for 13-of-26, each collection contains 5,200,300 payload bytes per input byte, before headers and container overhead. Choose large thresholds carefully; smaller chunks reduce buffer sizes but do not reduce this expansion.

The default `-chunk` is 2 MiB. Encoding, including dry runs, holds at most N times the configured chunk size in current collection payloads, plus input, compression, RNG, and output-format buffers. Decoding buffers the current chunk from each supplied collection. Directory listings, TAR indexes (one name and offset/size record per chunk), and Go's runtime also use memory, so `-chunk` is not a hard limit on process memory. Dry runs count input bytes before and after streaming compression without retaining the complete input or compressed archive; the input count includes TAR headers and padding.

For each data chunk D and each K-collection combination:

1. Request K-1 fresh pads R_1, R_2, ..., R_(K-1).
2. Compute C = D XOR R_1 XOR R_2 XOR ... XOR R_(K-1).
3. Distribute C and the K-1 pads across that combination's K collections.

Combining all K pieces recovers D because each pad cancels itself. With fewer than K collections, every combination is missing at least one piece. Under the ideal randomness assumptions above, those missing pieces hide D. Underdetermined equations alone do not prove secrecy if pads are predictable or correlated.

The generator is reused across chunks. New reads consume new output, but chunk boundaries do not create independent entropy sources or guarantee forward secrecy after generator-state compromise.

### Visible Metadata

Collection names and chunk headers expose K, N, the collection letter, the chunk count, and a public 128-bit backup ID. The ID helps detect accidentally mixed collections. Padlock computes it with FNV-1a over clock time, elapsed time, process ID, and a per-process counter. Treat those inputs as public. The label consumes zero random bytes, can be copied, and can repeat if its inputs coincide.

Padlock pads the compressed archive to whole input chunks. With the default 2-of-3 settings, each input chunk holds 1 MiB, so file sizes reveal the number of these chunks. A larger `-chunk` makes the size information coarser and increases memory use. Gzip padding occupies its own member and can require additional whole chunks to fit its framing. An encode dry run reports the compressed length and the alignment padding separately.

### Operational Boundaries

- Protect the host and its random generator during encoding, and keep fewer than K collections available to an attacker.
- The encoding host keeps the plaintext. Padlock reads the input directory and leaves it in place; filesystem snapshots (Time Machine, APFS, Volume Shadow Copy), swap and hibernation files, search indexes, and terminal scrollback (encoding lists every file name at normal logging level) can retain copies after the input is deleted, and shell history retains the command line, including input and output paths. Encode on a host whose storage is encrypted or that you can wipe, and treat the source directory as sensitive until it has been deliberately removed.
- Symbolic links inside the input are skipped, not followed. Encoding reports each one and ends with a warning giving the count; check it before relying on the backup, because the link targets are not in it.
- Never reuse pad bytes for different data or restart generators from previously used state. The implementation requests fresh output for each pad but cannot certify its uniqueness or unpredictability.
- Restore checks the backup IDs in the first chunk headers before creating or clearing the destination, and checks subsequent headers during decoding. Every supplied collection must belong to the same backup, including extra collections and collections supplied in a dry run.
- Restore requires K distinct collection letters. Additional copies of a collection are accepted only when their metadata and encoded payloads agree, checked chunk by chunk across every supplied copy. Conflicting, incomplete, or unreadable copies cause failure even when other collections meet the threshold. Duplicate first-chunk payloads are compared before the destination is created or cleared. Run a decode with `-dryrun` to check the complete backup before using `-clear`; a later failure can still leave a partial restore, and the dry run does not protect against subsequent input changes or destination failures. Matching copies do not authenticate the data.
- Restores wait for extraction and directory permission cleanup to finish, without a fixed completion timeout. Library callers can cancel through their context; cancellation stops further processing and waits for in-progress filesystem operations and cleanup, which may take time. A canceled restore can leave partial output.
- Collection headers, PNG CRCs, and archive validation detect some damage and inconsistencies. They do not authenticate the restored contents or reliably detect deliberate alterations.
- Padlock rejects sparse TAR entries in collection archives and decoded directory archives. Its encoder writes ordinary TAR entries, including for sparse input files.
- Backups include regular files and directories. The input directory itself may be a symlink; encoding and dry runs follow its target. Symbolic links within the input tree are skipped and reported, with a final count. FIFOs, devices, sockets, and other special files cause encoding to fail before output directories are created or cleared. Input readability is also checked at that point, but this preflight is not a filesystem snapshot: later changes or read failures can still leave partial output. Restore and dry-run restore reject unsupported TAR entry types. Collection archives and loose chunk files must be regular files; special files and file symlinks discovered at preflight are rejected before creating or clearing the restore destination. Files are checked again when opened, but later input changes can still cause a partial restore.
- Multiple output directories must be separate: no repeated paths, aliases, or parent/child nesting. The complete set is checked before creating or clearing any destination, including layouts supplied to a dry run. For directories that do not exist yet, names differing only by case or Unicode normalization are conservatively treated as potentially identical. On Windows, new directory components resembling 8.3 aliases or ending in a dot or space are rejected; use full, unambiguous names. Separate directories do not establish separate devices or independent failure domains.
- On macOS and Linux, newly created output and collection directories use owner-only permissions (`0700`), and generated collection files and TAR archives use `0600`. Collection entries inside TAR archives also use `0600`. This applies to both CLI and library restore destinations. Existing directories retain their permissions; restored files and newly restored subdirectories use the archived modes where supported, subject to filesystem behavior. Collection writers refuse to overwrite existing files. Windows access is governed by the destination's ACLs.

### Documentation

- [Overview](docs/wiki/Overview.md) - High-level overview of the Padlock system
- [Architecture](docs/wiki/Architecture.md) - Technical architecture details
- [Usage Guide](docs/wiki/Usage-Guide.md) - Instructions for using Padlock
- [Security Model](docs/wiki/Security-Model.md) - Security principles and implementation
- [Implementation Details](docs/wiki/Implementation-Details.md) - Code organization and design

## Installation and Usage

### Building from Source

Install Git and Go 1.27.4 or later; use a supported Go release with current security
patches. Then clone the repository and build from its root:

```bash
git clone https://github.com/rayozzie/padlock.git
cd padlock
go build -o padlock ./cmd/padlock
```

On Windows, use `go build -o padlock.exe ./cmd/padlock`. The examples below use
`padlock` for an executable on your PATH; use `./padlock` or `.\padlock.exe` when
running it from the current directory.

`./build.sh` uses Bash to build all seven supported targets and their SHA256 files,
with cgo disabled and local source paths removed. It stages every build before
replacing packaged outputs. Run it from a clean commit when preparing published
binaries: `go version -m <binary>` records that source revision, which precedes
the commit publishing the resulting artifacts. `bin/padlock` is the macOS ARM64
convenience copy, not a portable executable for every platform.

### Quick Start

With an existing `input` directory, create three collections requiring any two:

```bash
padlock encode ./input ./collections -copies 3 -required 2 -format bin
padlock decode ./collections ./restored -dryrun
padlock decode ./collections ./restored
```

This creates `2A3.tar`, `2B3.tar`, and `2C3.tar`. For a recovery check using only
two collections, place any two of those files in a separate directory and use it
as the decode input. Keep collections on storage locations appropriate to your
recovery and access requirements. Padlock reads collection TARs and loose collection directories. Extract a ZIP
wrapper before passing its contents to the decoder.

To mix your own random generator into every pad byte, add `-pad-stream PATH` when encoding. [Hardware stream examples](#using-a-hardware-random-stream-for-every-pad-byte)

### Command-Line Usage

- **Encode:**

  ```text
  padlock encode <inputDir> <outputDir> -copies 5 -required 3 -format png -chunk 2097152 [-clear] [-verbose] [-files] [-dryrun]
  padlock encode <inputDir> <outputDir1> <outputDir2> ... <outputDirN> [-required K] [options]
  ```

  - `<inputDir>`: Directory containing the data to be archived and encoded.
  - `<outputDir>`: Destination directory for the generated collection TAR files or subdirectories.
  - `-copies`: Number of collections to create (must be between 2 and 26). With multiple output directories, defaults to their count; an explicitly supplied value must match. Conflicts are rejected before outputs are created or cleared, including in dry runs.
  - `-required`: Minimum number of collections required for reconstruction, from 2 through `-copies`. It must be supplied whenever `-copies` is greater than 2 with one output directory; with multiple output directories it defaults to all of them. Missing or invalid values fail before outputs are created or cleared, including in dry runs.
  - `-format`: Output format, either "bin" or "png" (default: "png").
  - `-chunk`: Maximum encoded payload bytes per collection chunk, before headers and container overhead. The minimum is C(N-1, K-1): for example, 2 bytes for 2-of-3 or 6 bytes for 3-of-5. Invalid sizes are rejected before output directories are created or cleared, including with `-clear` or `-dryrun`. Every backup is padded to a whole number of input chunks (`-chunk` ÷ C(N-1, K-1) bytes each), so collection sizes reveal only the chunk count; see [Visible Metadata](#visible-metadata).
  - `-clear` authorizes clearing the specified output directories after startup checks. A later failure can leave a partial backup.
  - `-verbose`: (Optional) Enables detailed trace/debug messages.
  - `-files`: (Optional) Creates individual files for each collection instead of TAR archives.
  - `-dryrun`: (Optional) Process the complete stream and report sizes without writing output files. Encoding counts frames before PNG/TAR overhead and reports the chunk alignment padding; these are not exact disk-space estimates.
  - `-pad-stream PATH` XORs one checked byte from a raw device, pipe, or file with each normal mixer byte. Use `-` for stdin. Run `-dryrun` to size the stream. [Hardware stream examples](#using-a-hardware-random-stream-for-every-pad-byte)
  - `-pad-skip N` discards a documented one-time header of N bytes from `-pad-stream` before checking and consuming the pad data. Defaults to zero.
  - `-entropy-file PATH`: (Optional, repeatable, encode only) Supplement seed material with external raw bytes; see [Optional External Entropy](#optional-external-entropy).
  - `-entropy-timeout DURATION`: (Optional, encode only) Set the maximum idle time for pad bytes and the total time per source open or seed read (default `10s`).

- **Decode:**

  ```text
  padlock decode <inputDir> <outputDir> [-clear] [-verbose] [-dryrun]
  padlock decode <inputDir1> <inputDir2> ... <inputDirN> <outputDir> [options]
  padlock decode <inputDir> -dryrun
  ```

  - `<inputDir>`: Root directory containing the collection subdirectories or TAR files.
  - `<outputDir>`: Destination directory where the original data will be restored.
  - `-clear`: (Optional) Clears the output directory before decoding.
  - `-verbose`: (Optional) Enables detailed trace/debug messages.
  - `-dryrun`: (Optional) Reconstruct and validate the complete stream without writing restored files. Checks cannot certify authenticity or predict every destination-specific error.

Put directory arguments before flags. With multiple decode inputs, the last
directory argument is always the output, even in a dry run. Omit the output only
for a single-input dry run. Multiple inputs may be collection directories or
directories containing collection subdirectories/TAR files.

**Important:**  
For encoding and decoding, input and output directories must be separate: neither may be the same as, or contain, the other. Padlock checks all input/output pairs, including relative paths and symlink aliases, before clearing or creating any output directory. Dry runs do not prepare output directories. Also, ensure that the number of available collections meets or exceeds the required threshold; otherwise, an error will be displayed.

## Implementation Details

- **Source File Organization:**
  - **cmd/padlock/main.go:** The command-line interface entry point.
  - **cmd/padlock/entropy.go:** Local file, device, pipe, and stdin entropy sources, with validation and timeouts.
  - **pkg/padlock/padlock.go:** Coordinates the encoding and decoding processes, integrating the various components.
  - **pkg/file/:** Contains modules for file and directory operations:
    - **format.go:** Implementations for working with different file formats (BIN and PNG).
    - **directory.go:** Directory validation and management.
    - **archive.go:** Streaming TAR collection writers and archive helpers.
    - **collection_discovery.go:** TAR metadata probing without temporary extraction.
    - **collection_index.go:** Numeric chunk indexes for direct reads from user-packed TARs.
    - **extract.go:** Confined archive extraction and directory permission restoration.
    - **collection.go:** Collection directory operations.
    - **serialize.go:** Directory serialization/deserialization to/from tar streams.
    - **compress.go:** Stream compression/decompression using gzip.
    - **padding.go:** Chunk alignment padding that hides the exact compressed length.
  - **pkg/pad/pad.go:** Core implementation of the one-time pad threshold scheme.
  - **pkg/pad/rng.go:** XORs the five normal generators with the optional full-length pad stream. Separate external seed sources can supplement OS randomness at initialization and reseeding.
  - **pkg/trace/trace.go:** Context-based logging system for debug and trace information.

## Security review

Claude Mythos 5.2 reviewed the full-length pad stream on November 23, 2026. Its macOS ARM64 tests traced individual bytes from files and pipes into saved collections across five threshold configurations. Short reads, startup-probe boundaries, and forced ChaCha20 reseeds all preserved byte alignment. Each stored pad byte equaled one checked stream byte XOR one local mixer byte. Seed reads consumed zero pad-stream bytes. [Reviewed source](https://github.com/rayozzie/padlock/tree/7043347d7bf4ab922638c41762cbffa396a755a6)

The review reported three medium and two low findings in the reviewed source, with no high or critical findings. The following safeguards and tests cover all five areas.

| Review area | What Padlock checks |
|---|---|
| Slow producers | A stream that keeps delivering bytes can take longer than the idle timeout to fill a chunk. A stalled stream stops the encode. [Timeout tests](cmd/padlock/pad_stream_preflight_test.go) |
| Existing backups | Startup health checks run before `-clear` touches the destination. A later failure can leave partial output. [Startup tests](cmd/padlock/pad_stream_preflight_test.go) |
| Stuck bits and text input | Each bit position is checked over a rolling 512-byte window. This catches faults that can leave the total count of one bits balanced. [Health-check tests](cmd/padlock/pad_stream_health_test.go) |
| Source independence | Unix device-identity checks reject `/dev/random` and `/dev/urandom`, including aliases and redirected stdin. The operator verifies the origin of any file, pipe, or virtual device. [Device tests](cmd/padlock/entropy_unix_test.go) |
| Byte-for-byte mixing | Regression tests compare actual pad values across a ChaCha20 reseed and in saved collections. [Reseed test](pkg/pad/rng_pad_stream_value_test.go) · [Collection test](cmd/padlock/pad_stream_trace_test.go) |

The review covered encoder source and controlled input streams. Physical entropy quality, host compromise, Windows behavior, packaged executables, and the restore path were outside its scope.

## License

[MIT License](LICENSE)
