package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"explo/src/config"
	"explo/src/web"
	"explo/src/web/backend/app"
	"explo/src/web/backend/run"
	"explo/src/web/backend/playlist"
	"explo/src/web/backend/jobs"
	"explo/src/web/backend/settings"
	"explo/src/web/backend/auth"
)

// ConfigResponse is returned by GET /api/config.
type ConfigResponse struct {
	Values  map[string]string `json:"values"`
	Sources map[string]string `json:"sources"` // "env" | "file"
}

type Server struct {
	cfg            config.ServerConfig
	mux            *http.ServeMux
	server         *http.Server
	authStore      *auth.AuthStore
	settings       *settings.Settings
	cronJobs       *jobs.Jobs
	manualRun      *run.ManualRun
	customPlaylist *playlist.Playlist
}

func NewServer(cfg config.ServerConfig) *Server {
	sessionManager := auth.NewSessionManager(
		auth.NewInMemorySessionStore(),
		1*time.Hour,
		7*(24*time.Hour),
		"session",
	)

	authStore := auth.NewAuthStore(
		cfg.Username,
		cfg.Password,
		sessionManager,
	)
	webCfg := app.Config{
		WebEnvPath: cfg.WebEnvPath,
		WebDataDir: cfg.WebDataDir,
		ExploPath: cfg.ExploPath,
	}

	settings := settings.NewSettings(webCfg)

	cronJobs := jobs.NewJobs()
	manualRun := run.NewManualRun(webCfg)
	playlist := playlist.NewPlaylist(webCfg, settings)

	mux := http.NewServeMux()
	s := &Server{
		cfg: cfg,
		mux: mux,
		server: &http.Server{
			Addr:    cfg.Port,
			Handler: sessionManager.Handle(mux),
		},
		authStore:      authStore,
		settings:       settings,
		cronJobs:       cronJobs,
		manualRun:      manualRun,
		customPlaylist: playlist,
	}

	s.registerRoutes()
	return s
}

func (s *Server) Start() error {
	s.initServerLog()
	s.startJobs()
	coversDir := filepath.Join(s.cfg.WebDataDir, "cache", "covers")
	if _, err := os.Stat(coversDir); os.IsNotExist(err) {
		s.customPlaylist.PrefetchCovers()
	}
	slog.Info("Explo web UI started", "addr", s.server.Addr)
	go checkForUpdate()
	return s.server.ListenAndServe()
}

func checkForUpdate() {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/LumePart/Explo/releases/latest")
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return
	}
	l := parseVer(release.TagName)
	c := parseVer(config.Version)
	newer := false
	for i := range 3 {
		if l[i] > c[i] {
			newer = true
			break
		}
		if l[i] < c[i] {
			break
		}
	}
	if newer {
		slog.Info("new version available!", "latest", release.TagName, "current", config.Version)
	}
}

func parseVer(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 3)
	var out [3]int
	for i, p := range parts {
		if i >= 3 {
			break
		}
		_, _ = fmt.Sscanf(p, "%d", &out[i])
	}
	return out
}

// Jobs to register on startup
func (s *Server) startJobs() {

	coversDir := filepath.Join(s.cfg.WebDataDir, "cache", "covers")
	if err := s.cronJobs.RegisterCoverCleanup(
		"0 3 * * *", coversDir, s.cfg.CacheSizeMB<<20); err != nil {
		slog.Warn("failed to register cover cleanup job", "err", err.Error())
	}

	if err := s.customPlaylist.RegisterCustomPlaylistRefresh(s.cronJobs); err != nil {
		slog.Warn("failed to register custom playlist refresh job", "err", err.Error())
	}

	s.cronJobs.Start()
}

// spaFS returns the filesystem to serve the frontend from.
// When WEB_DEV=true, serves directly from src/web/dist on disk so that
// running "npm run build" reflects changes without recompiling the binary.
func spaFS() (fs.FS, []byte) {
	if os.Getenv("WEB_DEV") == "true" {
		diskFS := os.DirFS("src/web/dist")
		index, _ := fs.ReadFile(diskFS, "index.html")
		return diskFS, index
	}
	embedded, _ := fs.Sub(web.DistFiles, "dist")
	index, _ := fs.ReadFile(embedded, "index.html")
	return embedded, index
}

// ── Logging ────────────────────────────────────────────────────────────────

// logPath returns the path to the single rolling log file.
func (s *Server) logPath() string {
	return filepath.Join(s.cfg.WebDataDir, "logs", "explo.log")
}

// initServerLog redirects the default slog handler so all server log output
// goes to both stderr and the rolling log file.
func (s *Server) initServerLog() {
	lf, err := s.openRunLog()
	if err != nil {
		return
	}
	w := io.MultiWriter(os.Stderr, lf)
	slog.SetDefault(slog.New(slog.NewTextHandler(w, nil)))
}

// openRunLog opens the single rolling log file in append mode.
func (s *Server) openRunLog() (*os.File, error) {
	p := s.logPath()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
}