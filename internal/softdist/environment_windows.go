//go:build windows

package softdist

import (
	"golang.org/x/sys/windows/registry"
	"strings"
)

// Registered per-user uninstall strings must expand against the token's
// environment, never the service's LOCALAPPDATA/USERPROFILE.
func (s *Session) expandEnvironment(text string) (string, error) {
	if s == nil || len(s.environment) == 0 {
		return registry.ExpandString(text)
	}
	values := make(map[string]string, len(s.environment))
	for _, entry := range s.environment {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[strings.ToUpper(key)] = value
		}
	}
	var out strings.Builder
	for {
		start := strings.IndexByte(text, '%')
		if start < 0 {
			out.WriteString(text)
			break
		}
		out.WriteString(text[:start])
		text = text[start:]
		end := strings.IndexByte(text[1:], '%')
		if end < 0 {
			out.WriteString(text)
			break
		}
		end++
		if value, ok := values[strings.ToUpper(text[1:end])]; ok {
			out.WriteString(value)
		} else {
			out.WriteString(text[:end+1])
		}
		text = text[end+1:]
	}
	return out.String(), nil
}
