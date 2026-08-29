package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func JCS(v any) ([]byte, error) {
	var sb strings.Builder
	if err := jcsWrite(&sb, v); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

func Equal(a, b any) bool {
	ja, err := JCS(a)
	if err != nil {
		return false
	}
	jb, err := JCS(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ja, jb)
}

func jcsWrite(sb *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		if x {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return fmt.Errorf("jcs: non-finite number")
		}
		if x == 0 {
			sb.WriteByte('0')
			return nil
		}
		sb.WriteString(jcsNumber(x))
	case string:
		if !utf8.ValidString(x) {
			return fmt.Errorf("jcs: invalid UTF-8 string")
		}
		jcsString(sb, x)
	case []any:
		sb.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				sb.WriteByte(',')
			}
			if err := jcsWrite(sb, e); err != nil {
				return err
			}
		}
		sb.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			jcsString(sb, k)
			sb.WriteByte(':')
			if err := jcsWrite(sb, x[k]); err != nil {
				return err
			}
		}
		sb.WriteByte('}')
	default:
		return fmt.Errorf("jcs: unsupported value type %T", v)
	}
	return nil
}

func jcsNumber(f float64) string {
	neg := f < 0
	if neg {
		f = -f
	}
	s := strconv.FormatFloat(f, 'e', -1, 64)
	e := strings.IndexByte(s, 'e')
	digits := strings.Replace(s[:e], ".", "", 1)
	digits = strings.TrimRight(digits, "0")
	exp, _ := strconv.Atoi(s[e+1:])
	var out strings.Builder
	if neg {
		out.WriteByte('-')
	}
	if exp < -6 || exp >= 21 {
		out.WriteByte(digits[0])
		if len(digits) > 1 {
			out.WriteByte('.')
			out.WriteString(digits[1:])
		}
		out.WriteByte('e')
		if exp < 0 {
			out.WriteByte('-')
			exp = -exp
		} else {
			out.WriteByte('+')
		}
		out.WriteString(strconv.Itoa(exp))
		return out.String()
	}
	if exp >= 0 {
		if len(digits) > exp+1 {
			out.WriteString(digits[:exp+1])
			out.WriteByte('.')
			out.WriteString(digits[exp+1:])
		} else {
			out.WriteString(digits)
			for range exp + 1 - len(digits) {
				out.WriteByte('0')
			}
		}
		return out.String()
	}
	out.WriteString("0.")
	for range -exp - 1 {
		out.WriteByte('0')
	}
	out.WriteString(digits)
	return out.String()
}

func jcsString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\b':
			sb.WriteString(`\b`)
		case '\t':
			sb.WriteString(`\t`)
		case '\n':
			sb.WriteString(`\n`)
		case '\f':
			sb.WriteString(`\f`)
		case '\r':
			sb.WriteString(`\r`)
		case 0x2028:
			sb.WriteString(`\u2028`)
		case 0x2029:
			sb.WriteString(`\u2029`)
		default:
			if r < 0x20 {
				fmt.Fprintf(sb, `\u%04x`, r)
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

func decodeJSON(raw []byte) any {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		panic(err)
	}
	return v
}
