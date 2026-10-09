// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"sort"
	"testing"
)

// Reference the original layout independently of the production iterator and
// rank calculation: collect combinations recursively, then sort the strings.
// Use only small lists in tests.
func referenceCombinationLayout(total, required int) ([]string, map[byte][]string) {
	var combinations []string
	var collect func(int, string)
	collect = func(start int, prefix string) {
		if len(prefix) == required {
			combinations = append(combinations, prefix)
			return
		}
		for i := start; i <= total-(required-len(prefix)); i++ {
			collect(i+1, prefix+string(rune('A'+i)))
		}
	}
	collect(0, "")
	sort.Strings(combinations)
	byCollection := make(map[byte][]string)
	for _, combination := range combinations {
		for i := range combination {
			letter := combination[i]
			byCollection[letter] = append(byCollection[letter], combination)
		}
	}
	return combinations, byCollection
}

func TestCombinationCounts(t *testing.T) {
	for n := 0; n <= 26; n++ {
		for k := -1; k <= n+1; k++ {
			var want int64
			if k >= 0 && k <= n {
				want = new(big.Int).Binomial(int64(n), int64(k)).Int64()
			}
			if got := combinationCount(n, k); int64(got) != want {
				t.Fatalf("C(%d,%d) = %d, want %d", n, k, got, want)
			}
		}
	}
}

func TestCombinationLayoutMatchesReference(t *testing.T) {
	var cases [][2]int
	for total := 2; total <= 9; total++ {
		for required := 2; required <= total; required++ {
			cases = append(cases, [2]int{total, required})
		}
	}
	cases = append(cases, [2]int{26, 2}, [2]int{26, 25}, [2]int{26, 26})
	for _, tc := range cases {
		total, required := tc[0], tc[1]
		t.Run(fmt.Sprintf("%d_of_%d", required, total), func(t *testing.T) {
			combinations, byCollection := referenceCombinationLayout(total, required)
			indexes := make([]int, required)
			for i := range indexes {
				indexes[i] = i
			}
			for index, want := range combinations {
				letters := make([]byte, required)
				for i, collection := range indexes {
					letters[i] = 'A' + byte(collection)
				}
				if string(letters) != want {
					t.Fatalf("combination %d = %q, want %q", index, letters, want)
				}
				if more := nextCombination(indexes, total); more != (index < len(combinations)-1) {
					t.Fatalf("incorrect end of combinations at index %d", index)
				}
			}
			for letter, list := range byCollection {
				if len(list) != collectionPermutationCount(total, required) {
					t.Fatalf("wrong piece count for collection %c", letter)
				}
				for want, combination := range list {
					got, err := collectionCombinationIndex(total, combination, string(letter))
					if err != nil || got != want {
						t.Fatalf("%c/%s: index %d, error %v; want %d", letter, combination, got, err, want)
					}
				}
			}
		})
	}

	// Check both ends of the largest layout without building its ten million
	// combinations. The last combination is last in every participating list.
	for _, tc := range []struct {
		combination string
		index       int
	}{
		{"ABCDEFGHIJKLM", 0},
		{"NOPQRSTUVWXYZ", 5200299},
	} {
		for i := range tc.combination {
			letter := tc.combination[i : i+1]
			if got, err := collectionCombinationIndex(26, tc.combination, letter); err != nil || got != tc.index {
				t.Fatalf("13-of-26 %s/%s: %d, %v; want %d", letter, tc.combination, got, err, tc.index)
			}
		}
	}
}

func TestCombinationIndexRejectsInvalidSelection(t *testing.T) {
	for _, tc := range []struct {
		total               int
		combination, letter string
	}{
		{1, "AB", "A"}, {27, "AB", "A"}, {3, "A", "A"}, {3, "ABCD", "A"},
		{3, "AAC", "A"}, {3, "CBA", "B"}, {3, "ABZ", "A"}, {3, "ABc", "A"},
		{3, "AB", "C"}, {3, "ABC", ""}, {3, "ABC", "AB"},
	} {
		if _, err := collectionCombinationIndex(tc.total, tc.combination, tc.letter); err == nil {
			t.Fatalf("accepted invalid selection: %+v", tc)
		}
	}
}

// Every eight bytes uniquely identifies one segment requested from this test
// source, allowing checks for reused or omitted pad ranges across combinations
// and chunk boundaries. This is not a randomness provider for real backups.
type numberedPadSource struct{ blocks uint64 }

func (s *numberedPadSource) Name() string { return "numbered test pads" }
func (s *numberedPadSource) Read(_ context.Context, p []byte) error {
	if len(p)%8 != 0 {
		return fmt.Errorf("test pad size must be divisible by eight")
	}
	for offset := 0; offset < len(p); offset += 8 {
		s.blocks++
		binary.LittleEndian.PutUint64(p[offset:offset+8], s.blocks)
	}
	return nil
}

