# Padlock: A One-Time-Pad K-of-N Data Encoding Utility

**Padlock** is a high-performance, single-pass encoding and decoding utility that implements a threshold one-time-pad scheme for secure data storage and transmission. It splits data into encrypted chunks so that only a minimum number of collections (or “shares”) are required to recover the original content. By relying solely on secure random number generation and XOR operations, Padlock achieves information-theoretic security while remaining straightforward and fully streamable.

## Key Features

- **Threshold Security:**  
  The data is split into N collections, where at least K collections (with 2 ≤ K ≤ N ≤ 26) are needed to reconstruct the original content. With fewer than K collections, no information is revealed.

- **Stream-Pipelined Processing:**  
  Both the encoding and decoding processes operate as fully streaming pipelines, processing the data chunk-by-chunk without needing to load the entire dataset into memory. This makes Padlock ideal for large-scale or real-time applications.

- **Candidate Record Mode (No Additional Cryptography):**  
  Instead of complex cryptographic algorithms, Padlock uses a candidate-record approach based solely on one-time-pad generation and XOR operations. For each input chunk:
  - A random one-time pad is generated and XORed with the plaintext to produce ciphertext.
  - Both the pad and the ciphertext are divided evenly into N segments.
  - For every K‑subset of the N collections, a candidate record is created as follows:
    - **Candidate ID:** A string of K letters representing the collections (for example, "ACE").
    - **Left Half:** Contains the ciphertext segments for collections in the candidate set and the pad segments for the others.
    - **Right Half:** Contains the pad segments for collections in the candidate set and the ciphertext segments for those not included.
  - When the appropriate candidate record is used, XORing its left and right halves recovers the original plaintext.

- **Flexible Output Formats:**  
  Candidate records are stored as individual files in one of two formats:
  - **PNG Files:** Files are named using the pattern  
    `IMG<collectionID>_<chunkNumber>.PNG`  
    (for example, if the collection directory is “3C5”, the first candidate chunk file is named `IMG3C5_00001.PNG`).
  - **Raw Binary Files (.bin):** Files are named with the format  
    `<collectionID>_<chunkNumber>.bin`

- **User-Friendly Messaging and Error Handling:**  
  Messages intended for users (such as summaries and error notifications) are always displayed. Detailed trace and debug messages, which are prefixed with "ENCODE:" or "DECODE:", appear only when the `-verbose` flag is set.

## How It Works

### Overview

1. **Encoding Process:**
   - **Archive & Compress:**  
     The input directory is archived using tar and optionally compressed using gzip.
   - **Chunking:**  
     The compressed stream is divided into chunks. The user-specified chunk size defines the total size allocated for the candidate records (the “candidate block”) within each chunk.
   - **Encryption via One-Time Pad:**  
     For each chunk, a new random pad is generated and XORed with the plaintext chunk to generate the ciphertext.
   - **Segmenting Data:**  
     Both the pad and ciphertext are split into N equally sized segments.
   - **Candidate Record Generation:**  
     For every K‑subset of the N collections, a candidate record is built as follows:
     - The record begins with a candidate ID (a string of K letters).
     - It then comprises two halves (left and right), constructed from the pad and ciphertext segments as described above.
     - A candidate block, starting with a record count, is built from all candidate records and is written to each collection’s directory.
   - **Keychain Metadata:**  
     A keychain record (designated as chunk 0) is written to every collection. This record contains metadata (number of copies, required collections, mode, and total input data size) using user-friendly terms.

2. **Decoding Process:**
   - **Keychain and Collection Discovery:**  
     The keychain is read from one collection to extract important parameters. The available collection directories (each containing an explicit collection letter in its name) are then identified.
   - **Candidate Record Selection:**  
     The tool determines which candidate record to use based on the available collection letters. If fewer than the required number of collections are present, an error is reported.
   - **Data Reconstruction:**  
     For each chunk, the appropriate candidate record is located. Its left and right halves are XORed to recover the original plaintext.
   - **Extraction:**  
     The reassembled data is decompressed (if needed) and untarred to rebuild the original directory structure and files.

## Security

- **Perfect Secrecy:**  
  As long as a new one-time pad is generated securely for each chunk and is never reused, the encryption provides information-theoretic (perfect) secrecy.

- **Threshold Assurance:**  
  The design guarantees that without access to at least the required number of collections, no useful information about the original data is revealed.

## Installation and Usage

### Requirements

- Go (version 1.23 or later is recommended)
- A standard Go build environment

### Building Padlock

To build the utility, run the following command in your terminal. (Simply copy and paste the command as-is.)

```bash
go build -o padlock cmd/padlock/main.go
```

### Command-Line Usage

- **Encode:**

  padlock encode <inputDir> <outputDir> -copies 5 -required 3 -format png -chunk 2097152 [-clear] [-verbose]

  - `<inputDir>`: Directory containing the data to be archived and encoded.
  - `<outputDir>`: Destination directory for the generated collection subdirectories.
  - `-copies`: Number of collections to create (must be between 2 and 26).
  - `-required`: Minimum number of collections required for reconstruction.
  - `-format`: Output format, either "bin" or "png".
  - `-chunk`: Candidate block size in bytes (total size allocated for candidate records in one chunk).
  - `-clear`: (Optional) Clears the output directory before encoding.
  - `-verbose`: (Optional) Enables detailed trace/debug messages (prefixed with "ENCODE:").

- **Decode:**

  padlock decode <inputDir> <outputDir> [-clear] [-verbose]

  - `<inputDir>`: Root directory containing the collection subdirectories.
  - `<outputDir>`: Destination directory where the original data will be restored.
  - `-clear`: (Optional) Clears the output directory before decoding.
  - `-verbose`: (Optional) Enables detailed trace/debug messages (prefixed with "DECODE:").

**Important:**  
Do not place the output directory within the input directory to avoid recursive processing. Also, ensure that the number of available collections meets or exceeds the required threshold; otherwise, an error will be displayed.

## Implementation Details

- **Source File Organization:**
  - **cmd/padlock/main.go:** The command-line interface entry point.
  - **encode.go:** Implements tar archiving, optional gzip compression, stream chunking, one-time pad encryption, candidate record generation, and file output.
  - **decode.go:** Reassembles candidate records, recovers encrypted data via XOR, decompresses (if necessary), and untars the original data.
  - **format.go:** Contains implementations for writing candidate blocks in both raw binary and PNG formats.
  - **rng.go:** Provides secure random number generation by combining crypto/rand with math/rand.
  - **common.go:** Includes utility functions such as generating K-element combinations and mapping collection indices to letters.

## Disclaimer

Padlock is a demonstration of a secure, threshold-based method for splitting and encrypting data using a one-time pad and XOR operations without relying on additional cryptographic algorithms. Users must ensure that one-time pads are never reused and that configuration parameters are correctly set to achieve the intended level of security.

## License

MIT License
