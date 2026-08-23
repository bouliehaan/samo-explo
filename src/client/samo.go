package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"explo/src/config"
	"explo/src/models"
	"explo/src/util"
)

// Samo talks to a samo-server over its native REST API rather than the
// Subsonic compatibility surface. samo's Subsonic surface is deliberately
// read-only for playlists — it implements getPlaylists/getPlaylist but not
// createPlaylist/updatePlaylist/startScan — so a Subsonic client authenticates
// fine, downloads fine, and then fails on the last step of every run. The
// native API has all three.
//
// Auth is a bearer token (Settings -> API tokens, or `deploy.sh`, which mints
// one for you). Scans and the explo endpoints require an ADMIN token; a
// non-admin token still downloads and builds playlists, it just cannot ask the
// server to rescan, so it falls back to the SLEEP wait like other clients.
type Samo struct {
	Cfg        config.ClientConfig
	HttpClient *util.HttpClient

	headers    map[string]string
	libraryID  string
	playlistID string

	// Set when the server's own explo drop-folder integration is enabled and
	// pointed at a folder. That pipeline fingerprints each drop with AcoustID
	// and re-derives its "Explore" playlist from the folder on every pass, so
	// anything we created would be a duplicate sitting next to it.
	serverExplo bool
	// Server-side path of the drop folder, from the server's own explo config.
	// Ours is the path inside THIS container, which is almost never the same
	// string — the server needs its own to scan just the folder.
	serverExploDir string
}

func NewSamo(cfg config.ClientConfig, httpClient *util.HttpClient) *Samo {
	return &Samo{Cfg: cfg, HttpClient: httpClient}
}

type samoUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	// samo reports a role string, not a boolean; "admin" is the one that can
	// trigger scans and read the explo endpoints.
	Role string `json:"role"`
}

type samoLibrary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type samoLibraryList struct {
	Items []samoLibrary `json:"items"`
}

type samoExternalIDs struct {
	MusicBrainzRecordingID string `json:"musicBrainzRecordingId"`
	MusicBrainzTrackID     string `json:"musicBrainzTrackId"`
}

type samoAudioFile struct {
	Path string `json:"path"`
}

type samoTrack struct {
	ID              string          `json:"id"`
	Title           string          `json:"title"`
	AlbumTitle      string          `json:"albumTitle"`
	DisplayArtist   string          `json:"displayArtist"`
	ArtistNames     []string        `json:"artistNames"`
	DurationSeconds int             `json:"durationSeconds"`
	ExternalIDs     samoExternalIDs `json:"externalIds"`
	AudioFiles      []samoAudioFile `json:"audioFiles"`
}

type samoSearchResult struct {
	Tracks []samoTrack `json:"tracks"`
}

type samoPlaylist struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	TrackCount int    `json:"trackCount"`
	System     bool   `json:"system"`
}

type samoPlaylistList struct {
	Items []samoPlaylist `json:"items"`
}

type samoScanJob struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type samoScanJobList struct {
	Items []samoScanJob `json:"items"`
}

type samoExploConfig struct {
	Enabled    bool   `json:"enabled"`
	Configured bool   `json:"configured"`
	Folder     string `json:"folder"`
}

type samoExploTracks struct {
	Summary struct {
		InFolder   int `json:"inFolder"`
		Identified int `json:"identified"`
	} `json:"summary"`
}

func (c *Samo) endpoint(path string) string {
	return strings.TrimRight(c.Cfg.URL, "/") + "/api/v1" + path
}

func (c *Samo) request(method, path string, payload any) ([]byte, error) {
	var body *bytes.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	} else {
		body = bytes.NewReader(nil)
	}
	return c.HttpClient.MakeRequest(method, c.endpoint(path), body, c.headers)
}

func (c *Samo) AddHeader() error {
	c.headers = map[string]string{"Authorization": "Bearer " + c.Cfg.Creds.APIKey}
	return nil
}

