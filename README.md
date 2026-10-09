# Padlock: A secret-splitting utility for backups & border-crossings

**Padlock** is a streaming K-of-N utility for splitting data across collections for backups and border-crossings. It combines random pads with data using XOR, then distributes the pieces so that any K intact collections from the same backup can recover the original content. The splitting operation uses XOR and combinatorial distribution; its confidentiality depends on the randomness used to generate the pads. See [Security](#security) for the assumptions and limitations of the default generator.

## Download

Pre-built binaries are available for the following platforms:

| Platform | Architecture | Download | SHA256 |
|----------|-------------|----------|--------|
| macOS | ARM64 | [padlock](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/macos-arm64/padlock) | [SHA256](bin/macos-arm64/padlock.sha256.txt) |
| macOS | AMD64 | [padlock](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/macos-amd64/padlock) | [SHA256](bin/macos-amd64/padlock.sha256.txt) |
| Windows | ARM64 | [padlock.exe](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/windows-arm64/padlock.exe) | [SHA256](bin/windows-arm64/padlock.exe.sha256.txt) |
| Windows | AMD64 | [padlock.exe](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/windows-amd64/padlock.exe) | [SHA256](bin/windows-amd64/padlock.exe.sha256.txt) |
| Linux | ARM64 | [padlock](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/linux-arm64/padlock) | [SHA256](bin/linux-arm64/padlock.sha256.txt) |
| Linux | AMD64 | [padlock](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/linux-amd64/padlock) | [SHA256](bin/linux-amd64/padlock.sha256.txt) |
| Linux | ARMv7 | [padlock](https://github.com/rayozzie/padlock/raw/refs/heads/master/bin/linux-armv7/padlock) | [SHA256](bin/linux-armv7/padlock.sha256.txt) |

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
     The input directory is archived using tar and optionally compressed using gzip.
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

Padlock's splitting operation uses XOR and combinatorial distribution. The ideal one-time-pad scheme has perfect secrecy only when every pad is uniformly random, independent of the data and all other pads, secret from the attacker, and never reused. This protects the encoded contents, not public metadata such as collection sizes and threshold parameters.

The default generator uses the operating system's random generator and seeded software generators. Those are practical randomness mechanisms, not proof of the ideal assumptions. Padlock therefore does not claim information-theoretic secrecy or unconditional resistance to future classical or quantum attacks for its default implementation.

### Random Number Generation

By default, `MultiRNG` XORs the output of five generators:

| Generator | How it obtains randomness | Role and limitations |
|-----------|---------------------------|----------------------|
| `CryptoRand` | Go's `crypto/rand` on every read | Uses the OS randomness source, which also supplies the other generators' seeds |
| `MathRand` | A separate eight-byte seed from `crypto/rand.Reader` | Legacy `math/rand` reduces the seed to fewer than 2^31 initial states; a statistical PRNG, not a security fallback |
| `ChaCha20Rand` | A separate 32-byte key and 12-byte nonce from `crypto/rand.Reader` | A seeded generator; obtains a fresh key and nonce before exceeding its 256 GiB stream limit |
| `PCG64Rand` | Two 64-bit seed words from a separate 16-byte read of `crypto/rand.Reader` | Both words are random rather than time-based; still a statistical PRNG, not a security fallback |
| `MT19937Rand` | A separate eight-byte seed from `crypto/rand.Reader` | Mersenne Twister, a statistical PRNG, not a security fallback |

Without external entropy options, these are **five generator algorithms sharing one OS randomness source**. Go's `crypto/rand` is the standard source for security-sensitive random bytes. Separate draws from a functioning OS RNG are intended to provide unpredictable values. The generators have separate state and consume separate seed bytes; their seeds are not deliberately reused.

The shared OS source is a common trust dependency. The OS may combine multiple entropy inputs internally; Padlock does not separately control or verify their independence. A compromised OS randomness source could affect both direct output and every seed. Separate reads of `/dev/urandom`, timestamps, process IDs, or hashing those values would not establish an independent source either. Padlock does not automatically collect hardware or process-specific entropy separately. External sources can be selected explicitly as described below.

XOR preserves a secret uniform input when it is independent of the other inputs and the attacker's knowledge. Without that assumption, it is not automatically as strong as its strongest input: identical streams cancel because `X XOR X = 0`. The default mixture provides algorithm diversity, with no guarantee of surviving compromise of the shared source or host process.

The statistical generators remain in the mixture for algorithm diversity. Seeding them with OS random bytes does not make them secure on their own. Go documents this limitation for [math/rand](https://pkg.go.dev/math/rand) and [math/rand/v2](https://pkg.go.dev/math/rand/v2); [crypto/rand](https://pkg.go.dev/crypto/rand) documents the OS randomness interface.

### Optional External Entropy

Encoding can supplement the OS source with one or more sources of your choosing:

```bash
padlock encode ./input ./collections -copies 3 -required 2 \
  -entropy-file /media/rng/fresh-seed.bin \
  -entropy-url 'https://your-entropy-service.example/raw' \
  -entropy-timeout 10s
```

- `-entropy-file PATH` reads raw bytes from a file, device, or named pipe. Use `-entropy-file -` to read a producer's raw bytes from stdin. The file is read sequentially and is never rewound or cycled within the operation.
- `-entropy-url URL` fetches fresh raw bytes over HTTPS with normal certificate validation. Both source options are repeatable. They are disabled by default; ordinary operation makes no network requests.
- `-entropy-timeout DURATION` limits each source open/read (default `10s`). A selected source's timeout, short read, exhaustion, or reported error stops encoding. Initial seeding completes before output directories can be cleared.
- Dry runs validate option syntax without opening files, consuming stdin, or contacting services. They do not verify source availability. Decoding requires only the collections and never contacts these external sources.

At initialization, **each external source supplies 76 bytes**. Padlock XORs those bytes with 76 fresh OS bytes and contributions from every other selected source, then assigns separate ranges to MathRand (8), ChaCha20 (44), PCG64 (16), and MT19937 (8). The direct `CryptoRand` output provider remains active. Each later ChaCha20 reseed requires **44 additional fresh bytes from every source** and the OS, before its 256 GiB generated-stream limit is exceeded. That limit measures generated RNG output, not input file size.

Keep entropy files outside the backup input and output directories. Padlock rejects the same local file/device/pipe selected twice, including file aliases, and rejects entropy inputs also present in the input or output tree, including hard links. A new invocation opens a regular file at its beginning: supply a new file or fresh contents for every encoding run. Padlock does not modify seed files or track their reuse between runs. Named-pipe producers must be connected before a read begins. Padlock waits for delayed or partial bytes within `-entropy-timeout`; receiving some bytes does not restart that timeout. An empty pipe with no producer fails rather than being retried indefinitely.

#### HTTPS Source Contract

The URL must return a new `200 OK` response with `Content-Type: application/octet-stream` and at least the requested number of **raw bytes**, not JSON, hexadecimal text, or HTML. Each request is a GET with an `X-Padlock-Entropy-Bytes` header (`76` initially, `44` on reseeding). Extra response bytes are discarded when the response is closed. The client requests no caching, rejects responses marked with a nonzero `Age`, and refuses redirects and content encoding. URLs with userinfo or fragments are rejected. Use a file or pipe adapter for providers that use a different API format.

Requests do not contain local seeds, backup data, or other sources' contributions. Response bytes and URL query credentials are not logged or stored in collections. HTTPS uses TLS for transport; the XOR splitting and backup format are unchanged.

#### What the Additional Sources Establish

This option lets the operator add separately obtained seed material. When a contribution is uniform, secret from the attacker, and independent of the other contributions, XOR preserves that property in the combined seed. Choose sources with separate origins and trust dependencies; multiple paths to the same device or services backed by the same generator do not establish independence.

A remote provider knows its contribution. Public beacons provide public values and cannot substitute for secret entropy if the other sources become predictable. HTTPS confidentiality also depends on TLS and the local host; Go's TLS implementation uses OS randomness, so HTTPS alone does not guarantee protection against a compromised local RNG. An independently supplied physical source can avoid that particular transport dependency. All sources still pass through the encoding host.

Additional seed sources improve diversity when chosen appropriately. They do not make MathRand, PCG64, or MT19937 secure generators, or turn finite seeds expanded by software into information-theoretic one-time pads. Padlock reports the number of additional sources used; it does not certify their independence, secrecy, or quality.

### Failures and Validation

- Seed reads must fill the entire requested seed. Initialization stops if a complete seed cannot be obtained; it does not substitute a zero seed, a timestamp, or partial data. The optional-source CLI reports initialization errors normally; older standalone constructors panic on initialization errors. The OS RNG may terminate the process on failure.
- If any generator returns an error during mixing, `MultiRNG` returns that error and leaves the caller's output buffer unchanged. It does not silently drop a failed generator.
- A generator returning bytes successfully does not prove those bytes are unpredictable. Padlock cannot detect a compromised source that returns plausible bytes without reporting an error.
- Development tests exercise generator behavior and sample statistics. Output writers do not certify randomness or issue byte-frequency quality warnings. Statistical tests cannot establish independence, secrecy, or resistance to prediction; deterministic generators can pass them.
- Temporary seed and mixing buffers are cleared after use where the implementation owns them. Active generators retain the state they need. This cleanup does not guarantee erasure of all memory copies or protect a compromised process.
- Generator locks protect concurrent access to their state. They do not isolate the generators from a compromised process or OS.

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

### Operational Boundaries

- Protect the host and its random generator during encoding, and keep fewer than K collections available to an attacker.
- Never reuse pad bytes for different data or restart generators from previously used state. The implementation requests fresh output for each pad but cannot certify its uniqueness or unpredictability.
- Each new backup has a public identifier repeated in every chunk. Decode rejects different identifiers across any supplied collections, including extra collections beyond the required threshold. The first headers are checked before creating or clearing the destination; subsequent chunks are checked during decoding. Dry runs apply the same checks.
- Restore requires K distinct collection letters. Additional copies of a collection are accepted only when their metadata and encoded payloads agree, checked chunk by chunk across every supplied copy. Conflicting, incomplete, or unreadable copies cause failure even when other collections meet the threshold. Duplicate first-chunk payloads are compared before the destination is created or cleared. Run a decode with `-dryrun` to check the complete backup before using `-clear`; a later failure can still leave a partial restore, and the dry run does not protect against subsequent input changes or destination failures. Matching copies do not authenticate the data.
- Restores wait for extraction and directory permission cleanup to finish, without a fixed completion timeout. Library callers can cancel through their context; cancellation stops further processing and waits for in-progress filesystem operations and cleanup, which may take time. A canceled restore can leave partial output.
- Collection headers, PNG CRCs, and archive validation detect some damage and inconsistencies. They do not authenticate the restored contents or reliably detect deliberate alterations.
- Padlock rejects sparse TAR entries in collection archives and decoded directory archives. Its encoder writes ordinary TAR entries, including for sparse input files.
- Backups include regular files and directories. The input directory itself may be a symlink; encoding and dry runs follow its target. Symbolic links within the input tree are skipped. FIFOs, devices, sockets, and other special files cause encoding to fail before output directories are created or cleared. Input readability is also checked at that point, but this preflight is not a filesystem snapshot: later changes or read failures can still leave partial output. Restore and dry-run restore reject unsupported TAR entry types. Collection archives and loose chunk files must be regular files; special files and file symlinks discovered at preflight are rejected before creating or clearing the restore destination. Files are checked again when opened, but later input changes can still cause a partial restore.
- Multiple output directories must be separate: no repeated paths, aliases, or parent/child nesting. The complete set is checked before creating or clearing any destination, including layouts supplied to a dry run. For directories that do not exist yet, names differing only by case or Unicode normalization are conservatively treated as potentially identical. On Windows, new directory components resembling 8.3 aliases or ending in a dot or space are rejected; use full, unambiguous names. Separate directories do not establish separate devices or independent failure domains.
- On macOS and Linux, newly created output and collection directories use owner-only permissions (`0700`), and generated collection files and TAR archives use `0600`. Collection entries inside TAR archives also use `0600`. This applies to both CLI and library restore destinations. Existing directories retain their permissions; restored files and newly restored subdirectories use the archived modes where supported, subject to filesystem behavior. Collection writers refuse to overwrite existing files. Windows access is governed by the destination's ACLs.

### Documentation

- [Overview](docs/wiki/Overview.md) - High-level overview of the Padlock system
- [Architecture](docs/wiki/Architecture.md) - Technical architecture details
- [Usage Guide](docs/wiki/Usage-Guide.md) - Instructions for using Padlock
- [Security Model](docs/wiki/Security-Model.md) - Security principles and implementation
- [Implementation Details](docs/wiki/Implementation-Details.md) - Code organization and design

## Installation and Usage

### Requirements

- Go 1.27.2 or later; use a supported release with current security patches (needed when building from source)
- A standard Go build environment

### Building Padlock

To build the utility, run the following command in your terminal. (Simply copy and paste the command as-is.)

```bash
go build -o padlock ./cmd/padlock
```

`./build.sh` builds all seven supported targets and their SHA256 files. It stages every build before replacing packaged outputs. `bin/padlock` is the macOS ARM64 convenience copy.

### Command-Line Usage

- **Encode:**

  padlock encode <inputDir> <outputDir> -copies 5 -required 3 -format png -chunk 2097152 [-clear] [-verbose] [-files] [-dryrun]

  - `<inputDir>`: Directory containing the data to be archived and encoded.
  - `<outputDir>`: Destination directory for the generated collection TAR files or subdirectories.
  - `-copies`: Number of collections to create (must be between 2 and 26). With multiple output directories, defaults to their count; an explicitly supplied value must match. Conflicts are rejected before outputs are created or cleared, including in dry runs.
  - `-required`: Minimum number of collections required for reconstruction, from 2 through `-copies`. Defaults to 2 with one output directory, or all collections with multiple output directories. An explicitly supplied valid value overrides this default. Invalid values fail before outputs are created or cleared, including in dry runs.
  - `-format`: Output format, either "bin" or "png".
  - `-chunk`: Maximum encoded payload bytes per collection chunk, before headers and container overhead. The minimum is C(N-1, K-1): for example, 2 bytes for 2-of-3 or 6 bytes for 3-of-5. Invalid sizes are rejected before output directories are created or cleared, including with `-clear` or `-dryrun`.
  - `-clear`: (Optional) Clears the output directory before encoding.
  - `-verbose`: (Optional) Enables detailed trace/debug messages.
  - `-files`: (Optional) Creates individual files for each collection instead of TAR archives.
  - `-dryrun`: (Optional) Process the complete stream and report sizes without writing output files. Encoding counts frames before PNG/TAR overhead; these are not exact disk-space estimates.
  - `-entropy-file PATH`, `-entropy-url URL`: (Optional, repeatable, encode only) Supplement seed material with external raw bytes; see [Optional External Entropy](#optional-external-entropy).
  - `-entropy-timeout DURATION`: (Optional, encode only) Limit each external source open/read; default `10s`.

- **Decode:**

  padlock decode <inputDir> <outputDir> [-clear] [-verbose] [-dryrun]

  - `<inputDir>`: Root directory containing the collection subdirectories or TAR files.
  - `<outputDir>`: Destination directory where the original data will be restored.
  - `-clear`: (Optional) Clears the output directory before decoding.
  - `-verbose`: (Optional) Enables detailed trace/debug messages.
  - `-dryrun`: (Optional) Reconstruct and validate the complete stream without writing restored files. Checks cannot certify authenticity or predict every destination-specific error.

**Important:**  
For encoding and decoding, input and output directories must be separate: neither may be the same as, or contain, the other. Padlock checks all input/output pairs, including relative paths and symlink aliases, before clearing or creating any output directory. Dry runs do not prepare output directories. Also, ensure that the number of available collections meets or exceeds the required threshold; otherwise, an error will be displayed.

## Implementation Details

- **Source File Organization:**
  - **cmd/padlock/main.go:** The command-line interface entry point.
  - **cmd/padlock/entropy.go:** Optional file/device/pipe and HTTPS entropy sources, with validation and timeouts.
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
  - **pkg/pad/pad.go:** Core implementation of the one-time pad threshold scheme.
  - **pkg/pad/rng.go:** XORs five generators; optionally mixes external contributions into their seeds alongside OS randomness.
  - **pkg/trace/trace.go:** Context-based logging system for debug and trace information.

## License

MIT License
