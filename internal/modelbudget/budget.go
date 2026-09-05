// Package modelbudget applies the conservative token estimate used at model
// request boundaries.
package modelbudget

import (
	"errors"
)

const DefaultSafetyTokens = 1_024

func EstimateTokens(encoded []byte) int {
	if len(encoded) == 0 {
		return 0
	}
	return (len(encoded) + 3) / 4
}

func Fits(encoded []byte, contextWindow, maxOutput, safety int) bool {
	return contextWindow > 0 && maxOutput > 0 && safety > 0 &&
		EstimateTokens(encoded)+maxOutput+safety <= contextWindow
}

// TruncateResult returns the longest UTF-8 JSON prefix that the caller can
// carry inside an explicit truncation envelope.
func TruncateResult(encoded []byte, fits func(map[string]any) bool) (map[string]any, error) {
	result := func(prefix int) map[string]any {
		return map[string]any{
			"content_prefix": string(encoded[:prefix]),
			"original_bytes": len(encoded),
			"truncated":      true,
		}
	}
	if !fits(result(0)) {
		return nil, errors.New("model context cannot fit a truncated tool result")
	}
	boundaries := []int{0}
	for index := range string(encoded) {
		if index != 0 {
			boundaries = append(boundaries, index)
		}
	}
	boundaries = append(boundaries, len(encoded))
	low, high := 0, len(boundaries)-1
	for low < high {
		middle := low + (high-low+1)/2
		if fits(result(boundaries[middle])) {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return result(boundaries[low]), nil
}
