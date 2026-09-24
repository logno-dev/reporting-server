package renderer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestRenderWritesBothDataFileNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test renderer uses a shell script")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "common"), 0o750); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "typst")
	script := "#!/bin/sh\nset -eu\ncmp \"$3/data.json\" \"$3/report.json\"\ncp \"$3/report.json\" \"$5\"\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	input := json.RawMessage(`{"sample":{"id":"123"}}`)
	result, err := New(binary, root, time.Second).Render(context.Background(), "content", input)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if string(result) != string(input) {
		t.Errorf("Render() output = %q, want %q", result, input)
	}
}
