package backend

import (
	"os"
	"net/http"
	"path/filepath"
	"log/slog"
	"encoding/json"
	"strings"
)

// handleGetLog returns the contents of the rolling log file.
func (s *Server) handleGetLog(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.manualRun.LogPath())
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "failed to read log", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := w.Write(data); err != nil {
		slog.Error("failed writing http response", "msg", err.Error())
	}
}

// handleBrowse returns subdirectories of the requested path for filesystem autocomplete.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path := filepath.Clean(r.URL.Query().Get("path"))
	if path == "" || path == "." {
		path = "/"
	}
	if !filepath.IsAbs(path) {
		http.Error(w, "path must be absolute", http.StatusBadRequest)
		return
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode([]string{}); err != nil {
			slog.Error("failed to encode empty slice", "msg", err.Error())
		}
		return
	}

	dirs := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, filepath.Join(path, e.Name()))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(dirs); err != nil {
		slog.Warn("failed to encode directories to response", "err", err.Error())
	}
}