package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"unicode/utf8"
)

const sessionNamespaceDomain = "cc-connect/session-manager/v1"

var errSessionNamespaceUnavailable = errors.New("stable persistent session namespace unavailable")

// SessionNamespace returns the opaque, versioned identity of a SessionManager.
// The persisted store path is canonicalized locally and is never returned or
// placed in a child-process environment. An in-memory manager has no stable
// identity and therefore fails closed.
func SessionNamespace(sessions *SessionManager) (string, error) {
	if sessions == nil || sessions.StorePath() == "" {
		return "", errSessionNamespaceUnavailable
	}
	if !utf8.ValidString(sessions.StorePath()) {
		return "", errSessionNamespaceUnavailable
	}

	storePath, err := filepath.Abs(filepath.Clean(sessions.StorePath()))
	if err != nil {
		return "", errSessionNamespaceUnavailable
	}
	storePath, err = filepath.EvalSymlinks(storePath)
	if err != nil {
		return "", errSessionNamespaceUnavailable
	}
	info, err := os.Stat(storePath)
	if err != nil || !info.Mode().IsRegular() {
		return "", errSessionNamespaceUnavailable
	}
	storePath = filepath.Clean(storePath)
	return namespaceFromCanonicalSessionStorePath(storePath)
}

func namespaceFromCanonicalSessionStorePath(storePath string) (string, error) {
	if !utf8.ValidString(storePath) {
		return "", errSessionNamespaceUnavailable
	}

	hash := sha256.New()
	_, _ = hash.Write([]byte(sessionNamespaceDomain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(storePath))
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ValidSessionNamespace reports whether namespace has the opaque wire form.
// Provenance is established by SessionNamespace; this check prevents malformed
// or inherited values from reaching the Codex launcher.
func ValidSessionNamespace(namespace string) bool {
	if len(namespace) != sha256.Size*2 {
		return false
	}
	for _, char := range namespace {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
