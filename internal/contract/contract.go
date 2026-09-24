// Package contract derives and validates the constrained data contracts used by report templates.
// It intentionally recognizes common Typst expressions rather than attempting complete Typst analysis.
package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	MaxSourceBytes = 1 << 20
	MaxDataBytes   = 2 << 20
	maxTokens      = 100000
	maxPaths       = 2000
	maxDepth       = 32
)

var ErrTooComplex = errors.New("template contract exceeds complexity limits")

type Diagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Offset   int    `json:"offset,omitempty"`
}

type ValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Result struct {
	SampleData  json.RawMessage `json:"sampleData"`
	DataSchema  json.RawMessage `json:"dataSchema"`
	SchemaHash  string          `json:"schemaHash"`
	Diagnostics []Diagnostic    `json:"diagnostics"`
}

type tokenKind byte

const (
	identifier tokenKind = iota
	stringToken
	symbol
)

type token struct {
	kind   tokenKind
	text   string
	offset int
}

type pathPart struct {
	name     string
	optional bool
}

type pathInfo struct {
	parts   []pathPart
	arrayAt int
	leaf    bool
}

type alias struct {
	parts   []pathPart
	arrayAt int
}

type node struct {
	children map[string]*node
	required bool
	array    bool
	leaf     bool
}

