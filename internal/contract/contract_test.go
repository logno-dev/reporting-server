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

func TestAnalyzeDistinguishesContentBlocksFromIndexedAccess(t *testing.T) {
	result, err := Analyze(`#let report = json("report.json")
#if report.show [Visible] else [Hidden]
#report.title [content block]`, json.RawMessage(`{"show":true,"title":"Example"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("content blocks must not be treated as indexed access: %#v", result.Diagnostics)
	}

	result, err = Analyze(`#let report = json("report.json")
#report.values[index]`, json.RawMessage(`{"values":["example"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Message != "dynamic indexed access rooted in report data cannot be analyzed" {
		t.Fatalf("expected blocking indexed-access diagnostic: %#v", result.Diagnostics)
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

func TestInvoiceArrayMethodsDoNotBecomeDataFields(t *testing.T) {
	source := `#let data = json("data.json")
#let organization = data.at("organization")
#let family = data.at("family")
#let session = data.at("session")
#let amounts = data.at("amounts")

#set page(paper: "us-letter", margin: 0.7in)
#set text(font: "Libertinus Serif", size: 10pt, fill: rgb("243044"))
#set par(leading: 0.65em)

#align(right)[
  #text(size: 19pt, weight: "bold", fill: rgb("174f3a"))[#data.at("title")]
  #v(4pt)
  Issued #data.at("issueDate")
]

#text(size: 16pt, weight: "bold")[#organization.at("name")]
#if organization.at("address") != "" [#linebreak()#organization.at("address")]
#if organization.at("contact") != "" [#linebreak()#organization.at("contact")]

#v(18pt)
#box(fill: rgb("f3f6fa"), inset: 12pt, radius: 4pt, width: 100%)[
  *Bill to* #h(1fr) *Session*#linebreak()
  #family.at("name") #h(1fr) #session.at("name")
  #if family.at("guardians") != "" [#linebreak()#family.at("guardians")]
]

#v(18pt)
#let items = data.at("lineItems", default: ())
#if items.len() > 0 {
  table(
    columns: (1fr, auto), inset: 9pt,
    stroke: (x: none, y: 0.5pt + rgb("d9e0e8")),
    table.header([*Description*], [*Amount*]),
    ..items.map(item => (item.at("description"), align(right)[#item.at("amount")])).flatten(),
  )
  v(12pt)
}
#table(
  columns: (1fr, auto),
  inset: 9pt,
  stroke: (x: none, y: 0.5pt + rgb("d9e0e8")),
  [Total charges], align(right)[#amounts.at("total")],
  [Amount paid], align(right)[#amounts.at("paid")],
  text(weight: "bold")[Balance due], align(right)[#text(weight: "bold", fill: rgb("174f3a"))[#amounts.at("balance")]],
)

#if data.at("dueDate") != "" [#v(12pt)*Due date:* #data.at("dueDate")]
#if data.at("footer") != "" [#v(24pt)#box(stroke: 0.5pt + rgb("d9e0e8"), inset: 10pt, width: 100%)[#data.at("footer")]]`
	sample := json.RawMessage(`{
  "title": "DVCLC Invoice",
  "issueDate": "9/24/2026",
  "organization": { "name": "DVCLC", "address": "123 Main Street, Palm Desert, CA 92260", "contact": "office@example.com | example.com" },
  "family": { "name": "Rivera Family", "guardians": "Alex Rivera, Jordan Rivera" },
  "session": { "name": "Fall 2026" },
  "lineItems": [
    { "description": "Session registration fee — 2 children; 2-child family rate", "amount": "$200.00" },
    { "description": "Sam — Art Studio", "amount": "$30.00" },
    { "description": "Taylor — Science Lab", "amount": "$20.00" }
  ],
  "amounts": { "total": "$250.00", "paid": "$0.00", "balance": "$250.00" },
  "dueDate": "10/15/2026",
  "footer": "Payment is due by the date shown above."
}`)
	result, err := Analyze(source, sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
	errors, err := Validate(result.DataSchema, sample)
	if err != nil || len(errors) != 0 {
		t.Fatalf("invoice data must satisfy its contract: %v %#v", err, errors)
	}
	var original any
	if err := json.Unmarshal(sample, &original); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(result.SampleData) {
		t.Fatalf("analysis changed invoice data: %s", result.SampleData)
	}
}

func TestAnalyzeDistinguishesMethodsFromFields(t *testing.T) {
	result, err := Analyze(`#let data = json("data.json")
#data.items.len()
#data.items.map(item => item).flatten()
#data.title.upper()
#data.metadata.len
#data.metadata.at("map")`, json.RawMessage(`{"items":["one","two"],"title":"Invoice","metadata":{"len":2,"map":"kept"}}`))
	if err != nil {
		t.Fatal(err)
	}
	validationErrors, err := Validate(result.DataSchema, result.SampleData)
	if err != nil || len(validationErrors) != 0 {
		t.Fatalf("methods must not require object fields: %v %#v", err, validationErrors)
	}
	validationErrors, err = Validate(result.DataSchema, json.RawMessage(`{"items":["one"],"title":"Invoice","metadata":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, validationError := range validationErrors {
		paths[validationError.Path] = true
	}
	if len(validationErrors) != 2 || !paths["$.metadata.len"] || !paths["$.metadata.map"] {
		t.Fatalf("real fields named len and map must still be required: %#v", validationErrors)
	}
}

func TestAnalyzeMergesTypesAcrossArrayItems(t *testing.T) {
	source := `#let report = json("report.json")
#for question in report.questions [
  #for row in question.listRows [#if row.accomplished [Accomplished]]
]`
	sample := json.RawMessage(`{
  "questions": [
    {"listRows":[{"accomplished":""}]},
    {"listRows":[]},
    {"listRows":[{"accomplished":false}]}
  ]
}`)
	result, err := Analyze(source, sample)
	if err != nil {
		t.Fatal(err)
	}
	validationErrors, err := Validate(result.DataSchema, sample)
	if err != nil || len(validationErrors) != 0 {
		t.Fatalf("sample must satisfy its merged contract: %v %#v\nschema: %s", err, validationErrors, result.DataSchema)
	}
	validationErrors, err = Validate(result.DataSchema, json.RawMessage(`{"questions":[{"listRows":[{"accomplished":1}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(validationErrors) != 1 || validationErrors[0].Path != "$.questions[0].listRows[0].accomplished" || validationErrors[0].Message != "expected boolean or string" {
		t.Fatalf("unexpected validation errors: %#v", validationErrors)
	}
}
