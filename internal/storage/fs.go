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
	path = filepath.Clean(path)
	if strings.HasPrefix(path, "..") || strings.Contains(path, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root: %s", path)
	}
	abs := filepath.Join(fs.root, path)
	// Double-check after clean/join.
	realAbs, err := filepath.Abs(abs)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if !strings.HasPrefix(realAbs, fs.root) && realAbs != fs.root {
		return "", fmt.Errorf("path escapes root: %s", path)
	}
	return realAbs, nil
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
			continue
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

// Root returns the absolute storage root.
func (fs *FS) Root() string {
	return fs.root
}
