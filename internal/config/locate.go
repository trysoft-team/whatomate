package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ConfigEnvVar names the environment variable that points at a config file.
const ConfigEnvVar = "WHATOMATE_CONFIG"

const configFileName = "config.toml"

// Source is the config file Locate chose.
type Source struct {
	Path string
	// BaseDir is where relative paths in the config resolve. Empty means the
	// working directory (an explicit -config, WHATOMATE_CONFIG or
	// ./config.toml); otherwise the user config folder or the system data
	// folder.
	BaseDir string
}

type candidate struct {
	path, base string
}

// locateEnv is the environment Locate reads; tests substitute it.
type locateEnv struct {
	goos      string
	getenv    func(string) string
	cwd, home string
	// root prefixes absolute paths so tests can stand in for / and C:\.
	root string
}

// Locate finds the config file: flagPath if set, else $WHATOMATE_CONFIG,
// else ./config.toml, else the per-user config folder, else the system
// config folder. It fails, naming every path it tried, when none exists.
func Locate(flagPath string) (Source, error) {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return locate(flagPath, locateEnv{goos: runtime.GOOS, getenv: os.Getenv, cwd: cwd, home: home})
}

func locate(flagPath string, e locateEnv) (Source, error) {
	if flagPath != "" {
		if !isFile(flagPath) {
			return Source{}, fmt.Errorf("config file %s not found", flagPath)
		}
		return Source{Path: flagPath}, nil
	}
	if p := e.getenv(ConfigEnvVar); p != "" {
		p = e.abs(p)
		if !isFile(p) {
			return Source{}, fmt.Errorf("config file %s (from %s) not found", p, ConfigEnvVar)
		}
		return Source{Path: p}, nil
	}

	cands := append([]candidate{{path: filepath.Join(e.cwd, configFileName)}}, e.userCandidates()...)
	cands = append(cands, e.systemCandidates()...)
	searched := make([]string, 0, len(cands))
	for _, c := range cands {
		if isFile(c.path) {
			return Source{Path: c.path, BaseDir: c.base}, nil
		}
		searched = append(searched, c.path)
	}
	return Source{}, errors.New("no config file found; looked in:\n  " + strings.Join(searched, "\n  ") +
		"\npass -config <path>, set " + ConfigEnvVar + ", or create one of these files from config.example.toml")
}

func (e locateEnv) userCandidates() []candidate {
	var dirs []string
	switch e.goos {
	case "windows":
		if d := e.getenv("APPDATA"); d != "" {
			dirs = append(dirs, e.abs(d))
		}
	case "darwin":
		dirs = append(dirs, filepath.Join(e.home, "Library", "Application Support"), e.xdgConfigHome())
	default:
		dirs = append(dirs, e.xdgConfigHome())
	}
	out := make([]candidate, 0, len(dirs))
	for _, d := range dirs {
		dir := filepath.Join(d, "whatomate")
		out = append(out, candidate{path: filepath.Join(dir, configFileName), base: dir})
	}
	return out
}

func (e locateEnv) systemCandidates() []candidate {
	switch e.goos {
	case "windows":
		pd := e.getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		dir := filepath.Join(e.abs(pd), "whatomate")
		return []candidate{{path: filepath.Join(dir, configFileName), base: dir}}
	case "darwin":
		var out []candidate
		for _, prefix := range []string{"/opt/homebrew", "/usr/local"} {
			out = append(out, candidate{
				path: e.abs(filepath.Join(prefix, "etc", "whatomate", configFileName)),
				base: e.abs(filepath.Join(prefix, "var", "whatomate")),
			})
		}
		return out
	default:
		return []candidate{{
			path: e.abs("/etc/whatomate/" + configFileName),
			base: e.abs("/var/lib/whatomate"),
		}}
	}
}

func (e locateEnv) xdgConfigHome() string {
	if d := e.getenv("XDG_CONFIG_HOME"); d != "" {
		return e.abs(d)
	}
	return filepath.Join(e.home, ".config")
}

// abs returns p under root when absolute, otherwise under the working directory.
func (e locateEnv) abs(p string) string {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return filepath.Join(e.root, p)
	}
	return filepath.Join(e.cwd, p)
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// ResolvePaths makes relative file paths in the config relative to base.
// An empty base leaves them relative to the working directory. Hold music and
// ringback files stay relative to audio_dir, and bare command names (no path
// separator) are left for a PATH lookup.
func (c *Config) ResolvePaths(base string) {
	if base == "" {
		return
	}
	join := func(p *string) {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	joinIfPath := func(p *string) {
		if strings.ContainsAny(*p, `/\`) {
			join(p)
		}
	}
	join(&c.Database.Path)
	join(&c.Storage.LocalPath)
	join(&c.Calling.AudioDir)
	join(&c.TTS.PiperModel)
	joinIfPath(&c.TTS.PiperBinary)
	joinIfPath(&c.TTS.OpusencBinary)
}
