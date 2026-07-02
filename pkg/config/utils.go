package config

import (
	"crypto/rand"
	"strings"
)

// forbiddenSuffixWords are vulgar words that must not appear in generated suffixes,
// based on the 3-4 letter entries of the LDNOOBW list
var forbiddenSuffixWords = []string{
	"ANAL", "ANUS", "ASS", "BBW", "BDSM", "BOOB", "BUTT", "CLIT", "COCK",
	"COON", "CUM", "CUNT", "DICK", "DVDA", "FAG", "FUCK", "GURO", "JIZZ",
	"KIKE", "KKK", "MILF", "MONG", "NAZI", "NSFW", "NUDE", "ORGY", "PAKI",
	"POOF", "POON", "PORN", "PTHC", "QUIM", "RAPE", "SCAT", "SEX", "SHIT",
	"SLUT", "SMUT", "SPIC", "SUCK", "TIT", "TWAT", "WANK", "YAOI",
}

// generateRandomSuffix generates a random uppercase letter suffix of the specified length,
// re-rolling if it contains a vulgar word
func generateRandomSuffix(length int) string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, length)
attempt:
	for range 10 {
		if _, err := rand.Read(b); err != nil {
			break
		}
		for i := range b {
			b[i] = letters[b[i]%26]
		}
		s := string(b)
		for _, w := range forbiddenSuffixWords {
			if strings.Contains(s, w) {
				continue attempt
			}
		}
		return s
	}
	return strings.Repeat("X", length) // Fallback if random fails
}
