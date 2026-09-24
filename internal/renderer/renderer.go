package renderer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type Renderer struct {
	binary  string
	root    string
	timeout time.Duration
}

func New(binary, root string, timeout time.Duration) *Renderer {
	return &Renderer{binary: binary, root: root, timeout: timeout}
}

func (r *Renderer) Render(ctx context.Context, source string, data json.RawMessage) ([]byte, error) {
	workspace, err := os.MkdirTemp("", "report-render-*")
	if err != nil {
		return nil, fmt.Errorf("create render workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	if err := os.CopyFS(filepath.Join(workspace, "common"), os.DirFS(filepath.Join(r.root, "common"))); err != nil {
		return nil, fmt.Errorf("copy shared Typst components: %w", err)
	}
	inputPath := filepath.Join(workspace, "main.typ")
	outputPath := filepath.Join(workspace, "report.pdf")
	if err := os.WriteFile(inputPath, []byte(source), 0o600); err != nil {
		return nil, fmt.Errorf("write Typst source: %w", err)
	}
	// Keep both conventional names available so templates can describe their
	// input as either generic data or a report without duplicating datasets.
	for _, name := range []string{"data.json", "report.json"} {
		if err := os.WriteFile(filepath.Join(workspace, name), data, 0o600); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
	}

	renderCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	command := exec.CommandContext(renderCtx, r.binary, "compile", "--root", workspace, inputPath, outputPath)
	output, err := command.CombinedOutput()
	if err != nil {
		if renderCtx.Err() != nil {
			return nil, fmt.Errorf("Typst render timed out: %w", renderCtx.Err())
		}
		return nil, fmt.Errorf("Typst render failed: %w: %s", err, output)
	}
	pdf, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("read rendered PDF: %w", err)
	}
	return pdf, nil
}
