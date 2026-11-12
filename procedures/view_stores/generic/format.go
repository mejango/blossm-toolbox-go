package view

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

type Headers struct {
	ID      string        `json:"id"`
	Root    eventlib.Root `json:"root"`
	Version int64         `json:"version"`
}

type Document struct {
	Headers Headers         `json:"headers"`
	Body    json.RawMessage `json:"body"`
}

// Format isolates the formatter's input and output from the stored projection.
func Format(p eventlib.Projection, formatter func(eventlib.Projection) (json.RawMessage, error)) (Document, error) {
	if p.Deleted {
		return Document{}, eventlib.ErrNotFound
	}
	headers := Headers{ID: p.ID, Root: p.Root, Version: p.Version}
	body, err := canonicaljson.Canonical(p.Body)
	if err != nil {
		return Document{}, err
	}
	p.Body = body
	raw, err := json.Marshal(p)
	if err != nil {
		return Document{}, err
	}
	var copy eventlib.Projection
	if err := json.Unmarshal(raw, &copy); err != nil {
		return Document{}, err
	}
	body = copy.Body
	if formatter != nil {
		body, err = formatter(copy)
		if err != nil {
			return Document{}, err
		}
	}
	body, err = canonicaljson.Canonical(body)
	if err != nil {
		return Document{}, err
	}
	return Document{Headers: headers, Body: body}, nil
}

type Column struct{ Name, Path string }

// CSV selects body paths and appends the original header ID. All data is
// validated before writing; writer failures can still leave a partial export.
func CSV(w io.Writer, documents []Document, columns []Column) error {
	names := map[string]bool{"id": true}
	paths := make([][]string, len(columns))
	header := make([]string, len(columns)+1)
	for i, column := range columns {
		if strings.TrimSpace(column.Name) == "" || names[column.Name] {
			return fmt.Errorf("empty or duplicate CSV column %q", column.Name)
		}
		names[column.Name] = true
		paths[i] = strings.Split(column.Path, ".")
		for _, part := range paths[i] {
			if strings.TrimSpace(part) == "" {
				return fmt.Errorf("empty CSV path segment in %q", column.Path)
			}
		}
		header[i] = spreadsheetString(column.Name)
	}
	header[len(columns)] = "id"
	records := make([][]string, 0, len(documents)+1)
	records = append(records, header)
	for i, document := range documents {
		raw, err := canonicaljson.Canonical(document.Body)
		if err != nil {
			return fmt.Errorf("CSV document %d: %w", i, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var body any
		if err := decoder.Decode(&body); err != nil {
			return err
		}
		record := make([]string, len(header))
		for j, path := range paths {
			values := csvPathValues(body, path)
			var value any
			if len(values) == 1 {
				value = values[0]
			} else if len(values) > 1 {
				value = values
			}
			switch v := value.(type) {
			case nil:
			case string:
				record[j] = spreadsheetString(v)
			default:
				raw, err := canonicaljson.Encode(v)
				if err != nil {
					return err
				}
				record[j] = string(raw)
			}
		}
		record[len(columns)] = spreadsheetString(document.Headers.ID)
		records = append(records, record)
	}
	return csv.NewWriter(w).WriteAll(records)
}

// Terminal arrays remain JSON arrays; traversal collects all present matches.
func csvPathValues(value any, path []string) []any {
	if len(path) == 0 {
		return []any{value}
	}
	switch v := value.(type) {
	case []any:
		var values []any
		for _, item := range v {
			values = append(values, csvPathValues(item, path)...)
		}
		return values
	case map[string]any:
		if next, ok := v[path[0]]; ok {
			return csvPathValues(next, path[1:])
		}
	}
	return nil
}

func spreadsheetString(value string) string {
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") {
		return "'" + value
	}
	for _, r := range value {
		// ECMAScript whitespace includes BOM; controls include C0, DEL and C1.
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '\ufeff' {
			continue
		}
		if strings.ContainsRune("=+@-＝＋＠－", r) {
			return "'" + value
		}
		break
	}
	return value
}
