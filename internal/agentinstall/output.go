package agentinstall

import (
	"strings"
	"sync"
	"unicode"
)

type boundedOutput struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if len(b.data)+n > b.limit {
		b.overflow = true
	}
	if n >= b.limit {
		b.data = append(b.data[:0], p[n-b.limit:]...)
	} else {
		if extra := len(b.data) + n - b.limit; extra > 0 {
			copy(b.data, b.data[extra:])
			b.data = b.data[:len(b.data)-extra]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.ToValidUTF8(string(b.data), "�")
}

func redactSecrets(text string) string {
	text = strings.ToValidUTF8(text, "�")
	lowerBytes := []byte(text)
	for i, b := range lowerBytes {
		if b >= 'A' && b <= 'Z' {
			lowerBytes[i] = b + ('a' - 'A')
		}
	}
	lower := string(lowerBytes)
	var result strings.Builder
	offset := 0
	for offset < len(text) {
		start, marker := -1, ""
		for _, candidate := range []string{"bearer ", "basic ", "sk-", "ghp_", "xox", "api_key="} {
			if at := strings.Index(lower[offset:], candidate); at >= 0 && (start < 0 || offset+at < start) {
				start, marker = offset+at, candidate
			}
		}
		if start < 0 {
			break
		}
		credential := start
		if strings.HasSuffix(marker, " ") || strings.HasSuffix(marker, "=") {
			credential += len(marker)
		}
		credential += len(text[credential:]) - len(strings.TrimLeftFunc(text[credential:], func(r rune) bool { return unicode.IsSpace(r) || r == '\'' || r == '"' }))
		end := credential + len(text[credential:])
		if at := strings.IndexFunc(text[credential:], func(r rune) bool { return unicode.IsSpace(r) || strings.ContainsRune("\"',;&<>", r) }); at >= 0 {
			end = credential + at
		}
		result.WriteString(text[offset:credential])
		result.WriteString("[REDACTED]")
		offset = max(end, start+len(marker))
	}
	result.WriteString(text[offset:])
	return result.String()
}

type diagnosticStream struct {
	tail    *boundedOutput
	line    []byte
	discard bool
}

func (s *diagnosticStream) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			s.flush()
			s.tail.Write([]byte("\n"))
			s.discard = false
		} else if !s.discard {
			if len(s.line) == outputTailCap {
				s.line = nil
				s.discard = true
			} else {
				s.line = append(s.line, b)
			}
		}
	}
	return len(p), nil
}
func (s *diagnosticStream) flush() {
	if s.discard {
		s.tail.Write([]byte("[overlong diagnostic omitted]"))
	} else {
		s.tail.Write([]byte(redactSecrets(string(s.line))))
	}
	s.line = nil
}