// Analyze derives a contract and merges missing inferred fields into sampleData.
func Analyze(source string, sampleData json.RawMessage) (Result, error) {
	if len(source) > MaxSourceBytes || len(sampleData) > MaxDataBytes {
		return Result{}, ErrTooComplex
	}
	var sample map[string]any
	decoder := json.NewDecoder(bytes.NewReader(sampleData))
	decoder.UseNumber()
	if err := decoder.Decode(&sample); err != nil || sample == nil {
		return Result{}, errors.New("sample data must be a JSON object")
	}
	tokens, err := lex(source)
	if err != nil {
		return Result{}, err
	}
	paths, diagnostics, err := analyzeTokens(tokens)
	if err != nil {
		return Result{}, err
	}
	root := &node{children: map[string]*node{}}
	for _, path := range paths {
		current := root
		if len(path.parts) > maxDepth {
			return Result{}, ErrTooComplex
		}
		for index, part := range path.parts {
			if current.children[part.name] == nil {
				current.children[part.name] = &node{children: map[string]*node{}}
			}
			current = current.children[part.name]
			if path.arrayAt == index+1 {
				current.array = true
			}
			if !part.optional {
				current.required = true
			}
		}
		current.leaf = current.leaf || path.leaf
	}
	mergeObject(sample, root)
	schema, err := schemaFor(sample, root, 0)
	if err != nil {
		return Result{}, err
	}
	schemaObject := schema.(map[string]any)
	schemaObject["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	mergedJSON, err := json.Marshal(sample)
	if err != nil {
		return Result{}, err
	}
	schemaJSON, err := json.Marshal(schemaObject)
	if err != nil {
		return Result{}, err
	}
	digest := sha256.Sum256(schemaJSON)
	return Result{SampleData: mergedJSON, DataSchema: schemaJSON, SchemaHash: hex.EncodeToString(digest[:]), Diagnostics: diagnostics}, nil
}

func lex(source string) ([]token, error) {
	result := make([]token, 0, len(source)/4)
	for i := 0; i < len(source); {
		if len(result) > maxTokens {
			return nil, ErrTooComplex
		}
		if unicode.IsSpace(rune(source[i])) {
			i++
			continue
		}
		if source[i] == '/' && i+1 < len(source) && source[i+1] == '/' {
			i += 2
			for i < len(source) && source[i] != '\n' {
				i++
			}
			continue
		}
		if source[i] == '/' && i+1 < len(source) && source[i+1] == '*' {
			start := i
			i += 2
			depth := 1
			for i < len(source) && depth > 0 {
				if i+1 < len(source) && source[i:i+2] == "/*" {
					depth++
					i += 2
					continue
				}
				if i+1 < len(source) && source[i:i+2] == "*/" {
					depth--
					i += 2
					continue
				}
				i++
			}
			if depth != 0 {
				return nil, fmt.Errorf("unterminated comment at byte %d", start)
			}
			continue
		}
		if source[i] == '"' {
			start := i
			i++
			var value strings.Builder
			for i < len(source) && source[i] != '"' {
				if source[i] == '\\' && i+1 < len(source) {
					var decoded string
					j := i + 2
					for j < len(source) && source[j] != '"' && source[j] != '\\' {
						j++
					}
					if err := json.Unmarshal([]byte(source[start:j+1]), &decoded); err == nil && j < len(source) && source[j] == '"' {
						value.Reset()
						value.WriteString(decoded)
						i = j
						break
					}
					value.WriteByte(source[i+1])
					i += 2
					continue
				}
				value.WriteByte(source[i])
				i++
			}
			if i >= len(source) {
				return nil, fmt.Errorf("unterminated string at byte %d", start)
			}
			result = append(result, token{kind: stringToken, text: value.String(), offset: start})
			i++
			continue
		}
		if isIdentifierStart(source[i]) {
			start := i
			for i < len(source) && (isIdentifierStart(source[i]) || source[i] >= '0' && source[i] <= '9' || source[i] == '-') {
				i++
			}
			result = append(result, token{kind: identifier, text: source[start:i], offset: start})
			continue
		}
		result = append(result, token{kind: symbol, text: source[i : i+1], offset: i})
		i++
	}
	return result, nil
}

func isIdentifierStart(value byte) bool {
	return value == '_' || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func analyzeTokens(tokens []token) ([]pathInfo, []Diagnostic, error) {
	aliases := map[string]alias{}
	var paths []pathInfo
	diagnostics := make([]Diagnostic, 0)
	for i := 0; i < len(tokens); i++ {
		// Root declarations are deliberately limited to the two files mounted by the renderer.
		if i+6 < len(tokens) && tokens[i].text == "let" && tokens[i+1].kind == identifier && tokens[i+2].text == "=" && tokens[i+3].text == "json" && tokens[i+4].text == "(" && tokens[i+5].kind == stringToken && tokens[i+6].text == ")" {
			if tokens[i+5].text == "report.json" || tokens[i+5].text == "data.json" {
				aliases[tokens[i+1].text] = alias{}
			}
			continue
		}
		if i+3 < len(tokens) && tokens[i].text == "let" && tokens[i+1].kind == identifier && tokens[i+2].text == "=" {
			if base, ok := aliases[tokens[i+3].text]; ok {
				info, end, diags := parseAccess(tokens, i+3, base)
				diagnostics = append(diagnostics, diags...)
				aliases[tokens[i+1].text] = alias{parts: info.parts, arrayAt: info.arrayAt}
				if len(info.parts) > 0 {
					info.leaf = false
					paths = append(paths, info)
				}
				i = end
				continue
			}
		}
		if i+3 < len(tokens) && tokens[i].text == "for" && tokens[i+1].kind == identifier && tokens[i+2].text == "in" {
			if base, ok := aliases[tokens[i+3].text]; ok {
				info, end, diags := parseAccess(tokens, i+3, base)
				diagnostics = append(diagnostics, diags...)
				info.arrayAt, info.leaf = len(info.parts), false
				paths = append(paths, info)
				aliases[tokens[i+1].text] = alias{parts: info.parts, arrayAt: info.arrayAt}
				i = end
				continue
			}
		}
		if base, ok := aliases[tokens[i].text]; ok {
			info, end, diags := parseAccess(tokens, i, base)
			diagnostics = append(diagnostics, diags...)
			if len(info.parts) > 0 {
				info.leaf = true
				paths = append(paths, info)
			}
			i = end
		}
		if len(paths) > maxPaths {
			return nil, nil, ErrTooComplex
		}
	}
	return paths, diagnostics, nil
}

func parseAccess(tokens []token, start int, base alias) (pathInfo, int, []Diagnostic) {
	parts := append([]pathPart(nil), base.parts...)
	info := pathInfo{parts: parts, arrayAt: base.arrayAt}
	var diagnostics []Diagnostic
	i := start
	for i+1 < len(tokens) {
		if tokens[i+1].text == "." && i+2 < len(tokens) && tokens[i+2].kind == identifier {
			field := tokens[i+2]
			if field.text == "at" && i+3 < len(tokens) && tokens[i+3].text == "(" {
				if i+4 >= len(tokens) || tokens[i+4].kind != stringToken {
					diagnostics = append(diagnostics, Diagnostic{Severity: "error", Message: "dynamic .at access rooted in report data cannot be analyzed", Offset: field.offset})
					return info, i + 3, diagnostics
				}
				optional := false
				end := i + 5
				depth := 1
				for end < len(tokens) && depth > 0 {
					if tokens[end].text == "(" {
						depth++
					}
					if tokens[end].text == ")" {
						depth--
					}
					if depth == 1 && tokens[end].text == "default" && end+1 < len(tokens) && tokens[end+1].text == ":" {
						optional = true
					}
					end++
				}
				info.parts = append(info.parts, pathPart{name: tokens[i+4].text, optional: optional})
				i = end - 1
				continue
			}
			info.parts = append(info.parts, pathPart{name: field.text})
			i += 2
			continue
		}
		if tokens[i+1].text == "[" {
			diagnostics = append(diagnostics, Diagnostic{Severity: "error", Message: "dynamic indexed access rooted in report data cannot be analyzed", Offset: tokens[i+1].offset})
		}
		break
	}
	return info, i, diagnostics
}

func mergeObject(value map[string]any, structure *node) {
	for name, child := range structure.children {
		existing, ok := value[name]
		if child.array {
			array, valid := existing.([]any)
			if !ok {
				array, valid = []any{map[string]any{}}, true
				value[name] = array
			}
			if ok && !valid {
				continue
			}
			if valid && len(array) == 0 {
				array = append(array, map[string]any{})
				value[name] = array
			}
			if len(array) > 0 {
				if item, ok := array[0].(map[string]any); ok {
					mergeObject(item, child)
				}
			}
			continue
		}
		if len(child.children) > 0 {
			object, valid := existing.(map[string]any)
			if !ok {
				object, valid = map[string]any{}, true
				value[name] = object
			}
			if valid {
				mergeObject(object, child)
			}
			continue
		}
		if !ok {
			value[name] = ""
		}
	}
}

func schemaFor(value any, structure *node, depth int) (any, error) {
	if depth > maxDepth {
		return nil, ErrTooComplex
	}
	if structure.array {
		itemStructure := *structure
		itemStructure.array = false
		var itemValue any = map[string]any{}
		if array, ok := value.([]any); ok && len(array) > 0 {
			itemValue = array[0]
		}
		item, err := schemaFor(itemValue, &itemStructure, depth+1)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": item}, nil
	}
	if len(structure.children) > 0 {
		object, _ := value.(map[string]any)
		if object == nil {
			object = map[string]any{}
		}
		properties := map[string]any{}
		required := []string{}
		keys := make([]string, 0, len(object)+len(structure.children))
		seen := map[string]bool{}
		for key := range object {
			seen[key] = true
			keys = append(keys, key)
		}
		for key := range structure.children {
			if !seen[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := structure.children[key]
			if child == nil {
				child = &node{children: map[string]*node{}}
			}
			property, err := schemaFor(object[key], child, depth+1)
			if err != nil {
				return nil, err
			}
			properties[key] = property
			if child.required {
				required = append(required, key)
			}
		}
		result := map[string]any{"type": "object", "properties": properties, "additionalProperties": true}
		if len(required) > 0 {
			result["required"] = required
		}
		return result, nil
	}
	switch typed := value.(type) {
	case map[string]any:
		properties := map[string]any{}
		required := []string{}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := structure.children[key]
			if child == nil {
				child = &node{children: map[string]*node{}}
			}
			property, err := schemaFor(typed[key], child, depth+1)
			if err != nil {
				return nil, err
			}
			properties[key] = property
			if child.required {
				required = append(required, key)
			}
		}
		result := map[string]any{"type": "object", "properties": properties, "additionalProperties": true}
		if len(required) > 0 {
			result["required"] = required
		}
		return result, nil
	case []any:
		item := any(map[string]any{"type": "string"})
		if len(typed) > 0 {
			var err error
			item, err = schemaFor(typed[0], structure, depth+1)
			if err != nil {
				return nil, err
			}
		} else if len(structure.children) > 0 {
			item, _ = schemaFor(map[string]any{}, structure, depth+1)
		}
		return map[string]any{"type": "array", "items": item}, nil
	case string:
		return map[string]any{"type": "string"}, nil
	case bool:
		return map[string]any{"type": "boolean"}, nil
	case json.Number:
		if strings.ContainsAny(string(typed), ".eE") {
			return map[string]any{"type": "number"}, nil
		}
		return map[string]any{"type": "integer"}, nil
	case nil:
		return map[string]any{"type": "null"}, nil
	default:
		return nil, fmt.Errorf("unsupported sample value %T", value)
	}
}

// Validate validates data against the locally generated schema subset. It performs no reference resolution.
func Validate(schema, data json.RawMessage) ([]ValidationError, error) {
	if len(schema) > MaxDataBytes || len(data) > MaxDataBytes {
		return nil, ErrTooComplex
	}
	var schemaValue map[string]any
	var dataValue any
	if err := json.Unmarshal(schema, &schemaValue); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&dataValue); err != nil {
		return nil, fmt.Errorf("decode data: %w", err)
	}
	var result []ValidationError
	if err := validateAt(schemaValue, dataValue, "$", 0, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func validateAt(schema map[string]any, value any, path string, depth int, result *[]ValidationError) error {
	if depth > maxDepth || len(*result) > maxPaths {
		return ErrTooComplex
	}
	typeName, _ := schema["type"].(string)
	valid := typeMatches(typeName, value)
	if !valid {
		*result = append(*result, ValidationError{Path: path, Message: "expected " + typeName})
		return nil
	}
	if typeName == "object" {
		object := value.(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, raw := range required {
				name, _ := raw.(string)
				if _, exists := object[name]; !exists {
					*result = append(*result, ValidationError{Path: joinPath(path, name), Message: "required field is missing"})
				}
			}
		}
		for name, childValue := range properties {
			childData, exists := object[name]
			if !exists {
				continue
			}
			child, ok := childValue.(map[string]any)
			if !ok {
				return errors.New("invalid generated schema property")
			}
			if err := validateAt(child, childData, joinPath(path, name), depth+1, result); err != nil {
				return err
			}
		}
	}
	if typeName == "array" {
		itemSchema, ok := schema["items"].(map[string]any)
		if !ok {
			return errors.New("invalid generated array schema")
		}
		for index, item := range value.([]any) {
			if err := validateAt(itemSchema, item, fmt.Sprintf("%s[%d]", path, index), depth+1, result); err != nil {
				return err
			}
		}
	}
	return nil
}

func typeMatches(typeName string, value any) bool {
	switch typeName {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		return ok && !strings.ContainsAny(string(number), ".eE")
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func joinPath(parent, name string) string {
	if name != "" && isIdentifierStart(name[0]) {
		for i := 1; i < len(name); i++ {
			if !isIdentifierStart(name[i]) && !(name[i] >= '0' && name[i] <= '9') {
				return parent + "[" + fmt.Sprintf("%q", name) + "]"
			}
		}
		return parent + "." + name
	}
	return parent + "[" + fmt.Sprintf("%q", name) + "]"
}
