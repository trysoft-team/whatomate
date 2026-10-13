package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// ConfigEnvVar names the environment variable that points at a config file.
const ConfigEnvVar = "WHATOMATE_CONFIG"

const configFileName = "config.toml"

// ErrNotFound matches (errors.Is) the error Locate returns when no config
// file exists, as opposed to one that exists but can't be read.
var ErrNotFound = errors.New("config file not found")

type notFoundError struct{ msg string }

func (e *notFoundError) Error() string        { return e.msg }
func (e *notFoundError) Is(target error) bool { return target == ErrNotFound }

// Source is the config file Locate chose.
type Source struct {
	Path string
	// BaseDir is where relative paths in the config resolve. Empty means the
	// working directory (an explicit -config or WHATOMATE_CONFIG); otherwise
	// the user config folder or the system data folder.
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
// else the per-user config folder, else the system config folder. The
// working directory is not searched. It fails, naming every path it tried,
// when none exists.
func Locate(flagPath string) (Source, error) {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return locate(flagPath, locateEnv{goos: runtime.GOOS, getenv: os.Getenv, cwd: cwd, home: home})
}

func locate(flagPath string, e locateEnv) (Source, error) {
	if flagPath != "" {
		ok, err := isExplicitFile(flagPath)
		if err != nil {
			return Source{}, err
		}
		if !ok {
			return Source{}, &notFoundError{fmt.Sprintf("config file %s not found", flagPath)}
		}
		return Source{Path: flagPath}, nil
	}
	if p := e.getenv(ConfigEnvVar); p != "" {
		p = e.abs(p)
		ok, err := isExplicitFile(p)
		if err != nil {
			return Source{}, err
		}
		if !ok {
			return Source{}, &notFoundError{fmt.Sprintf("config file %s (from %s) not found", p, ConfigEnvVar)}
		}
		return Source{Path: p}, nil
	}

	cands := append(e.userCandidates(), e.systemCandidates()...)
	searched := make([]string, 0, len(cands))
	var unchecked error
	for _, c := range cands {
		ok, err := isFile(c.path)
		if ok {
			return Source{Path: c.path, BaseDir: c.base}, nil
		}
		if err != nil && unchecked == nil {
			unchecked = err
		}
		searched = append(searched, c.path)
	}
	// A path that couldn't be checked may hold a config, so this isn't ErrNotFound.
	if unchecked != nil {
		return Source{}, unchecked
	}
	return Source{}, &notFoundError{"no config file found; looked in:\n  " + strings.Join(searched, "\n  ") +
		"\npass -config <path>, set " + ConfigEnvVar + ", or create one of these files from config.example.toml"}
}

// SetupTarget lists where first-run setup may write a config file.
type SetupTarget struct {
	// Fixed is the file named by -config or WHATOMATE_CONFIG; setup writes
	// exactly there and relative paths stay relative to the working directory.
	Fixed string
	// User and System are the per-user and system locations, used when Fixed
	// is empty. BaseDir is each one's data folder.
	User, System []Source
}

// Target returns where setup may write the config file for flagPath.
func Target(flagPath string) SetupTarget {
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	return target(flagPath, locateEnv{goos: runtime.GOOS, getenv: os.Getenv, cwd: cwd, home: home})
}

func target(flagPath string, e locateEnv) SetupTarget {
	if flagPath != "" {
		if !filepath.IsAbs(flagPath) {
			flagPath = filepath.Join(e.cwd, flagPath)
		}
		return SetupTarget{Fixed: flagPath}
	}
	if p := e.getenv(ConfigEnvVar); p != "" {
		return SetupTarget{Fixed: e.abs(p)}
	}
	toSources := func(cs []candidate) []Source {
		out := make([]Source, 0, len(cs))
		for _, c := range cs {
			out = append(out, Source{Path: c.path, BaseDir: c.base})
		}
		return out
	}
	return SetupTarget{User: toSources(e.userCandidates()), System: toSources(e.systemCandidates())}
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

// isFile reports whether p is a file. It returns an error only when p's
// existence can't be checked (e.g. permission denied).
func isFile(p string) (bool, error) {
	fi, err := os.Stat(p)
	switch {
	case err == nil:
		return !fi.IsDir(), nil
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return false, nil
	}
	return false, fmt.Errorf("checking config file: %w", err)
}

// isExplicitFile is isFile for a path the operator named. A directory there
// is an error, not a missing file: Docker creates one when a bind-mounted
// config file is missing on the host, and setup can't write over it.
func isExplicitFile(p string) (bool, error) {
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return false, fmt.Errorf("config path %s is a directory, not a file", p)
	}
	return isFile(p)
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
	join(&c.Backup.Dir)
	join(&c.TTS.PiperModel)
	joinIfPath(&c.TTS.PiperBinary)
	joinIfPath(&c.TTS.OpusencBinary)
}
