// Copyright 2025 Ray Ozzie. All rights reserved.

package pad

import "fmt"

func validateCopies(totalCopies, requiredCopies int) error {
	if totalCopies < 2 || totalCopies > 26 {
		return fmt.Errorf("totalCopies must be between 2 and 26, got %d", totalCopies)
	}
	if requiredCopies < 2 {
		return fmt.Errorf("requiredCopies must be at least 2, got %d", requiredCopies)
	}
	if requiredCopies > totalCopies {
		return fmt.Errorf("requiredCopies cannot be greater than totalCopies, got %d > %d", requiredCopies, totalCopies)
	}
	return nil
}

// ValidateChunkSize checks that a collection chunk can hold at least one input
// byte for the requested K-of-N configuration. The minimum is C(N-1, K-1) bytes.
// Callers can reject invalid settings before preparing output directories.
func ValidateChunkSize(totalCopies, requiredCopies, outputChunkBytes int) error {
	if err := validateCopies(totalCopies, requiredCopies); err != nil {
		return err
	}
	return validateChunkSize(totalCopies, requiredCopies, outputChunkBytes, collectionPermutationCount(totalCopies, requiredCopies))
}

// collectionPermutationCount requires validated copy counts.
func collectionPermutationCount(totalCopies, requiredCopies int) int {
	return combinationCount(totalCopies-1, requiredCopies-1)
}

func validateChunkSize(totalCopies, requiredCopies, outputChunkBytes, permutationCount int) error {
	if permutationCount < 1 {
		return fmt.Errorf("invalid pad: permutation count must be positive, got %d", permutationCount)
	}
	if outputChunkBytes < permutationCount {
		return fmt.Errorf("chunk size must be at least %d bytes for %d-of-%d encoding, got %d",
			permutationCount, requiredCopies, totalCopies, outputChunkBytes)
	}
	return nil
}
