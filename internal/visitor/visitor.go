// Package visitor invents the identifier the service counts requests by: one
// for the session, held in memory, written only into the jars the extractor
// and the player read and removed with them. ADR-018 has the reasoning.
package visitor

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const FileName = "cookies.txt"

// Perm is the jar's mode: not a secret, but the one file bivy writes that
// another local user could recognise a session by (ADR-001).
const Perm fs.FileMode = 0o600

// session is who bivy is for as long as this process runs; never persisted.
var session = sync.OnceValues(identifier)

// Write puts a jar holding the session's visitor identifier in dir, replacing
// whatever was there. Replacing is the point: a jar left alone accumulates
// what the service writes into it, which is a durable identity (ADR-018).
func Write(dir string) (string, error) {
	id, err := session()
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
