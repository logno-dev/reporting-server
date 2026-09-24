package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemPut(t *testing.T) {
	root := t.TempDir()
	store := NewFilesystem(root)
	contents := []byte("pdf contents")

	result, err := store.Put(context.Background(), "reports/2026/report.pdf", contents)
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if result.Key != "reports/2026/report.pdf" {
		t.Errorf("Key = %q", result.Key)
	}
	digest := sha256.Sum256(contents)
	if result.SHA256 != hex.EncodeToString(digest[:]) {
		t.Errorf("SHA256 = %q", result.SHA256)
	}
	written, err := os.ReadFile(filepath.Join(root, "reports", "2026", "report.pdf"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(written) != string(contents) {
		t.Errorf("written contents = %q", written)
	}
	loaded, err := store.Get(context.Background(), result.Key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if string(loaded) != string(contents) {
		t.Errorf("Get() contents = %q", loaded)
	}
}

func TestFilesystemPutRejectsTraversal(t *testing.T) {
	store := NewFilesystem(t.TempDir())

	for _, key := range []string{"..", "../outside.pdf"} {
		if _, err := store.Put(context.Background(), key, []byte("pdf")); err == nil {
			t.Errorf("Put() accepted path traversal key %q", key)
		}
	}
}
