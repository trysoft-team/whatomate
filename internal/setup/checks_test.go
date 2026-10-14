package setup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSystem struct {
	mem, disk uint64
	err       error
}

func (f fakeSystem) TotalMemory() (uint64, error)    { return f.mem, f.err }
func (f fakeSystem) FreeDisk(string) (uint64, error) { return f.disk, f.err }

func testChecker(t *testing.T, sys System, serverTime time.Time, missing ...string) *Checker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", serverTime.UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return &Checker{
		Version: "v-test", System: sys, HTTP: srv.Client(), ProbeURL: srv.URL,
		LookPath: func(name string) (string, error) {
			for _, m := range missing {
				if m == name {
					return "", errors.New("not found")
				}
			}
			return "/usr/bin/" + name, nil
		},
		Now:      func() time.Time { return time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC) },
		PortFree: func(port int) bool { return port != 5000 },
	}
}

func byID(results []CheckResult) map[string]CheckResult {
	out := map[string]CheckResult{}
	for _, r := range results {
		out[r.ID] = r
	}
	return out
}

func TestRun_AllGood(t *testing.T) {
	dir := t.TempDir()
	c := testChecker(t, fakeSystem{mem: 8 << 30, disk: 50 << 30}, time.Date(2026, 10, 13, 12, 0, 30, 0, time.UTC))
	res := c.Run(context.Background(), CheckInput{Port: 4747, ListenPort: 4747, ConfigDir: dir, DataDir: filepath.Join(dir, "data")})
	ids := make([]string, 0, len(res))
	for _, r := range res {
		ids = append(ids, r.ID)
		assert.Equal(t, Pass, r.Status, r.ID+": "+r.Detail)
	}
	assert.Equal(t, []string{"system", "folders", "disk", "memory", "port", "internet", "clock", "audio"}, ids)
	assert.Contains(t, byID(res)["system"].Detail, "v-test")
}

func TestRun_Warnings(t *testing.T) {
	dir := t.TempDir()
	c := testChecker(t, fakeSystem{mem: 256 << 20, disk: 100 << 20}, time.Date(2026, 10, 13, 12, 10, 0, 0, time.UTC), "ffmpeg")
	r := byID(c.Run(context.Background(), CheckInput{Port: 4747, ListenPort: 4747, ConfigDir: dir, DataDir: dir}))
	assert.Equal(t, Warn, r["disk"].Status)
	assert.Equal(t, Warn, r["memory"].Status)
	assert.Equal(t, Warn, r["clock"].Status)
	assert.Contains(t, r["clock"].Detail, "10m0s")
	assert.Equal(t, Warn, r["audio"].Status)
	assert.Contains(t, r["audio"].Detail, "ffmpeg")
	assert.NotContains(t, r["audio"].Detail, "opusenc")
}

func TestRun_UnknownSystemValuesWarn(t *testing.T) {
	dir := t.TempDir()
	c := testChecker(t, fakeSystem{err: errors.New("nope")}, time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))
	r := byID(c.Run(context.Background(), CheckInput{Port: 4747, ListenPort: 4747, ConfigDir: dir, DataDir: dir}))
	assert.Equal(t, Warn, r["disk"].Status)
	assert.Equal(t, Warn, r["memory"].Status)
}

func TestRun_Port(t *testing.T) {
	dir := t.TempDir()
	c := testChecker(t, fakeSystem{mem: 8 << 30, disk: 50 << 30}, time.Now())
	run := func(port int) CheckResult {
		return byID(c.Run(context.Background(), CheckInput{Port: port, ListenPort: 4747, ConfigDir: dir, DataDir: dir}))["port"]
	}
	assert.Equal(t, Pass, run(4747).Status, "the port setup is using")
	assert.Equal(t, Pass, run(6000).Status)
	assert.Equal(t, Fail, run(5000).Status, "in use")
	assert.Equal(t, Fail, run(0).Status)
	assert.Equal(t, Fail, run(70000).Status)
}

func TestRun_NoInternet(t *testing.T) {
	dir := t.TempDir()
	c := testChecker(t, fakeSystem{mem: 8 << 30, disk: 50 << 30}, time.Now())
	c.ProbeURL = "http://127.0.0.1:1"
	r := byID(c.Run(context.Background(), CheckInput{Port: 4747, ListenPort: 4747, ConfigDir: dir, DataDir: dir}))
	assert.Equal(t, Warn, r["internet"].Status)
	assert.Equal(t, Warn, r["clock"].Status)
}

func TestRun_FolderNotWritableFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	require.NoError(t, os.Mkdir(locked, 0o500))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	c := testChecker(t, fakeSystem{mem: 8 << 30, disk: 50 << 30}, time.Now())
	r := byID(c.Run(context.Background(), CheckInput{Port: 4747, ListenPort: 4747, ConfigDir: filepath.Join(locked, "whatomate"), DataDir: dir}))
	assert.Equal(t, Fail, r["folders"].Status)
	assert.Contains(t, r["folders"].Detail, locked)
}

func TestWritable_CreatesNothing(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a", "b")
	assert.True(t, Writable(target))
	_, err := os.Stat(filepath.Join(dir, "a"))
	assert.True(t, os.IsNotExist(err), "Writable must not create folders")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "Writable must not leave files")
}
