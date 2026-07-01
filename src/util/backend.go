package util

import (
	"strings"
	"os/exec"
	"os"
	"log/slog"

	"explo/src/web/backend/app"
)

// customEnvPrefix converts a playlist name like "Today's Hits"
// to an env-var prefix like "CUSTOM_TODAYS_HITS".
// Non-alphanumeric characters are collapsed into underscores.
func CustomEnvPrefix(name string) string {
	var b strings.Builder
	prevUnderscore := true // start true so leading separators are skipped
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevUnderscore = false
		} else if !prevUnderscore {
			b.WriteRune('_')
			prevUnderscore = true
		}
	}
	return "CUSTOM_" + strings.TrimRight(b.String(), "_")
}

// triggerLibraryRefresh spawns the CLI with --refresh-only in the background to
// nudge the configured media server's library scan. Fire-and-forget: errors are
// logged but do not block the caller.
func TriggerLibraryRefresh(cfg app.Config) {
	go func() {
		cmd := exec.Command(cfg.ExploPath, "--refresh-only", "--config", cfg.WebEnvPath)
		env := make([]string, 0, len(os.Environ()))
		for _, e := range os.Environ() {
			if !strings.HasPrefix(e, "WEB_UI=") {
				env = append(env, e)
			}
		}
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			slog.Warn("library refresh failed", "err", err.Error(), "output", string(out))
			return
		}
		slog.Info("library refresh complete")
	}()
}