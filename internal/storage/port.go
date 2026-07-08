package storage

import (
	"context"
)

// DirEntry represents a single entry returned by List.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// Port is the hexagonal StoragePort abstraction.
type Port interface {
	// Read returns the contents of a file.
	Read(ctx context.Context, path string) ([]byte, error)

	// Write creates or overwrites a file at path.
	Write(ctx context.Context, path string, data []byte) error

	// List returns directory entries at path.
	List(ctx context.Context, path string) ([]DirEntry, error)

	// Exists reports whether a file or directory exists.
	Exists(ctx context.Context, path string) (bool, error)

	// Mkdir creates a directory (and parents if needed).
	Mkdir(ctx context.Context, path string) error
}
