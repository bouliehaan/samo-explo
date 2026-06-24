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

// handleGetLog returns the contents of the rolling log file.
func (s *Server) handleGetLog(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.logPath())
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
<<<<<<< HEAD
}

// ── Manual run ─────────────────────────────────────────────────────────────

/* var errRunAlreadyStarted = errors.New("run already in progress")

// handleRun starts an explo run in the background. Clients follow output via /api/ui/run/events.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "bad form data", http.StatusBadRequest)
		return
	}

	args := buildArgs(r.FormValue("playlist"), r.FormValue("download_mode"),
		r.FormValue("persist") == "false", r.FormValue("exclude_local") == "true",
		s.cfg.WebEnvPath)

	if err := s.startRun(args); err != nil {
		if errors.Is(err, errRunAlreadyStarted) {
			http.Error(w, "a run is already in progress", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(s.currentRunStatus()); err != nil {
		slog.Warn("failed to encode current run status", "msg", err.Error())
	}
}

// triggerLibraryRefresh spawns the CLI with --refresh-only in the background to
// nudge the configured media server's library scan. Fire-and-forget: errors are
// logged but do not block the caller.
func (s *Server) triggerLibraryRefresh() {
	go func() {
		cmd := exec.Command(s.cfg.ExploPath, "--refresh-only", "--config", s.cfg.WebEnvPath)
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

func (s *Server) startRun(args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, s.cfg.ExploPath, args...)
	// Strip WEB_UI from env so the child process runs normally, not as web server.
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "WEB_UI=") {
			env = append(env, e)
		}
	}
	cmd.Env = env

	pr, pw, err := os.Pipe()
	if err != nil {
		cancel()
		return fmt.Errorf("failed to create pipe: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	lf, err := s.openRunLog()
	if err != nil {
		slog.Warn("failed to open run log", "err", err.Error())
	}

	s.manualRun.mu.Lock()
	if s.manualRun.running {
		s.manualRun.mu.Unlock()
		cancel()
		if err := pr.Close(); err != nil {
			slog.Warn("failed to close file reader", "err", err.Error())
		}

		if err := pw.Close(); err != nil {
			slog.Warn("failed to close file writer", "err", err.Error())
		}
		if lf != nil {
			if err := pw.Close(); err != nil {
				slog.Warn("failed to close file writer", "err", err.Error())
			}
		}
		return errRunAlreadyStarted
	}
	s.manualRun.running = true
	s.manualRun.cancel = cancel
	s.manualRun.exitCode = nil
	s.manualRun.logs = nil
	s.manualRun.mu.Unlock()

	if err := cmd.Start(); err != nil {
		s.finishRun(1)
		cancel()
		if err := pr.Close(); err != nil {
			slog.Warn("failed to close file reader", "err", err.Error())
		}

		if err := pw.Close(); err != nil {
			slog.Warn("failed to close file writer", "err", err.Error())
		}
		if lf != nil {
			if err := lf.Close(); err != nil {
				slog.Warn("failed to close run log", "err", err.Error())
			}
		}
		return fmt.Errorf("failed to start explo: %w", err)
	}

	// Close write end in parent so reader gets EOF when child exits.
	if err := pw.Close(); err != nil {
		slog.Warn("failed to close file writer", "err", err.Error())
	}

	go s.collectRunOutput(cmd, pr, lf)
	return nil
}

func (s *Server) collectRunOutput(cmd *exec.Cmd, pr *os.File, lf *os.File) {
	defer func() {
		if cerr := pr.Close(); cerr != nil {
			slog.Error("failed to close source file", "err", cerr.Error())
		}
	}()

	if lf != nil {
		defer func() {
			if cerr := lf.Close(); cerr != nil {
				slog.Error("failed to close source file", "err", cerr.Error())
			}
		}()
	}

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		line := scanner.Text()
		// Echo to stdout so runs show up in docker logs.
		_, _ = fmt.Fprintln(os.Stdout, line)
		if lf != nil {
			if _, err := fmt.Fprintln(lf, line); err != nil {
				s.appendRunLog("failed to write run output: " + err.Error())
			}
		}
		s.appendRunLog(line)
	}
	if err := scanner.Err(); err != nil {
		s.appendRunLog("failed to read run output: " + err.Error())
	}

	code := 0
	if err := cmd.Wait(); err != nil && cmd.ProcessState == nil {
		code = 1
	}
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	s.finishRun(code)
}

func (s *Server) handleStopRun(w http.ResponseWriter, r *http.Request) {
	s.manualRun.mu.Lock()
	cancel := s.manualRun.cancel
	running := s.manualRun.running
	s.manualRun.mu.Unlock()

	if !running || cancel == nil {
		http.Error(w, "no run is currently in progress", http.StatusConflict)
		return
	}

	cancel()
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) currentRunStatus() RunStatus {
	s.manualRun.mu.Lock()
	defer s.manualRun.mu.Unlock()

	var exitCode *int
	if s.manualRun.exitCode != nil {
		code := *s.manualRun.exitCode
		exitCode = &code
	}
	return RunStatus{Running: s.manualRun.running, ExitCode: exitCode}
}

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.currentRunStatus()); err != nil {
		slog.Warn("failed encoding current run status to response")
	}
}

// ── SSE event stream ───────────────────────────────────────────────────────

func (s *Server) appendRunLog(line string) {
	event := runEvent{data: line}

	s.manualRun.mu.Lock()
	s.manualRun.logs = append(s.manualRun.logs, line)
	subscribers := make([]chan runEvent, 0, len(s.manualRun.subscribers))
	for ch := range s.manualRun.subscribers {
		subscribers = append(subscribers, ch)
	}
	s.manualRun.mu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *Server) finishRun(code int) {
	done := runEvent{typ: "done", data: fmt.Sprintf("%d", code)}

	s.manualRun.mu.Lock()
	s.manualRun.running = false
	s.manualRun.cancel = nil
	s.manualRun.exitCode = &code
	subscribers := make([]chan runEvent, 0, len(s.manualRun.subscribers))
	for ch := range s.manualRun.subscribers {
		subscribers = append(subscribers, ch)
		delete(s.manualRun.subscribers, ch)
	}
	s.manualRun.mu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- done:
		default:
		}
		close(ch)
	}
}

// handleRunEvents streams the current in-memory run log, then follows new lines
// until the active run exits. Safe to reconnect after a browser refresh.
func (s *Server) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sendEvent := func(typ, data string) {
		if typ != "" {
			if _, err := fmt.Fprintf(w, "event: %s\n", typ); err != nil {
				slog.Warn("failed handling run event", "err", err.Error())
			}
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			slog.Warn("failed handling run event", "err", err.Error())
		}
		flusher.Flush()
	}

	ch := make(chan runEvent, 256)
	s.manualRun.mu.Lock()
	lines := append([]string(nil), s.manualRun.logs...)
	running := s.manualRun.running
	var exitCode *int
	if s.manualRun.exitCode != nil {
		code := *s.manualRun.exitCode
		exitCode = &code
	}
	if running {
		s.manualRun.subscribers[ch] = struct{}{}
	}
	s.manualRun.mu.Unlock()

	for _, line := range lines {
		sendEvent("", line)
	}
	if !running {
		if exitCode != nil {
			sendEvent("done", fmt.Sprintf("%d", *exitCode))
		}
		return
	}

	defer s.unsubscribeRun(ch)
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			sendEvent(ev.typ, ev.data)
			if ev.typ == "done" {
				return
			}
		}
	}
}

func (s *Server) unsubscribeRun(ch chan runEvent) {
	s.manualRun.mu.Lock()
	delete(s.manualRun.subscribers, ch)
	s.manualRun.mu.Unlock()
} */

// ── Helpers ────────────────────────────────────────────────────────────────

func buildArgs(playlist, downloadMode string, noPersist, excludeLocal bool, WebEnvPath string) []string {
	args := []string{"--config", WebEnvPath}
	if playlist != "" {
		args = append(args, "--playlist", playlist)
	}
	if downloadMode != "" {
		args = append(args, "--download-mode", downloadMode)
	}
	if noPersist {
		args = append(args, "--persist=false")
	}
	if excludeLocal {
		args = append(args, "--exclude-local")
	}
	return args
}
=======
}
>>>>>>> e957089 (remove functions from server.go)