func TestEncodePreservesLayoutAndDistinctPads(t *testing.T) {
	ctx := context.Background()
	// One full sixteen-byte chunk and a shorter final chunk of eight bytes.
	want := make([]byte, 24)
	for i := range want {
		want[i] = byte(i*37 + 5)
	}
	for _, tc := range [][2]int{{2, 2}, {3, 2}, {5, 3}, {8, 4}, {26, 2}, {26, 25}, {26, 26}} {
		total, required := tc[0], tc[1]
		t.Run(fmt.Sprintf("%d_of_%d", required, total), func(t *testing.T) {
			combinations, byCollection := referenceCombinationLayout(total, required)
			encoder, err := NewPadForEncode(ctx, total, required)
			if err != nil {
				t.Fatal(err)
			}
			frames := make(map[string][]*bytes.Buffer)
			source := new(numberedPadSource)
			err = encoder.Encode(ctx, 16*len(byCollection['A']), bytes.NewReader(want), source,
				func(name string, _ int, _ string) (io.WriteCloser, error) {
					frame := new(bytes.Buffer)
					frames[name] = append(frames[name], frame)
					return &nopCloser{frame}, nil
				}, "bin")
			if err != nil {
				t.Fatal(err)
			}
			seen := make(map[uint64]bool)
			for chunk, size := range []int{16, 8} {
				// Decode every subset using the old sorted-list layout, without
				// calling the production decoder or its rank calculation.
				for _, combination := range combinations {
					restored := make([]byte, size)
					for member := range combination {
						letter := combination[member]
						name := fmt.Sprintf("%d%c%d", required, letter, total)
						if len(frames[name]) != 2 {
							t.Fatalf("%s has %d chunks, want 2", name, len(frames[name]))
						}
						frame := frames[name][chunk].Bytes()
						payload := frame[1+int(frame[0]):]
						if len(payload) != size*len(byCollection[letter]) {
							t.Fatalf("%s has wrong payload size", name)
						}
						index := sort.SearchStrings(byCollection[letter], combination)
						piece := payload[index*size : (index+1)*size]
						for i := range restored {
							restored[i] ^= piece[i]
						}
						if member > 0 {
							for offset := 0; offset < len(piece); offset += 8 {
								block := binary.LittleEndian.Uint64(piece[offset : offset+8])
								if block == 0 || block > source.blocks || seen[block] {
									t.Fatalf("pad block %d was reused, uninitialized, or changed", block)
								}
								seen[block] = true
							}
						}
					}
					if !bytes.Equal(restored, want[chunk*16:chunk*16+size]) {
						t.Fatalf("old layout cannot restore chunk %d from %s", chunk+1, combination)
					}
				}
			}
			if expected := len(want) / 8 * (required - 1) * len(combinations); len(seen) != expected || uint64(expected) != source.blocks {
				t.Fatalf("requested %d random blocks and found %d distinct pads; want %d", source.blocks, len(seen), expected)
			}
		})
	}
}

func TestDecodeReferenceCombinationLayout(t *testing.T) {
	ctx := context.Background()
	want := []byte("old-format payload spanning two chunks")
	for _, tc := range [][2]int{{2, 2}, {3, 2}, {5, 3}, {8, 4}, {26, 2}, {26, 25}, {26, 26}} {
		total, required := tc[0], tc[1]
		t.Run(fmt.Sprintf("%d_of_%d", required, total), func(t *testing.T) {
			combinations, byCollection := referenceCombinationLayout(total, required)
			streams := make(map[byte]*bytes.Buffer)
			for letter := range byCollection {
				streams[letter] = new(bytes.Buffer)
			}
			random := rand.New(rand.NewSource(0))
			// Build legacy three-field frames using the original combination
			// layout, independently of the production encoder and its iterator.
			for offset, chunk := 0, 1; offset < len(want); offset, chunk = offset+23, chunk+1 {
				data := want[offset:min(offset+23, len(want))]
				pieces := make(map[string][][]byte)
				for _, combination := range combinations {
					cipher := make([][]byte, required)
					cipher[0] = bytes.Clone(data)
					for member := 1; member < required; member++ {
						cipher[member] = make([]byte, len(data))
						random.Read(cipher[member])
						for i := range data {
							cipher[0][i] ^= cipher[member][i]
						}
					}
					pieces[combination] = cipher
				}
				for letter, list := range byCollection {
					header := fmt.Sprintf("%d%c%d:%d:%d", required, letter, total, chunk, len(data))
					streams[letter].WriteByte(byte(len(header)))
					streams[letter].WriteString(header)
					for _, combination := range list {
						for member := range combination {
							if combination[member] == letter {
								streams[letter].Write(pieces[combination][member])
							}
						}
					}
				}
			}
			for _, selected := range combinations {
				var readers []io.Reader
				// Reverse input order to also exercise pairing labels and payloads.
				for i := len(selected) - 1; i >= 0; i-- {
					readers = append(readers, bytes.NewReader(streams[selected[i]].Bytes()))
				}
				decoder, err := NewPadForDecode(ctx, required)
				if err != nil {
					t.Fatal(err)
				}
				var restored bytes.Buffer
				if err := decoder.Decode(ctx, readers, &restored); err != nil || !bytes.Equal(restored.Bytes(), want) {
					t.Fatalf("legacy combination %s failed to restore: %v", selected, err)
				}
			}
		})
	}
}
