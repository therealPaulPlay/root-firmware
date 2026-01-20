package config

import "crypto/rand"

// generateRandomSuffix generates a random uppercase letter suffix of the specified length
func generateRandomSuffix(length int) string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "XXXX" // Fallback if random fails
	}
	for i := range b {
		b[i] = letters[b[i]%26]
	}
	return string(b)
}
