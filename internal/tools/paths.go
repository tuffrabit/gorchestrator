package tools

import (
	"path"
	"strings"

	"github.com/tuffrabit/gorchestrator/internal/storage"
)

// resolveAllowedPath resolves an agent-provided path to a storage-relative key
// that falls under the allowlist.
//
// Agents may pass:
//   - a full storage key under an allowlist prefix
//     (e.g. projects/1/issues/1/source/main.go)
//   - a path relative to preferredBase (typically the issue directory, or the
//     implementer workspace)
//   - empty, ".", or a lone slash meaning preferredBase when set
//
// Short names that match an allowlist entry's final segment (e.g. "source")
// resolve to that entry before preferredBase joins, so implementers listing
// "source" hit the snapshot rather than workspace/source.
//
// Paths that already look like storage keys (projects/…) are never re-joined
// under a base — they either match the allowlist as-is or are rejected.
//
// Path traversal ("..") is rejected. Absolute host paths are not supported.
func resolveAllowedPath(agentPath string, allowlist []string, preferredBase string) (string, bool) {
	agentPath = strings.TrimSpace(agentPath)
	agentPath = strings.ReplaceAll(agentPath, "\\", "/")
	agentPath = strings.TrimPrefix(agentPath, "/")

	var candidates []string
	add := func(c string) {
		c = path.Clean(c)
		if c == "." {
			c = ""
		}
		for _, existing := range candidates {
			if existing == c {
				return
			}
		}
		candidates = append(candidates, c)
	}

	if agentPath == "" || agentPath == "." {
		if preferredBase != "" {
			add(preferredBase)
		} else {
			// Unscoped root (tests use allowlist {""}).
			add("")
		}
	} else {
		if err := storage.ValidateRelativePath(agentPath); err != nil {
			return "", false
		}
		clean := path.Clean(agentPath)
		// 1. Path as a full storage key (or any relative form).
		add(clean)

		// Full storage keys are never re-rooted under preferredBase/allowlist
		// joins — that would turn projects/9/... into
		// projects/2/issues/2/projects/9/... and incorrectly pass the allowlist.
		if !isStorageKey(clean) {
			// 2. Single-segment alias for an allowlist entry (e.g. "source").
			if !strings.Contains(clean, "/") {
				for _, a := range allowlist {
					a = strings.TrimSpace(a)
					if a == "" || a == "." {
						continue
					}
					if path.Base(path.Clean(a)) == clean {
						add(a)
					}
				}
			}

			// 3. preferredBase + path (issue root or workspace).
			if preferredBase != "" {
				add(path.Join(preferredBase, clean))
			}

			// 4. Each allowlist prefix + path (covers main.go after workspace base,
			// or reading under source when base is the issue root).
			for _, a := range allowlist {
				a = strings.TrimSpace(a)
				if a == "" || a == "." || a == preferredBase {
					continue
				}
				add(path.Join(a, clean))
			}
		}
	}

	for _, c := range candidates {
		if resolved, ok := matchAllowlist(c, allowlist); ok {
			return resolved, true
		}
	}
	return "", false
}

// isStorageKey reports whether p already looks like an orchestrator storage key
// rooted at projects/… (or repos/…).
func isStorageKey(p string) bool {
	return strings.HasPrefix(p, "projects/") || strings.HasPrefix(p, "repos/")
}

// matchAllowlist reports whether p is under any allowlist prefix.
// An empty allowlist entry (or ".") means the whole storage root is allowed.
func matchAllowlist(p string, allowlist []string) (string, bool) {
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "." {
		p = ""
	}
	for _, prefix := range allowlist {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" || prefix == "." {
			return p, true
		}
		prefix = strings.TrimPrefix(path.Clean("/"+prefix), "/")
		if p == prefix || (prefix != "" && strings.HasPrefix(p, prefix+"/")) {
			return p, true
		}
	}
	return "", false
}
