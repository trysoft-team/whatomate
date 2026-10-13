package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	tableHeader = regexp.MustCompile(`^\s*\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`)
	portLine    = regexp.MustCompile(`^(\s*)port\s*=\s*([^#]*?)(\s*#.*)?$`)
)

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

// serverPortLine finds the [server] table header and its port line. Either
// index is -1 when missing.
func serverPortLine(lines []string) (header, port int) {
	header, port = -1, -1
	inServer := false
	for i, line := range lines {
		body, _ := lineEnding(line)
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
	}
	return header, port
}

// SetServerPort sets port in the [server] table of the TOML file at path and
// keeps every other byte. It adds the key, or the table, when missing.
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
	header, at := serverPortLine(lines)
	switch {
	case at >= 0:
		body, end := lineEnding(lines[at])
		m := portLine.FindStringSubmatch(body)
		lines[at] = m[1] + "port = " + value + m[3] + end
	case header >= 0:
		lines = append(lines[:header+1], append([]string{"port = " + value + nl}, lines[header+1:]...)...)
	default:
		if n := len(lines); n > 0 {
			if _, end := lineEnding(lines[n-1]); end == "" {
				lines[n-1] += nl
			}
		}
		lines = append(lines, nl, "[server]"+nl, "port = "+value+nl)
	}
	return WriteFileAtomic(path, []byte(strings.Join(lines, "")), fi.Mode().Perm())
}

// ReadServerPort returns [server] port from the TOML file at path, or
// DefaultPort when the file doesn't set it.
func ReadServerPort(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	lines := splitLines(string(raw))
	_, at := serverPortLine(lines)
	if at < 0 {
		return DefaultPort, nil
	}
	body, _ := lineEnding(lines[at])
	m := portLine.FindStringSubmatch(body)
	port, err := strconv.Atoi(strings.TrimSpace(m[2]))
	if err != nil {
		return 0, fmt.Errorf("[server] port in %s is not a number", path)
	}
	return port, nil
}
