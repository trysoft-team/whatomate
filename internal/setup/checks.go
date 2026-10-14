package setup

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/trysoft/whatomate/internal/netutil"
)

// Status is a check outcome. Fail blocks setup; Warn does not.
type Status string

const (
	Pass Status = "pass"
	Warn Status = "warn"
	Fail Status = "fail"
)

const (
	minFreeDisk   = 1 << 30   // 1 GB
	minMemory     = 512 << 20 // 512 MB
	maxClockDrift = 2 * time.Minute
)

// CheckResult is one line of the system check.
type CheckResult struct {
	ID     string `json:"id"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// CheckInput is what the user chose so far.
type CheckInput struct {
	Port, ListenPort   int
	ConfigDir, DataDir string
}

// System reads machine facts; tests substitute it.
type System interface {
	TotalMemory() (uint64, error)
	FreeDisk(path string) (uint64, error)
}

// Checker runs the system checks.
type Checker struct {
	Version  string
	System   System
	HTTP     *http.Client
	ProbeURL string
	LookPath func(string) (string, error)
	Now      func() time.Time
	PortFree func(int) bool
}

// NewChecker returns a Checker for this machine.
func NewChecker(version string) *Checker {
	return &Checker{
		Version:  version,
		System:   hostSystem{},
		HTTP:     &http.Client{Timeout: 5 * time.Second},
		ProbeURL: "https://graph.facebook.com/",
		LookPath: exec.LookPath,
		Now:      time.Now,
		PortFree: netutil.PortFree,
	}
}

// Run returns every check, in display order.
func (c *Checker) Run(ctx context.Context, in CheckInput) []CheckResult {
	out := []CheckResult{
		{ID: "system", Status: Pass, Detail: fmt.Sprintf("%s/%s, whatomate %s", runtime.GOOS, runtime.GOARCH, c.Version)},
		c.folders(in),
		c.disk(in.DataDir),
		c.memory(),
		c.port(in),
	}
	internet, clock := c.internet(ctx)
	return append(out, internet, clock, c.audio())
}

func (c *Checker) folders(in CheckInput) CheckResult {
	dirs := []string{in.ConfigDir}
	if in.DataDir != in.ConfigDir {
		dirs = append(dirs, in.DataDir)
	}
	var bad []string
	for _, d := range dirs {
		if !Writable(d) {
			bad = append(bad, d)
		}
	}
	if len(bad) > 0 {
		return CheckResult{ID: "folders", Status: Fail, Detail: "not writable: " + strings.Join(bad, ", ")}
	}
	return CheckResult{ID: "folders", Status: Pass, Detail: strings.Join(dirs, ", ")}
}

func (c *Checker) disk(dir string) CheckResult {
	free, err := c.System.FreeDisk(dir)
	switch {
	case err != nil:
		return CheckResult{ID: "disk", Status: Warn, Detail: "could not read free space: " + err.Error()}
	case free < minFreeDisk:
		return CheckResult{ID: "disk", Status: Warn, Detail: fmt.Sprintf("%s free; at least 1 GB recommended", size(free))}
	}
	return CheckResult{ID: "disk", Status: Pass, Detail: size(free) + " free"}
}

func (c *Checker) memory() CheckResult {
	total, err := c.System.TotalMemory()
	switch {
	case err != nil:
		return CheckResult{ID: "memory", Status: Warn, Detail: "could not read memory: " + err.Error()}
	case total < minMemory:
		return CheckResult{ID: "memory", Status: Warn, Detail: fmt.Sprintf("%s RAM; at least 512 MB recommended", size(total))}
	}
	return CheckResult{ID: "memory", Status: Pass, Detail: size(total) + " RAM"}
}

func (c *Checker) port(in CheckInput) CheckResult {
	switch {
	case in.Port < 1 || in.Port > 65535:
		return CheckResult{ID: "port", Status: Fail, Detail: "port must be between 1 and 65535"}
	case in.Port == in.ListenPort:
		return CheckResult{ID: "port", Status: Pass, Detail: fmt.Sprintf("%d (setup is using it)", in.Port)}
	case c.PortFree(in.Port):
		return CheckResult{ID: "port", Status: Pass, Detail: fmt.Sprintf("%d is free", in.Port)}
	}
	return CheckResult{ID: "port", Status: Fail, Detail: fmt.Sprintf("%d is in use by another program", in.Port)}
}

func (c *Checker) internet(ctx context.Context) (CheckResult, CheckResult) {
	host := c.ProbeURL
	if u, err := url.Parse(c.ProbeURL); err == nil {
		host = u.Host
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ProbeURL, nil)
	if err != nil {
		return CheckResult{ID: "internet", Status: Warn, Detail: err.Error()},
			CheckResult{ID: "clock", Status: Warn, Detail: "not checked (no internet)"}
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return CheckResult{ID: "internet", Status: Warn, Detail: "can't reach " + host + ": " + err.Error()},
			CheckResult{ID: "clock", Status: Warn, Detail: "not checked (no internet)"}
	}
	_ = resp.Body.Close()
	inet := CheckResult{ID: "internet", Status: Pass, Detail: host + " is reachable"}
	date, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return inet, CheckResult{ID: "clock", Status: Warn, Detail: "the server sent no time to compare"}
	}
	drift := c.Now().Sub(date)
	if drift < 0 {
		drift = -drift
	}
	if drift > maxClockDrift {
		return inet, CheckResult{ID: "clock", Status: Warn, Detail: "clock is off by " + drift.Round(time.Second).String()}
	}
	return inet, CheckResult{ID: "clock", Status: Pass, Detail: "within 2 minutes of internet time"}
}

func (c *Checker) audio() CheckResult {
	var missing []string
	for _, tool := range []string{"ffmpeg", "opusenc"} {
		if _, err := c.LookPath(tool); err != nil {
			missing = append(missing, tool)
		}
	}
	if len(missing) > 0 {
		return CheckResult{ID: "audio", Status: Warn, Detail: "not installed: " + strings.Join(missing, ", ") + " (calling hold music and TTS conversion won't work)"}
	}
	return CheckResult{ID: "audio", Status: Pass, Detail: "ffmpeg and opusenc found"}
}

func size(b uint64) string {
	const gb, mb = 1 << 30, 1 << 20
	if b >= gb {
		return fmt.Sprintf("%.1f GB", float64(b)/gb)
	}
	return fmt.Sprintf("%d MB", b/mb)
}
