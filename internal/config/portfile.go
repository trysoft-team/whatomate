package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/knadh/koanf/parsers/toml"
)

var (
	tableHeader      = regexp.MustCompile(`^\s*\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`)
	arrayTableHeader = regexp.MustCompile(`^\s*\[\[.*\]\]\s*(#.*)?$`)
	portLine         = regexp.MustCompile(`^(\s*)port\s*=\s*([^#]*?)(\s*#.*)?$`)
	// originsLine matches allowed_origins as a single-line basic string
	// without escapes, so its raw text is its value.
	originsLine = regexp.MustCompile(`^(\s*allowed_origins\s*=\s*)"([^"\\]*)"(\s*(?:#.*)?)$`)
)

// ErrCannotEditPort is returned by SetServerPort when the file's layout
// keeps it from changing only [server] port (and the allowed_origins entries
// it moves along); the file is left untouched.
var ErrCannotEditPort = errors.New("can't change [server] port in this file automatically")

// WriteFileAtomic writes data to path through a temp file in the same folder
// and a rename, creating the folder (0750) if needed. When the folder can't
// take a temp file but path itself is writable, it rewrites path in place.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".whatomate-*.tmp")
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return os.WriteFile(path, data, perm)
		}
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// link is os.Link; tests replace it to stand in for file systems without
// hard links.
var link = os.Link

// WriteFileExclusive writes data to a new file at path, creating the folder
// (0750) if needed. It never replaces a file: when path exists it fails with
// an error matching fs.ErrExist. The content is written to a temp file in the
// same folder and hard-linked into place; without hard links it creates path
// with O_EXCL and writes it directly.
func WriteFileExclusive(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".whatomate-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := fill(tmp, data, perm); err != nil {
		return err
	}
	err = link(name, path)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if err := fill(f, data, perm); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// fill sets f's mode, writes data, syncs and closes f.
func fill(f *os.File, data []byte, perm os.FileMode) error {
	err := f.Chmod(perm)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// splitLines splits s into lines that keep their "\n" or "\r\n" endings.
func splitLines(s string) []string {
	var out []string
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}

func lineEnding(line string) (body, end string) {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return line[:len(line)-2], "\r\n"
	case strings.HasSuffix(line, "\n"):
		return line[:len(line)-1], "\n"
	}
	return line, ""
}

// serverPortLine finds the [server] table header, its port line and its
// allowed_origins line (see originsLine). Each index is -1 when missing.
func serverPortLine(lines []string) (header, port, origins int) {
	header, port, origins = -1, -1, -1
	inServer := false
	for i, line := range lines {
		body, _ := lineEnding(line)
		if arrayTableHeader.MatchString(body) {
			inServer = false
			continue
		}
		if m := tableHeader.FindStringSubmatch(body); m != nil {
			inServer = m[1] == "server"
			if inServer && header < 0 {
				header = i
			}
			continue
		}
		if inServer && port < 0 && portLine.MatchString(body) {
			port = i
		}
		if inServer && origins < 0 && originsLine.MatchString(body) {
			origins = i
		}
	}
	return header, port, origins
}

// portValue returns the port set on lines[at], a portLine, or DefaultPort
// when at is -1.
func portValue(lines []string, at int) (int, error) {
	if at < 0 {
		return DefaultPort, nil
	}
	body, _ := lineEnding(lines[at])
	return strconv.Atoi(strings.TrimSpace(portLine.FindStringSubmatch(body)[2]))
}

// moveOrigins moves the comma-separated origins in list that are exactly
// http(s)://host:from to port to, trims every entry and drops duplicates,
// keeping the first. It reports false, and list unchanged, when no entry is
// on port from.
func moveOrigins(list string, from, to int) (string, bool) {
	var out []string
	seen := map[string]bool{}
	moved := false
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if e, ok := moveOrigin(entry, from, to); ok {
			entry, moved = e, true
		}
		if !seen[entry] {
			seen[entry] = true
			out = append(out, entry)
		}
	}
	if !moved {
		return list, false
	}
	return strings.Join(out, ","), true
}

