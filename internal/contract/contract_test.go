package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnalyzeDirectAliasesAndArrays(t *testing.T) {
	source := `#let report = json("report.json")
#let sample = report.coa.sample
#sample.sampleId
#for result in report.coa.results { [#result.name: #result.value] }`
	result, err := Analyze(source, json.RawMessage(`{"coa":{"sample":{"sampleId":"kept"},"results":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var sample map[string]any
	if err := json.Unmarshal(result.SampleData, &sample); err != nil {
		t.Fatal(err)
	}
	coa := sample["coa"].(map[string]any)
	if coa["sample"].(map[string]any)["sampleId"] != "kept" {
		t.Fatal("existing scalar was overwritten")
	}
	item := coa["results"].([]any)[0].(map[string]any)
	if item["name"] != "" || item["value"] != "" {
		t.Fatalf("missing iterable fields: %#v", item)
	}
	if len(result.SchemaHash) != 64 || len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	errors, err := Validate(result.DataSchema, json.RawMessage(`{"coa":{"sample":{"sampleId":"x"},"results":[{"name":"n","value":"v"}]}}`))
	if err != nil || len(errors) != 0 {
		t.Fatalf("validation failed: %v %#v", err, errors)
	}
}

func TestAnalyzeIgnoresCommentsAndStrings(t *testing.T) {
	source := `#let data = json("data.json")
// data.fake.path
/* data.other.path */
#let text = "data.string.path"
#data.real.value`
	result, err := Analyze(source, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.SampleData), "fake") || strings.Contains(string(result.SampleData), "other") || strings.Contains(string(result.SampleData), "string") {
		t.Fatalf("false path: %s", result.SampleData)
	}
	if !strings.Contains(string(result.SampleData), `"real":{"value":""}`) {
		t.Fatalf("missing direct path: %s", result.SampleData)
	}
}

func TestOptionalAtAndDynamicDiagnostic(t *testing.T) {
	result, err := Analyze(`#let report = json("report.json")
#report.at("subtitle", default: "")
#report.at(field)`, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Severity != "error" {
		t.Fatalf("expected blocking diagnostic: %#v", result.Diagnostics)
	}
	var schema map[string]any
	if err := json.Unmarshal(result.DataSchema, &schema); err != nil {
		t.Fatal(err)
	}
	required, _ := schema["required"].([]any)
	for _, field := range required {
		if field == "subtitle" {
			t.Fatal("defaulted .at field must be optional")
		}
	}
	errors, err := Validate(result.DataSchema, json.RawMessage(`{}`))
	if err != nil || len(errors) != 0 {
		t.Fatalf("optional field was required: %v %#v", err, errors)
	}
}

func TestAnalyzePreservesConflictingSampleValueAndRequiresStructure(t *testing.T) {
	result, err := Analyze(`#let report = json("report.json") #report.sample.id`, json.RawMessage(`{"sample":"user value"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(result.SampleData) != `{"sample":"user value"}` {
		t.Fatalf("sample was overwritten: %s", result.SampleData)
	}
	errors, err := Validate(result.DataSchema, result.SampleData)
	if err != nil {
		t.Fatal(err)
	}
	if len(errors) != 1 || errors[0].Path != "$.sample" {
		t.Fatalf("expected structural validation error: %#v", errors)
	}
}

func TestValidatePrecisePathsAndTypes(t *testing.T) {
	result, err := Analyze(`#let report = json("report.json")
#for result in report.results { #result.name }`, json.RawMessage(`{"results":[{"name":"example"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	errors, err := Validate(result.DataSchema, json.RawMessage(`{"results":[{}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(errors) != 1 || errors[0].Path != "$.results[0].name" {
		t.Fatalf("unexpected errors: %#v", errors)
	}
	errors, err = Validate(result.DataSchema, json.RawMessage(`{"results":"wrong"}`))
	if err != nil || len(errors) != 1 || errors[0].Path != "$.results" {
		t.Fatalf("unexpected type errors: %v %#v", err, errors)
	}
}

func TestDeterministicSchema(t *testing.T) {
	a, err := Analyze(`#let r = json("report.json") #r.b #r.a`, json.RawMessage(`{"z":true}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Analyze(`#let r = json("report.json") #r.b #r.a`, json.RawMessage(`{"z":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a.DataSchema) != string(b.DataSchema) || a.SchemaHash != b.SchemaHash {
		t.Fatal("schema output is not deterministic")
	}
}
