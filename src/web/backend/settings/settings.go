package settings

import (
	"strings"
	"os"
	"fmt"

	"explo/src/web/backend/app"
)

// ConfigResponse is returned by GET /api/config.
type ConfigResponse struct {
	Values  map[string]string `json:"values"`
	Sources map[string]string `json:"sources"` // "env" | "file"
}

type Settings struct {
	cfg app.Config
}

func NewSettings(Config app.Config) *Settings {
	return &Settings{cfg: Config}
}
// parseEnvText parses key=value lines, ignoring comments, blanks and unquotes variables
func (s *Settings) ParseEnvText(text string) map[string]string {
	out := map[string]string{}
	for line := range strings.SplitSeq(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		if k = strings.TrimSpace(k); k != "" {
			v = strings.TrimSpace(v)

			// unquote if quoted
			if len(v) >= 2 {
				if (v[0] == '\'' && v[len(v)-1] == '\'') ||
					(v[0] == '"' && v[len(v)-1] == '"') {
					v = v[1 : len(v)-1]
				}
			}
			out[k] = v
		}
	}
	return out
}

// updateEnvKeys reads the env file (falling back to fallback if missing), updates the
// given key=value pairs in-place preserving comments, and writes the result back.
func (s *Settings) UpdateEnvKeys(updates map[string]string, fallback []byte) error {
	path := s.cfg.WebEnvPath
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data = fallback
	} else if err != nil {
		return err
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	touched := make(map[string]bool)

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if val, ok := updates[key]; ok {
			if val == "" {
				lines[i] = "" // remove by blanking
			} else {
				lines[i] = key + "=" + formatEnvValue(val)
			}
			touched[key] = true
		}
	}

	// Append any keys that weren't already in the file
	for k, v := range updates {
		if !touched[k] && v != "" {
			lines = append(lines, k+"="+formatEnvValue(v))
		}
	}

	// Filter out consecutive blank lines left by removals
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, l := range lines {
		blank := strings.TrimSpace(l) == ""
		if blank && prevBlank {
			continue
		}
		out = append(out, l)
		prevBlank = blank
	}

	return os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0600)
}

// Check for special chars in env vars that might need quoting
func formatEnvValue(v string) string {
	// preserve already quoted values
	if strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") {
		return v
	}

	if strings.ContainsAny(v, `"$#?'`) {
		// escape single quotes inside value
		v = strings.ReplaceAll(v, `'`, `'\''`)
		return fmt.Sprintf(`'%s'`, v)
	}

	return v
}