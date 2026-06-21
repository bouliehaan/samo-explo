package app

import(
	"path/filepath"
)

func (c Config) CacheDir() string {
    return filepath.Join(c.WebDataDir, "cache")
}

func (c Config) CoversDir() string {
    return filepath.Join(c.CacheDir(), "covers")
}

func (c Config) LogsDir() string {
    return filepath.Join(c.WebDataDir, "logs")
}