// GetAuth verifies the token up front. Every other call would report the same
// 401, but scattered across a run as warnings that read like "no results" — a
// wrong token has to fail here, loudly, or a broken deployment looks like an
// empty week.
func (c *Samo) GetAuth() error {
	body, err := c.request("GET", "/users/me", nil)
	if err != nil {
		return fmt.Errorf("could not authenticate against samo at %s (check SYSTEM_URL and API_KEY): %w", c.Cfg.URL, err)
	}
	var user samoUser
	if err := util.ParseResp(body, &user); err != nil {
		return err
	}
	isAdmin := strings.EqualFold(user.Role, "admin")
	slog.Info("[samo] authenticated", "user", user.Username, "role", user.Role)
	if !isAdmin {
		slog.Warn("[samo] token is not an admin token — library scans and explo status are unavailable, falling back on the SLEEP wait")
	}
	return nil
}

// GetLibrary resolves which library to rescan. Prefers an exact LIBRARY_NAME
// match so a server with several music libraries can be aimed precisely, and
// otherwise takes the first music-kind library.
func (c *Samo) GetLibrary() error {
	body, err := c.request("GET", "/libraries", nil)
	if err != nil {
		return fmt.Errorf("could not list libraries: %w", err)
	}
	var list samoLibraryList
	if err := util.ParseResp(body, &list); err != nil {
		return err
	}
	for _, library := range list.Items {
		if strings.EqualFold(library.Name, c.Cfg.LibraryName) {
			c.libraryID = library.ID
			slog.Debug("[samo] matched library by name", "name", library.Name, "id", library.ID)
			return c.loadExploConfig()
		}
	}
	for _, library := range list.Items {
		if library.Kind == "music" {
			c.libraryID = library.ID
			slog.Debug("[samo] using music library", "name", library.Name, "id", library.ID)
			return c.loadExploConfig()
		}
	}
	return fmt.Errorf("no music library found on %s", c.Cfg.URL)
}

// loadExploConfig asks whether the server is running its own drop-folder
// pipeline. A non-admin token gets a 403 here; that is not fatal, it just means
// we assume the server is not managing the folder and build the playlist
// ourselves.
func (c *Samo) loadExploConfig() error {
	body, err := c.request("GET", "/explo/config", nil)
	if err != nil {
		slog.Debug("[samo] could not read explo config, assuming server-side explo is off", "err", err.Error())
		return nil
	}
	var cfg samoExploConfig
	if err := util.ParseResp(body, &cfg); err != nil {
		return nil
	}
	c.serverExplo = cfg.Enabled && cfg.Configured
	c.serverExploDir = cfg.Folder
	if c.serverExplo {
		slog.Info("[samo] server-side explo folder is active; samo will identify and build the playlist itself", "folder", cfg.Folder)
	}
	return nil
}

func (c *Samo) AddLibrary() error {
	return nil
}

// SearchSongs resolves each track to a samo track id. It is called twice per
// run: once before downloading, so --download-mode=normal can skip what the
// library already has, and once after the rescan to pick up the new arrivals.
func (c *Samo) SearchSongs(tracks []*models.Track) error {
	for _, track := range tracks {
		query := fmt.Sprintf("%s %s", util.CleanSearchTitle(track.CleanTitle), track.MainArtist)
		found, err := c.searchTracks(query)
		if err != nil {
			return err
		}
		if len(found) == 0 && track.MusicBrainzTrackID != "" {
			slog.Debug("[samo] using fallback MB TrackID search", "mbid", track.MusicBrainzTrackID)
			if found, err = c.searchTracks(track.MusicBrainzTrackID); err != nil {
				return err
			}
		}
		if len(found) == 0 {
			slog.Debug("[samo] no results", "query", query)
			continue
		}
		c.matchTrack(track, found)
		if !track.Present {
			slog.Debug("[samo] no matching track", "query", query)
		}
	}
	return nil
}

func (c *Samo) searchTracks(query string) ([]samoTrack, error) {
	path := fmt.Sprintf("/music/search?query=%s&limit=25", url.QueryEscape(query))
	body, err := c.request("GET", path, nil)
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}
	var result samoSearchResult
	if err := util.ParseResp(body, &result); err != nil {
		return nil, err
	}
	return result.Tracks, nil
}

