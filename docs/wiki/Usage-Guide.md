# Padlock Usage Guide

This guide provides detailed instructions for using the Padlock utility to securely encode and decode data.

## Installation

Padlock requires Go 1.27.2 or later to build from source. Use a supported release with current security patches:

```bash
# Clone the repository
git clone https://github.com/rayozzie/padlock.git
cd padlock

# Build the binary
go build -o padlock ./cmd/padlock

# Optionally, move to a directory in your PATH
sudo mv padlock /usr/local/bin/
```

## Basic Usage

Padlock has two main commands:

1. `encode`: Split input data into N collections with K-of-N threshold security
2. `decode`: Reconstruct original data from K or more collections

### Encoding Data

To encode data, use the following command structure:

```bash
padlock encode <inputDir> <outputDir> [options]
```

#### Required Parameters

- `<inputDir>`: Directory containing the data to be archived and encoded
- `<outputDir>`: Destination directory for collection TAR files, or collection subdirectories with `-files`

#### Options

- `-copies N`: Number of collections to create (2–26; defaults to 2 with one output directory, or the directory count with multiple outputs). An explicit value must match the number of output directories when more than one is supplied. A mismatch fails before creating or clearing outputs, including with `-dryrun`.
- `-required K`: Required distinct collections, with 2 ≤ K ≤ N. Defaults to 2 with one output directory and all collections with multiple output directories. Explicit invalid values fail before outputs are created or cleared.
- `-format FORMAT`: Output format: bin or png (default: png)
- `-clear`: Clear output directory if not empty
- `-chunk SIZE`: Maximum payload bytes per collection chunk, before headers and container overhead (default: 2 MiB). The minimum is C(N-1,K-1), for example 6 bytes for 3-of-5.
- `-verbose`: Enable detailed debug output
- `-files`: Create individual files for each collection instead of TAR archives (default: creates TAR archives)
- `-dryrun`: Calculate and display size information without actually writing output files
- `-entropy-file PATH`: Additional raw seed bytes from a file/device/pipe; `-` reads stdin (repeatable, encode only)
- `-entropy-url URL`: Additional seed bytes from fresh HTTPS `application/octet-stream` responses (repeatable, encode only)
- `-entropy-timeout DURATION`: Time limit per external source open/read (default: `10s`, encode only)

#### Examples

Create 3 collections where any 2 can reconstruct the data, in PNG format:
```bash
padlock encode ~/Documents/secret ~/Collections -copies 3 -required 2 -format png
```

Create 5 collections where any 3 are required, using TAR archives (default behavior):
```bash
padlock encode ~/Documents/top-secret ~/Collections -copies 5 -required 3
```

Enable verbose logging for debugging:
```bash
padlock encode ~/Documents/confidential ~/Collections -copies 4 -required 2 -verbose
```

Run in dry-run mode to see size information without writing files:
```bash
padlock encode ~/Documents/confidential ~/Collections -copies 4 -required 2 -dryrun
```

#### Adding External Entropy

Use a fresh externally generated seed file, kept outside the input/output trees:

```bash
padlock encode ./input ./collections -entropy-file /media/entropy/fresh-seed.bin
```

Read raw entropy bytes from an external producer through stdin:

```bash
external-entropy-producer | padlock encode ./input ./collections -entropy-file -
```

Select an HTTPS source as well as a local source:

```bash
padlock encode ./input ./collections \
  -entropy-file /media/entropy/fresh-seed.bin \
  -entropy-url 'https://your-entropy-service.example/raw' \
  -entropy-timeout 10s
```