// moveOrigin returns origin with its port set to to when origin is an http or
// https URL with explicit port from and nothing after it. When to is the
// scheme's default port the port is omitted, as browsers do in Origin.
func moveOrigin(origin string, from, to int) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.User != nil ||
		u.Path != "" || u.ForceQuery || u.RawQuery != "" || strings.Contains(origin, "#") {
		return "", false
	}
	old := strconv.Itoa(from)
	if u.Port() != old || !strings.HasSuffix(origin, ":"+old) {
		return "", false
	}
	if (u.Scheme == "http" && to == 80) || (u.Scheme == "https" && to == 443) {
		return strings.TrimSuffix(origin, ":"+old), true
	}
	return strings.TrimSuffix(origin, old) + strconv.Itoa(to), true
}

// SetServerPort sets port in the [server] table of the TOML file at path and
// keeps every other byte. It adds the key, or the table, when missing. When
// [server] allowed_origins is a single-line basic string, entries that are
// http(s)://host:old-port (old port being the file's port or DefaultPort)
// move to the new port in the same write; origins are left alone when the
// file's [server] port isn't a plain integer. When the edit wouldn't leave a
// valid file differing only in those values, it returns ErrCannotEditPort
// and writes nothing.
func SetServerPort(path string, port int) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	lines := splitLines(string(raw))
	nl := "\n"
	if len(lines) > 0 {
		if _, end := lineEnding(lines[0]); end != "" {
			nl = end
		}
	}
	value := strconv.Itoa(port)
	header, at, originsAt := serverPortLine(lines)
	var origins *string
	if old, err := portValue(lines, at); err == nil && old != port && originsAt >= 0 {
		body, end := lineEnding(lines[originsAt])
		m := originsLine.FindStringSubmatch(body)
		if list, ok := moveOrigins(m[2], old, port); ok {
			lines[originsAt] = m[1] + `"` + list + `"` + m[3] + end
			origins = &list
		}
	}
	switch {
	case at >= 0:
		body, end := lineEnding(lines[at])
		m := portLine.FindStringSubmatch(body)
		lines[at] = m[1] + "port = " + value + m[3] + end
	case header >= 0:
		if _, end := lineEnding(lines[header]); end == "" {
			lines[header] += nl
		}
		lines = append(lines[:header+1], append([]string{"port = " + value + nl}, lines[header+1:]...)...)
	default:
		if n := len(lines); n > 0 {
			if _, end := lineEnding(lines[n-1]); end == "" {
				lines[n-1] += nl
			}
		}
		lines = append(lines, nl, "[server]"+nl, "port = "+value+nl)
	}
	edited := []byte(strings.Join(lines, ""))
	if !onlyServerEdits(raw, edited, port, origins) {
		return ErrCannotEditPort
	}
	return WriteFileAtomic(path, edited, fi.Mode().Perm())
}

// onlyServerEdits reports whether after parses, sets [server] port to port,
// sets [server] allowed_origins to *origins when origins is non-nil (and
// leaves it as it was otherwise), and decodes to the same values as before in
// everything else.
func onlyServerEdits(before, after []byte, port int, origins *string) bool {
	old, err := toml.Parser().Unmarshal(before)
	if err != nil {
		return false
	}
	upd, err := toml.Parser().Unmarshal(after)
	if err != nil {
		return false
	}
	srv, _ := upd["server"].(map[string]any)
	if srv == nil || srv["port"] != int64(port) {
		return false
	}
	keys := []string{"port"}
	if origins != nil {
		if srv["allowed_origins"] != *origins {
			return false
		}
		keys = append(keys, "allowed_origins")
	}
	return reflect.DeepEqual(withoutServerKeys(old, keys), withoutServerKeys(upd, keys))
}

// withoutServerKeys drops keys from doc's server table, and the table too
// when nothing else is left in it.
func withoutServerKeys(doc map[string]any, keys []string) map[string]any {
	srv, ok := doc["server"].(map[string]any)
	if !ok {
		return doc
	}
	for _, k := range keys {
		delete(srv, k)
	}
	if len(srv) == 0 {
		delete(doc, "server")
	}
	return doc
}

// ReadServerPort returns [server] port from the TOML file at path, or
// DefaultPort when the file doesn't set it.
func ReadServerPort(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	lines := splitLines(string(raw))
	_, at, _ := serverPortLine(lines)
	port, err := portValue(lines, at)
	if err != nil {
		return 0, fmt.Errorf("[server] port in %s is not a number", path)
	}
	return port, nil
}
