package foldersnapshot

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

func containsRune(ranges [][2]rune, code rune) bool {
	low, high := 0, len(ranges)
	for low < high {
		mid := (low + high) / 2
		if code < ranges[mid][0] {
			high = mid
		} else if code > ranges[mid][1] {
			low = mid + 1
		} else {
			return true
		}
	}
	return false
}

// Normalize each scalar separately before counting decomposed combining marks.
// This rejects long runs before Go's stream-safe normalizer can insert a CGJ.
// Unicode normalization is stable for the pinned Unicode 15 assigned repertoire.
func supportedUnicode(name string) bool {
	if !utf8.ValidString(name) {
		return false
	}
	marks := 0
	for _, c := range name {
		if !containsRune(assigned15, c) {
			return false
		}
		for _, decomposed := range norm.NFD.String(string(c)) {
			if containsRune(marks15, decomposed) {
				marks++
			} else {
				marks = 0
			}
			if marks > 8 {
				return false
			}
		}
	}
	return true
}

// Called only after supportedUnicode. Never use runtime case tables.
func pathKey(name string) string {
	var key strings.Builder
	for _, c := range norm.NFC.String(name) {
		if folded, ok := folds15[c]; ok {
			key.WriteString(folded)
		} else {
			key.WriteRune(c)
		}
	}
	return norm.NFC.String(key.String())
}
