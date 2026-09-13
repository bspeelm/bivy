// Package visitor invents the identifier the service counts requests by, one
// per request and thrown away after it. A value kept between requests would be
// the durable identifier this exists to avoid, and what the service writes back
// beside it is discarded too. ADR-016 has the evidence and the cost.
package visitor

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the jar's name wherever it is written.
const FileName = "cookies.txt"

// Perm is the jar's mode: not a secret, but the one file bivy writes that
// another local user could recognise a session by (ADR-001).
const Perm fs.FileMode = 0o600

// Write puts a jar holding one invented visitor identifier in dir, replacing
// whatever was there, and returns its path.
func Write(dir string) (string, error) {
	id, err := identifier()
	if err != nil {
		return "", err
	}

	path := filepath.Join(dir, FileName)
	jar := "# Netscape HTTP Cookie File\n" +
		strings.Join([]string{".youtube.com", "TRUE", "/", "TRUE", "0", "VISITOR_INFO1_LIVE", id}, "\t") +
		"\n"
	if err := os.WriteFile(path, []byte(jar), Perm); err != nil {
		return "", fmt.Errorf("writing the visitor cookie: %w", err)
	}
	return path, nil
}

// identifier is eleven characters of randomness, the shape the service issues.
// From crypto/rand, so it encodes nothing about the machine it came from.
func identifier() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("inventing a visitor identifier: %w", err)
	}
	return strings.TrimRight(base64.URLEncoding.EncodeToString(raw), "=")[:11], nil
}
