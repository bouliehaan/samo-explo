package settings

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
)

// PathTemplatePreset is a named folder-structure template saved by the user.
type PathTemplatePreset struct {
	Name     string `json:"name"`
	Template string `json:"template"`
}

func pathTemplatesFilePath(cfgDir string) string {
	return filepath.Join(cfgDir, "path-templates.json")
}

func loadPathTemplates(cfgDir string) []PathTemplatePreset {
	data, err := os.ReadFile(pathTemplatesFilePath(cfgDir))
	if err != nil {
		return nil
	}
	var out []PathTemplatePreset
	if err := json.Unmarshal(data, &out); err != nil {
		slog.Warn("path-templates: failed to parse", "err", err)
		return nil
	}
	return out
}

func savePathTemplates(cfgDir string, presets []PathTemplatePreset) error {
	raw, err := json.MarshalIndent(presets, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(pathTemplatesFilePath(cfgDir), raw, 0644)
}
