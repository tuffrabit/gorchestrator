package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FS is the built-in OS filesystem adapter.
type FS struct {
	root string
}

// NewFS creates a filesystem-backed StoragePort rooted at root.
func NewFS(root string) (*FS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve storage root: %w", err)
	}
	return &FS{root: abs}, nil
}

func (fs *FS) resolve(path string) (string, error) {
	// Treat the key as relative to the root. Leading slashes or ".." are
	// normalized by Clean so that the resolved path stays inside the root
	// unless an explicit escape is present.
	path = filepath.Clean("/" + path)
	if strings.HasPrefix(path, "..") {
		return "", fmt.Errorf("path escapes root: %s", path)
	}

	abs := filepath.Join(fs.root, path)

	// Resolve symlinks on the deepest existing ancestor, then re-check
	// containment. This prevents a symlink inside the root from pointing
	// outside the root.
	existing, err := fs.deepestExistingAncestor(abs)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	resolved := abs
	if _, err := os.Lstat(existing); err == nil {
		resolvedExisting, err := filepath.EvalSymlinks(existing)
		if err != nil {
			return "", fmt.Errorf("resolve symlinks: %w", err)
		}
		remaining, err := filepath.Rel(existing, abs)
		if err != nil {
			return "", fmt.Errorf("resolve remaining path: %w", err)
		}
		resolved = filepath.Join(resolvedExisting, remaining)
	}

	// Final separator-aware containment check.
	rel, err := filepath.Rel(fs.root, resolved)
	if err != nil {
		return "", fmt.Errorf("containment check: %w", err)
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path escapes root: %s", path)
	}

	return resolved, nil
}

// deepestExistingAncestor walks up from abs until it finds a path that exists
// or until it can go no further.
func (fs *FS) deepestExistingAncestor(abs string) (string, error) {
	for {
		_, err := os.Lstat(abs)
		if err == nil {
			return abs, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			// Reached the filesystem root; return it and let the caller decide.
			return abs, nil
		}
		abs = parent
	}
}

// Read implements Port.
func (fs *FS) Read(ctx context.Context, path string) ([]byte, error) {
	abs, err := fs.resolve(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// Write implements Port.
func (fs *FS) Write(ctx context.Context, path string, data []byte) error {
	abs, err := fs.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, data, 0o644)
}

// List implements Port.
func (fs *FS) List(ctx context.Context, path string) ([]DirEntry, error) {
	abs, err := fs.resolve(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  info.Size(),
		})
	}
	return out, nil
}

// Exists implements Port.
func (fs *FS) Exists(ctx context.Context, path string) (bool, error) {
	abs, err := fs.resolve(path)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(abs)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Mkdir implements Port.
func (fs *FS) Mkdir(ctx context.Context, path string) error {
	abs, err := fs.resolve(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, 0o755)
}

// RemoveAll implements Port.
func (fs *FS) RemoveAll(ctx context.Context, path string) error {
	if err := refuseRootRemove(path); err != nil {
		return err
	}
	abs, err := fs.resolve(path)
	if err != nil {
		return err
	}
	// Extra belt: never delete the storage root itself.
	if abs == fs.root {
		return fmt.Errorf("refusing to remove storage root")
	}
	if err := os.RemoveAll(abs); err != nil {
		return err
	}
	return nil
}

// Root returns the absolute storage root.
func (fs *FS) Root() string {
	return fs.root
}

// refuseRootRemove blocks empty / "." / "/" paths that would wipe the store.
func refuseRootRemove(path string) error {
	p := strings.TrimSpace(path)
	p = strings.Trim(p, "/")
	if p == "" || p == "." {
		return fmt.Errorf("refusing to remove storage root")
	}
	return nil
}
