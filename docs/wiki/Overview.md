# Padlock Overview

## Introduction

Padlock is a high-performance K-of-N threshold data encoding and decoding utility that implements a one-time-pad scheme for secure data archiving and border-crossings. It was created by Ray Ozzie.

## Key Features

- **Threshold Security:**\
  The data is split into N collections, where at least K collections (with 2 ≤ K ≤ N ≤ 26) are needed to reconstruct the original content. Confidentiality with fewer collections depends on the pad randomness; see the [Security Model](Security-Model).

- **Stream-Pipelined Processing:**\
  Encoding, decoding, and dry runs process file contents chunk by chunk. Memory depends on the chunk size and number of collections, plus directory metadata, format buffers, and runtime overhead. Large thresholds still increase storage and processing costs.

- **XOR Threshold Splitting:**
  Padlock uses random pads and XOR. The ideal scheme provides perfect secrecy with uniform, independent, secret pads that are never reused. Its default software RNG does not establish these information-theoretic assumptions.

- **Flexible Output Formats:**\
  Data chunks are stored as individual files in one of two formats:
  - **PNG Files:** Encoded bytes in a custom PNG chunk, with CRC checks for accidental damage
  - **Raw Binary Files (.bin):** Efficient for direct data storage

- **Comprehensive Serialization:**\
  Padlock can process entire directories, automatically serializing and optionally compressing the content before encoding.

- **TAR Collection Support:**
  Collections are streamed directly into TAR archives by default. Use `-files` for loose BIN/PNG chunks. Discovery and reading do not require temporary extraction.

## Use Cases

Padlock is particularly well-suited for the following use cases:

### 1. Secure Data Archiving

Distributing collections across storage locations can provide redundancy and limit access to a complete recovery set. Recovery requires K distinct intact collections from the same backup. Confidentiality depends on the host and pad randomness described in the [Security Model](Security-Model).

### 2. Border Crossing Security

When traveling internationally with sensitive data, Padlock allows you to distribute the collections across different devices or cloud storage services, so that a device or location can hold fewer than the K collections needed for recovery. The encoding host and random generator must also be trusted.

### 3. Long-Term Confidentiality

The ideal threshold scheme is independent of an attacker's computing power only under its ideal randomness assumptions. The default RNG shares an OS randomness source and does not establish unconditional resistance to future classical or quantum attacks.

### 4. Distributed Backup Systems

Organizations can implement secure backup strategies where different departments or locations hold different collections, requiring collaboration for data recovery.

## Design Properties

The splitting design and the default randomness implementation have different assumptions:

1. **Simple Splitting Operation:** The data is split using random pads, XOR, and combinatorial distribution. Practical confidentiality also depends on the OS and software generators supplying the pads.

2. **Explicit Randomness Assumptions:** The default combines five generator algorithms with separate state and seed reads, all sharing one OS randomness source. This provides algorithm diversity without establishing independent entropy sources or perfect secrecy.

3. **Threshold Recovery:** K distinct collections are required for recovery. Additional matching copies are accepted, but conflicting or incomplete copies cause a failure.

4. **Recovery Material:** There is no separate decryption key to supply. The collections themselves contain the pieces needed for recovery and must be protected accordingly. An intact backup may be restored repeatedly; pad bytes must never be reused to encode different data.

5. **Streaming Operation:** Padlock processes data in chunks, with memory use depending on chunk size and collection count. Large thresholds can still make storage and processing costs impractical.
