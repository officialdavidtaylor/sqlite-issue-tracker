// Package identity generates locally unique identifiers without mutable state.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// New returns a cryptographically random identifier with the supplied prefix.
func New(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	return prefix + strings.ToLower(hex.EncodeToString(raw[:])), nil
}

// NewIssueID returns a short, user-facing issue identifier.
func NewIssueID() (string, error) {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate issue identifier: %w", err)
	}
	return "ISS-" + strings.ToUpper(hex.EncodeToString(raw[:])), nil
}
