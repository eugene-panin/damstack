package main

import (
	"bytes"
	"io"
	"slices"
	"strings"
	"sync"
)

// masked is what masker writes in place of a secret.
const masked = "••••"

// masker writes what it gets to w a line at a time, the values of secrets in
// it masked, so a log of a step keeps none of them.
type masker struct {
	mu  sync.Mutex
	w   io.Writer
	r   *strings.Replacer
	buf []byte
}

// newMasker masks the values in secrets long enough to be one, nested ones too.
func newMasker(w io.Writer, secrets map[string]any) *masker {
	var values []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			for _, line := range strings.Split(v, "\n") {
				if line = strings.TrimSpace(line); len(line) >= 6 {
					values = append(values, line)
				}
			}
		case map[string]any:
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(secrets)
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(values))
	for _, v := range slices.Compact(values) {
		pairs = append(pairs, v, masked)
	}
	return &masker{w: w, r: strings.NewReplacer(pairs...)}
}

func (m *masker) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buf = append(m.buf, p...)
	if i := bytes.LastIndexByte(m.buf, '\n'); i >= 0 {
		if _, err := io.WriteString(m.w, m.r.Replace(string(m.buf[:i+1]))); err != nil {
			return 0, err
		}
		m.buf = append(m.buf[:0], m.buf[i+1:]...)
	}
	return len(p), nil
}

// Flush writes what is left of a line.
func (m *masker) Flush() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := io.WriteString(m.w, m.r.Replace(string(m.buf)))
	m.buf = m.buf[:0]
	return err
}