// matchTrack mirrors the Subsonic client's acceptance rules so a library that
// satisfies one client satisfies the other: an exact MusicBrainz id, or a
// normalized title plus a corroborating album or artist. The path+duration rule
// is the fallback for a freshly downloaded file whose tags are still whatever
// the downloader wrote.
func (c *Samo) matchTrack(track *models.Track, candidates []samoTrack) {
	normalizedTitle := util.NormalizeTitle(track.CleanTitle)
	for _, candidate := range candidates {
		artist := candidate.DisplayArtist
		if artist == "" && len(candidate.ArtistNames) > 0 {
			artist = strings.Join(candidate.ArtistNames, " ")
		}

		musicBrainzMatch := track.MusicBrainzTrackID != "" &&
			(candidate.ExternalIDs.MusicBrainzTrackID == track.MusicBrainzTrackID ||
				candidate.ExternalIDs.MusicBrainzRecordingID == track.MusicBrainzTrackID)
		titleMatch := util.NormalizeTitle(candidate.Title) == normalizedTitle
		artistMatch := util.ContainsFold(artist, track.MainArtist)
		albumMatch := util.ContainsFold(candidate.AlbumTitle, track.Album)

		if musicBrainzMatch || (titleMatch && (albumMatch || artistMatch)) {
			track.ID = candidate.ID
			track.Present = true
			return
		}

		if track.File == "" {
			continue
		}
		durationMatch := util.Abs(candidate.DurationSeconds-(track.Duration/1000)) < 10
		for _, file := range candidate.AudioFiles {
			if durationMatch && util.ContainsFold(file.Path, track.File) {
				track.ID = candidate.ID
				track.Present = true
				return
			}
		}
	}
}

// RefreshLibrary asks samo to pick up what was just downloaded. When the server
// runs its own explo folder it tells us where that folder is, and a subpath
// scan of just that folder finishes in seconds instead of walking the library.
func (c *Samo) RefreshLibrary() error {
	if c.libraryID == "" {
		return fmt.Errorf("no library resolved; cannot trigger a scan")
	}
	payload := map[string]any{"mode": "quick"}
	if c.serverExploDir != "" {
		payload["subpaths"] = []string{c.serverExploDir}
	}
	if _, err := c.request("POST", "/libraries/"+url.PathEscape(c.libraryID)+"/scan", payload); err != nil {
		return err
	}
	return nil
}

// CheckRefreshState blocks until the newest scan job leaves a running state.
// Returning false hands control back to the SLEEP fallback, which is the right
// answer for a non-admin token that cannot read scan jobs at all.
func (c *Samo) CheckRefreshState() bool {
	deadline := time.Now().Add(15 * time.Minute)
	for {
		body, err := c.request("GET", "/scan/jobs?limit=1", nil)
		if err != nil {
			slog.Debug("[samo] could not read scan jobs", "err", err.Error())
			return false
		}
		var jobs samoScanJobList
		if err := util.ParseResp(body, &jobs); err != nil {
			return false
		}
		if len(jobs.Items) == 0 {
			return true
		}
		switch jobs.Items[0].Status {
		case "completed", "failed", "cancelled", "canceled":
			return true
		}
		if time.Now().After(deadline) {
			slog.Warn("[samo] scan still running after 15m, continuing anyway", "status", jobs.Items[0].Status)
			return true
		}
		slog.Debug("[samo] library scan still ongoing", "status", jobs.Items[0].Status)
		time.Sleep(10 * time.Second)
	}
}

