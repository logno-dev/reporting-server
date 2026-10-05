package api

import (
	"encoding/json"
	"testing"
)

func TestParseTemplateSelection(t *testing.T) {
	tests := []struct {
		name     string
		raw      json.RawMessage
		supplied bool
		want     []string
		wantErr  bool
	}{
		{name: "omitted"},
		{name: "all", raw: json.RawMessage(`null`), supplied: true},
		{name: "none", raw: json.RawMessage(`[]`), supplied: true, want: []string{}},
		{name: "selected", raw: json.RawMessage(`["a","b"]`), supplied: true, want: []string{"a", "b"}},
		{name: "wrong type", raw: json.RawMessage(`"a"`), supplied: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, supplied, err := parseTemplateSelection(test.raw)
			if (err != nil) != test.wantErr || supplied != test.supplied || stringSliceValue(got) != stringSliceValue(test.want) {
				t.Fatalf("parseTemplateSelection() = %#v, %v, %v", got, supplied, err)
			}
		})
	}
}

func stringSliceValue(value []string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
