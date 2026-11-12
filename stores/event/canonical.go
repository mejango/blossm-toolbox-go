package eventstore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"unicode/utf8"
)

// Canonical defines Blossm Go v1 JSON: sorted UTF-8 keys, no insignificant
// whitespace, finite IEEE754 numbers with safe integers and Go shortest decimal
// spelling. Ethereum integers are decimal strings, never JSON floating values.
// This is an internal protocol, not an RFC8785 or historical JS encoding claim.
func Canonical(raw []byte) (json.RawMessage, error) {
	if err := validateEscapes(raw); err != nil {
		return nil, err
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := readValue(d)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON data")
	}
	return json.Marshal(v)
}
func readValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch x := t.(type) {
	case json.Delim:
		if x == '{' {
			m := map[string]any{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, e
				}
				key := k.(string)
				if _, ok := m[key]; ok {
					return nil, fmt.Errorf("duplicate JSON key")
				}
				v, e := readValue(d)
				if e != nil {
					return nil, e
				}
				m[key] = v
			}
			_, e := d.Token()
			return m, e
		}
		if x == '[' {
			a := []any{}
			for d.More() {
				v, e := readValue(d)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			_, e := d.Token()
			return a, e
		}
		return nil, fmt.Errorf("unexpected delimiter")
	case json.Number:
		n, e := strconv.ParseFloat(string(x), 64)
		if e != nil || math.IsInf(n, 0) || math.IsNaN(n) || math.Abs(n) > 9007199254740991 {
			return nil, fmt.Errorf("number outside protocol range")
		}
		if n == 0 {
			n = 0
		}
		if math.Trunc(n) == n {
			return json.Number(strconv.FormatFloat(n, 'f', 0, 64)), nil
		}
		return json.Number(strconv.FormatFloat(n, 'g', -1, 64)), nil
	default:
		return t, nil
	}
}
func Encode(v any) (json.RawMessage, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	return Canonical(b)
}
func Digest(v any) (string, error) {
	b, e := Encode(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Decode rejects unknown fields and trailing input at typed application boundaries.
func Decode(raw json.RawMessage, dst any) error {
	b, err := Canonical(raw)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

// encoding/json replaces unpaired UTF-16 escapes; committed bytes must never
// depend on that lossy replacement. Validate escape pairs before decoding.
func validateEscapes(raw []byte) error {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			break
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return fmt.Errorf("short Unicode escape")
		}
		n, e := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if e != nil {
			return e
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return fmt.Errorf("unpaired low surrogate")
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return fmt.Errorf("unpaired high surrogate")
			}
			lo, e := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if e != nil || lo < 0xdc00 || lo > 0xdfff {
				return fmt.Errorf("invalid surrogate pair")
			}
			i += 6
		}
	}
	return nil
}
