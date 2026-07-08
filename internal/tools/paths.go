package tools

import (
	"path/filepath"
	"strings"
)

// resolveAllowedPath checks that path is within one of the allowed prefixes.
// All inputs and the allowlist are treated as relative to the storage root.
func resolveAllowedPath(path string, allowlist []string) (string, bool) {
	path = filepath.Clean("/" + path)
	for _, prefix := range allowlist {
		prefix = filepath.Clean("/" + prefix)
		if prefix == "/" {
			return strings.TrimPrefix(path, "/"), true
		}
		if path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator)) {
			return strings.TrimPrefix(path, "/"), true
		}
	}
	return "", false
}