// CreatePlaylist defers to the server when the server is already doing this.
//
// samo's own explo pipeline fingerprints every file in the drop folder with
// AcoustID, applies real metadata and cover art, hides the drop from Recently
// Added, and re-derives its system "Explore" playlist from the folder on every
// pass. Creating a second playlist here would leave two near-identical
// playlists, one of which the server rewrites and the other of which goes
// stale. So when that pipeline is on, downloading IS the integration.
func (c *Samo) CreatePlaylist(tracks []*models.Track) error {
	if c.serverExplo {
		c.logExploStatus(tracks)
		return nil
	}

	trackIDs := make([]string, 0, len(tracks))
	for _, track := range tracks {
		if track.Present && track.ID != "" {
			trackIDs = append(trackIDs, track.ID)
		}
	}
	if len(trackIDs) == 0 {
		return fmt.Errorf("none of the %d track(s) resolved to a samo track id", len(tracks))
	}

	body, err := c.request("POST", "/music/playlists", map[string]any{
		"description": c.Cfg.PlaylistDescr,
		"name":        c.Cfg.PlaylistName,
		"public":      c.Cfg.PublicPlaylist,
		"trackIds":    trackIDs,
	})
	if err != nil {
		return err
	}
	var playlist samoPlaylist
	if err := util.ParseResp(body, &playlist); err != nil {
		return err
	}
	c.playlistID = playlist.ID
	slog.Info("[samo] playlist created", "name", playlist.Name, "tracks", len(trackIDs))
	return nil
}

// logExploStatus reports what the server made of the drop, so a run that hands
// off to server-side explo still says something more useful than "done".
//
// The track count here is RESOLVED tracks, not downloads. Explo marks a
// recommendation you already own as Present during CheckTracks, skips it in the
// download loop, and keeps it in the slice — so this number is "already in your
// library" plus "newly fetched", and calling it "downloaded" sent an earlier
// version of this straight past a run where every single download had failed.
func (c *Samo) logExploStatus(tracks []*models.Track) {
	body, err := c.request("GET", "/explo/tracks?limit=1", nil)
	if err != nil {
		slog.Info("[samo] handed off to samo's explo pipeline", "resolved", len(tracks))
		return
	}
	var status samoExploTracks
	if err := util.ParseResp(body, &status); err != nil {
		return
	}
	slog.Info("[samo] handed off to samo's explo pipeline; it builds the Explore playlist itself",
		"resolved", len(tracks),
		"inFolder", status.Summary.InFolder,
		"identified", status.Summary.Identified)
	if status.Summary.InFolder != 0 {
		return
	}
	// Nothing is under samo's explo folder. Two very different causes, and
	// naming the wrong one costs an evening: either nothing new was fetched
	// this run, or files were written somewhere samo is not looking.
	slog.Warn("[samo] samo sees 0 tracks under its explo folder")
	slog.Warn("[samo] if the log above shows download failures, that is the cause — nothing new reached the folder")
	slog.Warn("[samo] otherwise check that DOWNLOAD_DIR lands on the same directory SAMO_EXPLO_DIRS names",
		"samoWatches", c.serverExploDir)
}

// SearchPlaylist finds the playlist a previous run created. System playlists
// are skipped: the server re-derives those and refuses writes to them with a
// 403, so adopting one here would only produce a confusing failure on delete.
func (c *Samo) SearchPlaylist() error {
	if c.serverExplo {
		return nil
	}
	body, err := c.request("GET", "/music/playlists?limit=500", nil)
	if err != nil {
		return err
	}
	var list samoPlaylistList
	if err := util.ParseResp(body, &list); err != nil {
		return err
	}
	for _, playlist := range list.Items {
		if playlist.System {
			continue
		}
		if playlist.Name == c.Cfg.PlaylistName {
			c.playlistID = playlist.ID
			return nil
		}
	}
	return nil
}

func (c *Samo) UpdatePlaylist() error {
	if c.serverExplo || c.playlistID == "" {
		return nil
	}
	_, err := c.request("PATCH", "/music/playlists/"+url.PathEscape(c.playlistID), map[string]any{
		"description": c.Cfg.PlaylistDescr,
		"public":      c.Cfg.PublicPlaylist,
	})
	return err
}

func (c *Samo) DeletePlaylist() error {
	if c.serverExplo {
		slog.Debug("[samo] server-managed Explore playlist is re-derived, nothing to delete")
		return nil
	}
	if c.playlistID == "" {
		return fmt.Errorf("playlist not found")
	}
	if _, err := c.request("DELETE", "/music/playlists/"+url.PathEscape(c.playlistID), nil); err != nil {
		return err
	}
	c.playlistID = ""
	return nil
}
