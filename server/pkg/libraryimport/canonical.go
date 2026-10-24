package libraryimport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/knowledge"
)

func frame(s string) string { return strconv.Itoa(len(s)) + ":" + s + "\n" }

func RecordDigest(frameName string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	canonical, err := CanonicalJSON(data)
	if err != nil {
		return "", err
	}
	return knowledge.Sum([]byte(frame(frameName) + frame(string(canonical)))), nil
}

func CanonicalJSON(data []byte) ([]byte, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid_utf8")
	}
	if err := validLibraryEscapes(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readLibraryJSON(decoder)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing_json")
	}
	var b strings.Builder
	if err := writeLibraryJSON(&b, value); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

func validLibraryEscapes(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return fmt.Errorf("invalid_escape")
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return fmt.Errorf("invalid_escape")
		}
		v, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return err
		}
		i += 4
		if v >= 0xD800 && v <= 0xDBFF {
			if i+6 >= len(data) || string(data[i+1:i+3]) != "\\u" {
				return fmt.Errorf("unpaired_surrogate")
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return fmt.Errorf("unpaired_surrogate")
			}
			i += 6
		} else if v >= 0xDC00 && v <= 0xDFFF {
			return fmt.Errorf("unpaired_surrogate")
		}
	}
	return nil
}

func readLibraryJSON(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := token.(type) {
	case json.Delim:
		if t == '{' {
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("invalid_key")
				}
				if _, ok := m[k]; ok {
					return nil, fmt.Errorf("duplicate_key")
				}
				v, err := readLibraryJSON(d)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			_, err = d.Token()
			return m, err
		}
		if t == '[' {
			a := []any{}
			for d.More() {
				v, err := readLibraryJSON(d)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err = d.Token()
			return a, err
		}
		return nil, fmt.Errorf("invalid_json")
	case json.Number:
		if strings.ContainsAny(string(t), ".eE") {
			return nil, fmt.Errorf("integer_required")
		}
		value, err := strconv.ParseInt(string(t), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("integer_range")
		}
		return json.Number(strconv.FormatInt(value, 10)), nil
	default:
		return t, nil
	}
}

func writeLibraryString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 32:
			fmt.Fprintf(b, "\\u%04x", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
func writeLibraryJSON(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		writeLibraryString(b, x)
	case json.Number:
		b.WriteString(string(x))
	case []any:
		b.WriteByte('[')
		for i, v := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeLibraryJSON(b, v); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeLibraryString(b, k)
			b.WriteByte(':')
			if err := writeLibraryJSON(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("invalid_json_value")
	}
	return nil
}
