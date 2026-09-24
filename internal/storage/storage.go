package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Result struct {
	Key    string
	SHA256 string
}

type Storage interface {
	Put(ctx context.Context, key string, contents []byte) (Result, error)
	Get(ctx context.Context, key string) ([]byte, error)
}

type Filesystem struct {
	root string
}

func NewFilesystem(root string) *Filesystem {
	return &Filesystem{root: root}
}

func (f *Filesystem) Test(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(f.root)
	if err != nil {
		return fmt.Errorf("access filesystem storage root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("filesystem storage root is not a directory")
	}
	return nil
}

func (f *Filesystem) Put(ctx context.Context, key string, contents []byte) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	cleanPath, err := cleanKey(key)
	if err != nil {
		return Result{}, err
	}
	path := filepath.Join(f.root, cleanPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return Result{}, fmt.Errorf("create storage directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary artifact: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o640); err == nil {
		_, err = temporary.Write(contents)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Result{}, fmt.Errorf("write artifact: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return Result{}, fmt.Errorf("store artifact: %w", err)
	}
	digest := sha256.Sum256(contents)
	return Result{Key: filepath.ToSlash(cleanPath), SHA256: hex.EncodeToString(digest[:])}, nil
}

func (f *Filesystem) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cleanPath, err := cleanKey(key)
	if err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(filepath.Join(f.root, cleanPath))
	if err != nil {
		return nil, fmt.Errorf("read artifact %q: %w", key, err)
	}
	return contents, nil
}

func cleanKey(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(key))
	if clean == "." || clean == ".." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid storage key %q", key)
	}
	return clean, nil
}
