package api

import "testing"

func TestTemplateAssetContentType(t *testing.T) {
	tests := []struct {
		name     string
		contents []byte
		want     string
		ok       bool
	}{
		{name: "png", contents: []byte("\x89PNG\r\n\x1a\n"), want: "image/png", ok: true},
		{name: "svg", contents: []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`), want: "image/svg+xml", ok: true},
		{name: "text", contents: []byte("not an image"), ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := templateAssetContentType(test.contents)
			if got != test.want || ok != test.ok {
				t.Fatalf("templateAssetContentType() = %q, %v, want %q, %v", got, ok, test.want, test.ok)
			}
		})
	}
}
