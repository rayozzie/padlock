package padlock

// CompressionMode is an enum for either clear (no compression) or gzip.
type CompressionMode int

const (
	CompressionClear CompressionMode = iota
	CompressionGz
)

const DefaultCompressionMode = CompressionGz

// GenerateCombinations produces all k-element subsets of {0,...,n-1} using backtracking.
func GenerateCombinations(n, k int) [][]int {
	var result [][]int
	combo := make([]int, k)
	var backtrack func(start, depth int)
	backtrack = func(start, depth int) {
		if depth == k {
			cpy := make([]int, k)
			copy(cpy, combo)
			result = append(result, cpy)
			return
		}
		for i := start; i < n-(k-depth)+1; i++ {
			combo[depth] = i
			backtrack(i+1, depth+1)
		}
	}
	backtrack(0, 0)
	return result
}

// collectionLetter returns the collection letter for index i (0-indexed).
// For example, index 0 returns 'A', index 1 returns 'B', etc.
func collectionLetter(i int) rune {
	return rune('A' + i)
}