These are example source paths and a placeholder URL; provide your own trusted sources. Each source must supply 76 bytes initially and 44 more for every ChaCha20 reseed. The HTTPS endpoint must respond with fresh raw binary data, not a JSON or text API response. See the [complete source contract](../../README.md#optional-external-entropy).

Selected sources are never silently omitted. Initial source failures leave existing outputs intact, including with `-clear`. Dry runs do not open or consume external sources. Decode does not need them. Regular files start at their beginning on each invocation: use new contents for every encoding run. Choosing additional inputs does not automatically prove their independence or secrecy; see the [Security Model](Security-Model).

### Decoding Data

To decode data, use the following command structure:

```bash
padlock decode <inputDir> <outputDir> [options]
```

#### Required Parameters

- `<inputDir>`: Root directory containing the collection subdirectories or TAR files
- `<outputDir>`: Destination directory where the original data will be restored

#### Options

- `-clear`: Clear output directory if not empty
- `-verbose`: Enable detailed debug output
- `-dryrun`: Calculate and display size information without actually writing output files

#### Examples

Reconstruct the original data from collections:
```bash
padlock decode ~/Collections ~/Restored
```

Empty backups and backups containing only directories are supported, including with `-dryrun`.

Clear the output directory before decoding:
```bash
padlock decode ~/Collections/subset ~/Restored -clear
```

Decode reports `collections belong to different backups` when backup identifiers differ. Supply collections from the same encoding run. The first chunk headers are checked before the destination is created or cleared, and later chunks are checked as decoding proceeds. Extra supplied collections and dry runs are checked too.

Enable verbose logging for debugging:
```bash
padlock decode ~/Collections ~/Restored -verbose
```

Run in dry-run mode to reconstruct and validate the stream without writing restored files:
```bash
padlock decode ~/Collections ~/Restored -dryrun
```

## Advanced Usage

### Using Dry Run Mode

Dry runs process the full input stream without creating, clearing, or writing output files. They can take substantial time and still require chunk buffers and directory metadata.

- Encode streams TAR serialization and gzip, then counts the encoded frames. Input counts include TAR headers and padding. Collection counts exclude PNG and outer collection-TAR overhead, so the report is not an exact estimate of on-disk space.
- Decode reconstructs and validates the complete decoded archive, including its end and compressed-stream checks. The decompressed size includes directory-TAR framing. No restored files are written, and no collection archive is temporarily extracted.
- Decode dry runs detect the same backup mixing, duplicate conflicts, incomplete chunks, sparse entries, and unsupported entry types as normal streaming validation. They cannot certify authenticity or predict destination-specific failures such as insufficient disk space or conflicting existing files.
- External entropy options are checked for syntax in encode dry runs, but the sources are not opened or consumed. A dry run does not establish that they will be available for a real encode.

```bash
padlock encode ./input -copies 3 -required 2 -dryrun
padlock decode ./collections -dryrun
```

### Working with Large Datasets

When working with large datasets, consider the following tips:

Large copy counts with a threshold near half the copies can be impractical even though combination tables are not stored in memory. For example, 13-of-26 produces 5,200,300 payload bytes in each collection for every byte supplied to the splitting engine (after compression, if enabled). The encoder's payload buffers can total up to `copies × chunk size`, with additional memory for input, randomness, and file formatting; decoding also buffers the supplied collections' current chunks. Reducing `-chunk` limits buffering but does not reduce the total storage expansion.

1. **Adjust Chunk Size**: Use the `-chunk` option to control memory usage:
   ```bash
   padlock encode ~/LargeData ~/Collections -chunk 1048576  # 1MB chunks
   ```

2. **Use Binary Format**: For very large datasets, the binary format may be more efficient:
   ```bash
   padlock encode ~/LargeData ~/Collections -format bin
   ```

3. **Monitor Progress**: Use the `-verbose` flag to monitor progress during long operations:
   ```bash
   padlock encode ~/LargeData ~/Collections -verbose
   ```

### Collection Distribution Strategies

For maximum security, distribute collections across different storage locations:

1. **Physical Separation**: Store collections on different physical devices (USB drives, SD cards, etc.)
2. **Cloud Distribution**: Upload collections to different cloud storage providers
3. **Geographic Distribution**: Store collections in different physical locations
4. **Time-Based Distribution**: Transfer collections at different times to reduce correlation

### Handling TAR Collections

By default, Padlock creates TAR archives for each collection:

```bash
padlock encode ~/Documents/secret ~/Collections -copies 3 -required 2
```

This creates `2A3.tar`, `2B3.tar`, and `2C3.tar`. Keep the chosen collections on storage locations appropriate to your recovery and access requirements.

If you prefer working with individual files instead of TAR archives, use the `-files` option:

```bash
padlock encode ~/Documents/secret ~/Collections -copies 3 -required 2 -files
```

For decoding, Padlock automatically detects and handles both formats:

```bash
# Decoding from TAR archives
padlock decode ~/Collections ~/Restored

# Decoding from directories containing individual files
padlock decode ~/Collections ~/Restored
```

Discovery probes TAR headers without extraction, including for unrelated archives beside the collections. A renamed TAR can be identified from its chunk filenames. Loose chunks and regular TAR members are read by numeric chunk number, including numbers above 9,999, regardless of the members' stored order. Collection archives and loose chunk files must be regular files; named pipes, sockets, devices, and file symlinks are rejected. PNG files carry encoded bytes in a custom chunk: preserve the original files rather than resizing, optimizing, or resaving them as images.

## Multiple Storage Locations

Supply one output directory per collection to place them directly on separate destinations. Specify the threshold explicitly when fewer than all collections should suffice:

```bash
padlock encode ./input /media/one/backup /media/two/backup /media/three/backup -required 2
padlock decode /media/one/backup /media/three/backup ./restored
```

Output directories must be separate: duplicate paths, aliases, and parent/child nesting are rejected before any output is created or cleared. Different directory names do not establish independent devices or failure domains. With multiple decode inputs, each can be a collection directory or a parent containing collection directories/TARs. With one decode input, supply the parent containing collections. When more than one path is supplied, the last is always the output path, including with `-dryrun`; provide a placeholder output path for a multiple-input dry run.

## Recovery and Storage Practices

- Keep K distinct intact collections available for recovery, and keep fewer than K available to someone who should not recover the data. Higher K also reduces how many lost collections the backup can tolerate; it is not a substitute for source quality or protection of the host.
- Test recovery from the intended subset. A backup can be copied and restored repeatedly. Never reuse pad bytes to encode different data or restart generators from previously used state.
- Decode reads K/N and chunk parameters from the collections. Renaming a TAR does not hide threshold parameters or backup identifiers inside its headers. You can repack a loose collection with ordinary `tar -cf 2A3.tar 2A3`; regular TAR members are read in numeric chunk order regardless of their stored order. macOS AppleDouble `._` sidecars are ignored in directories and TARs.
- Preserve the collection label and initial decimal chunk number in each filename, and keep the original contents. The optional `IMG` prefix, collection letter, and BIN/PNG extension accept either case. Copy decorations after the number are supported, for example `IMG2A3_0001 (1).PNG` or `img2a3_0001 (conflicted copy).png`. Arbitrary prefixes such as `copy-IMG…` or changing the number are not supported. Other BIN/PNG names are ignored with a summary at normal logging level. Two files for the same chunk within a collection are retained for validation and fail as duplicate sequence entries; remove the extra copy rather than merging collections.
- Duplicate copies are accepted only if every supplied copy agrees. Conflicting, incomplete, or unreadable copies cause failure even when other collections would meet the threshold. First-chunk duplicates are compared before destination preparation; use `-dryrun` to check the complete backup before `-clear`. A successful dry run cannot protect against subsequent input changes or destination failures. Backup identifiers, PNG payload CRCs, and archive checks detect some damage; they do not authenticate the data.
- `-clear` removes existing output contents after applicable preflight checks. First headers and duplicate first-chunk payloads are checked before decode prepares its destination. A later read, storage, or validation failure can leave a cleared destination or partial restore; it does not roll back prior files.
- Newly created output roots and parent directories use `0700` on macOS/Linux, including direct library restores. Existing directories keep their modes. Restored children use archived permission bits where supported; Windows uses filesystem ACLs. See the [README operational boundaries](../../README.md#operational-boundaries).
- Preserve the original collection bytes. Normal operation is offline; optional entropy URLs are contacted only while encoding with those options. Decoding needs only the collections.
