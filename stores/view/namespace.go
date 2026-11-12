package view

import (
	"encoding/hex"
	"strings"
)

const namedPrefix = "view:"

// PhysicalID is the shared identity rule for named view writers and readers.
// Empty name selects the unnamed store. The view: prefix is reserved there.
// Hex preserves bytewise logical-ID ordering and encodes arbitrary delimiters.
func PhysicalID(name, id string) string {
	if name == "" {
		return id
	}
	return namedPrefix + hex.EncodeToString([]byte(name)) + ":" + hex.EncodeToString([]byte(id))
}
func (s *Store) logicalID(id string) (string, bool) {
	if s.Name == "" {
		return id, !strings.HasPrefix(id, namedPrefix)
	}
	prefix := PhysicalID(s.Name, "")
	if !strings.HasPrefix(id, prefix) {
		return "", false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(id, prefix))
	return string(b), err == nil && len(b) > 0 && PhysicalID(s.Name, string(b)) == id
}
func (s *Store) validID(id string) bool {
	return id != "" && (s.Name != "" || !strings.HasPrefix(id, namedPrefix))
}
