package pad

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rayozzie/padlock/pkg/file"
	"github.com/rayozzie/padlock/pkg/trace"
)

// DecodeDirect is an alternative implementation of the decoder that directly 
// accesses files rather than using streaming io.Reader interfaces
func (p *Pad) DecodeDirect(ctx context.Context, basePath string, collections []string, output io.Writer) error {
	log := trace.FromContext(ctx).WithPrefix("DECODE-DIRECT")
	
	// Verify that we have enough collections
	if len(collections) < p.RequiredCopies {
		return fmt.Errorf("insufficient collections: found %d but require %d", 
			len(collections), p.RequiredCopies)
	}
	
	// Only use the required number of collections
	usableCollections := collections[:p.RequiredCopies]
	log.Infof("Using %d collections for direct decoding", len(usableCollections))
	
	// First count how many chunk files exist for diagnostics
	totalChunkCount := 0
	maxChunkIndex := 0
	for _, collName := range usableCollections {
		chunkCount := 0
		// Check for PNG files
		for i := 1; i <= 100; i++ { // Practical limit of 100 chunks
			chunkPath := filepath.Join(basePath, collName, fmt.Sprintf("IMG%s_%04d.PNG", collName, i))
			if _, err := os.Stat(chunkPath); !os.IsNotExist(err) {
				chunkCount++
				if i > maxChunkIndex {
					maxChunkIndex = i
				}
			} else {
				// Try BIN format
				chunkPath = filepath.Join(basePath, collName, fmt.Sprintf("%s_%04d.bin", collName, i))
				if _, err := os.Stat(chunkPath); !os.IsNotExist(err) {
					chunkCount++
					if i > maxChunkIndex {
						maxChunkIndex = i
					}
				} else {
					// No more files in this sequence
					break
				}
			}
		}
		totalChunkCount += chunkCount
		log.Infof("Collection %s has %d chunks", collName, chunkCount)
	}
	
	log.Infof("Found a total of %d chunks across %d collections (max index: %d)", 
		totalChunkCount, len(usableCollections), maxChunkIndex)
	
	// Process chunks sequentially until we don't find any more
	for chunkIndex := 1; ; chunkIndex++ {
		log.Infof("Processing chunk %d", chunkIndex)
		
		// Store chunk data for each collection
		chunks := make([][]byte, len(usableCollections))
		eofsEncountered := 0
		eofCollections := make([]bool, len(usableCollections))
		
		// For each collection, try to load the chunk data directly
		for i, collName := range usableCollections {
			// Skip collections that have already reached EOF
			if eofCollections[i] {
				eofsEncountered++
				continue
			}
			
			// Determine the format (PNG or BIN) - default to PNG
			format := file.FormatPNG
			
			// Construct the path to the chunk file
			var chunkPath string
			if format == file.FormatPNG {
				chunkPath = filepath.Join(basePath, collName, fmt.Sprintf("IMG%s_%04d.PNG", collName, chunkIndex))
			} else {
				chunkPath = filepath.Join(basePath, collName, fmt.Sprintf("%s_%04d.bin", collName, chunkIndex))
			}
			
			// Check if the file exists
			if _, err := os.Stat(chunkPath); os.IsNotExist(err) {
				// If file doesn't exist, try the other format
				if format == file.FormatPNG {
					format = file.FormatBin
					chunkPath = filepath.Join(basePath, collName, fmt.Sprintf("%s_%04d.bin", collName, chunkIndex))
					if _, err := os.Stat(chunkPath); os.IsNotExist(err) {
						// Neither format exists, so this is EOF for this collection
						log.Debugf("No chunk file found for collection %s at index %d", collName, chunkIndex)
						eofCollections[i] = true
						eofsEncountered++
						continue
					}
				} else {
					// End of this collection
					log.Debugf("No chunk file found for collection %s at index %d", collName, chunkIndex)
					eofCollections[i] = true
					eofsEncountered++
					continue
				}
			}
			
			// Read the chunk data
			formatter := file.GetFormatter(format)
			chunkDir := filepath.Dir(chunkPath)
			chunkData, err := formatter.ReadChunk(ctx, chunkDir, 0, chunkIndex)
			
			if err != nil {
				if strings.Contains(err.Error(), "not found") || os.IsNotExist(err) {
					// End of collection
					log.Debugf("Error reading chunk %d for collection %s: %v", chunkIndex, collName, err)
					eofCollections[i] = true
					eofsEncountered++
					continue
				}
				return fmt.Errorf("error reading chunk %d from collection %s: %w", chunkIndex, collName, err)
			}
			
			// Process the chunk data - extract the payload (skip the header)
			if len(chunkData) > 0 {
				// First byte is the length of the header name
				headerLen := int(chunkData[0])
				if headerLen+1 <= len(chunkData) {
					// Extract the header name
					headerName := string(chunkData[1:headerLen+1])
					log.Debugf("Chunk %d, collection %s: header = %s", chunkIndex, collName, headerName)
					
					// Skip the header to get the actual chunk data
					if headerLen+1 < len(chunkData) {
						chunks[i] = chunkData[headerLen+1:]
					} else {
						chunks[i] = []byte{} // Empty chunk
					}
				} else {
					log.Debugf("Invalid header in chunk %d from collection %s", chunkIndex, collName)
					chunks[i] = []byte{} // Use empty data
				}
			} else {
				log.Debugf("Empty chunk data for chunk %d from collection %s", chunkIndex, collName)
				chunks[i] = []byte{} // Empty chunk
			}
			
			log.Debugf("Chunk %d: read %d bytes from collection %s", chunkIndex, len(chunks[i]), collName)
		}
		
		// If all collections have reached EOF, we're done
		if eofsEncountered == len(usableCollections) {
			log.Infof("All collections completed - processed %d chunks", chunkIndex-1)
			return nil
		}
		
		// Check if we have an uneven number of chunks
		if eofsEncountered > 0 && eofsEncountered < len(usableCollections) {
			// Some collections have ended but others haven't - this is an error
			eofStatus := ""
			for i, collName := range usableCollections {
				eofStatus += fmt.Sprintf(" %s: %v |", collName, eofCollections[i])
			}
			log.Error(fmt.Errorf("Uneven collection sizes at chunk %d: %s", chunkIndex, eofStatus))
			return fmt.Errorf("uneven collection sizes detected at chunk %d", chunkIndex)
		}
		
		// We have chunk data from all collections, decode it
		log.Debugf("Decoding chunk %d with %d collections", chunkIndex, len(chunks))
		decodedChunk, err := decodeChunks(ctx, chunks, p.RequiredCopies, p.TotalCopies)
		if err != nil {
			return fmt.Errorf("failed to decode chunk %d: %w", chunkIndex, err)
		}
		
		log.Debugf("Successfully decoded chunk %d (%d bytes)", chunkIndex, len(decodedChunk))
		
		// Write the decoded data to output
		_, err = output.Write(decodedChunk)
		if err != nil {
			return fmt.Errorf("failed to write decoded data for chunk %d: %w", chunkIndex, err)
		}
		
		log.Debugf("Chunk %d: wrote %d bytes to output", chunkIndex, len(decodedChunk))
	}
}