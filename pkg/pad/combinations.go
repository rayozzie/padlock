// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import "fmt"

// combinationCount returns C(n, k). Callers must have validated n <= 26;
// every intermediate product then fits in a 32-bit int.
func combinationCount(n, k int) int {
	if k < 0 || k > n {
		return 0
	}
	if n-k < k {
		k = n - k
	}
	count := 1
	for i := 1; i <= k; i++ {
		count = count * (n - i + 1) / i
	}
	return count
}

// nextCombination advances a sorted combination of zero-based collection
// indexes in lexicographic order. Only the current K indexes are retained.
func nextCombination(combination []int, total int) bool {
	for i := len(combination) - 1; i >= 0; i-- {
		if combination[i] < total-len(combination)+i {
			combination[i]++
			for j := i + 1; j < len(combination); j++ {
				combination[j] = combination[j-1] + 1
			}
			return true
		}
	}
	return false
}

// collectionCombinationIndex locates a combination in one collection's
// lexicographically sorted payload without building or scanning the full list.
// Removing the common collection letter preserves this ordering, reducing the
// calculation to the rank of a (K-1)-combination among N-1 collections.
func collectionCombinationIndex(total int, combination, letter string) (int, error) {
	if total < 2 || total > 26 || len(combination) < 2 || len(combination) > total || len(letter) != 1 {
		return 0, fmt.Errorf("invalid combination %q for collection %q", combination, letter)
	}
	found := false
	for i := range combination {
		if combination[i] < 'A' || combination[i] >= 'A'+byte(total) || (i > 0 && combination[i] <= combination[i-1]) {
			return 0, fmt.Errorf("invalid combination %q: collection letters must be distinct and sorted", combination)
		}
		if combination[i] == letter[0] {
			found = true
		}
	}
	if !found {
		return 0, fmt.Errorf("collection %q is not in combination %q", letter, combination)
	}

	rank, next := 0, 0
	remaining := len(combination) - 1
	for i := range combination {
		if combination[i] == letter[0] {
			continue
		}
		index := int(combination[i] - 'A')
		if combination[i] > letter[0] {
			index--
		}
		for candidate := next; candidate < index; candidate++ {
			rank += combinationCount(total-2-candidate, remaining-1)
		}
		next = index + 1
		remaining--
	}
	return rank, nil
}
