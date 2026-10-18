package softdist

import "strings"

// Substitute replaces ${name} tokens in s with values from params. Unknown tokens
// are left untouched (so a literal MSI GUID like {23170F69-...} — which uses no
// leading $ — and any ${...} without a matching key pass through unchanged). Matching
// is exact on the name between ${ and }.
func Substitute(s string, params map[string]string) string {
	if s == "" || len(params) == 0 || !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	for {
		start := strings.Index(s, "${")
		if start < 0 {
			b.WriteString(s)
			break
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			b.WriteString(s) // no closing brace — leave the rest as-is
			break
		}
		end += start
		name := s[start+2 : end]
		if val, ok := params[name]; ok {
			b.WriteString(s[:start])
			b.WriteString(val)
		} else {
			b.WriteString(s[:end+1]) // unknown token: keep ${name} verbatim
		}
		s = s[end+1:]
	}
	return b.String()
}

// paramEnv turns the parameter map into AEGIS_PARAM_<NAME>=value environment entries
// for child scripts to read. Names are upper-cased; dots/dashes/spaces become
// underscores so they are valid env var names.
func paramEnv(params map[string]string) []string {
	if len(params) == 0 {
		return nil
	}
	repl := strings.NewReplacer(".", "_", "-", "_", " ", "_")
	out := make([]string, 0, len(params))
	for k, v := range params {
		out = append(out, "AEGIS_PARAM_"+strings.ToUpper(repl.Replace(k))+"="+v)
	}
	return out
}
