package setup

import (
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"time"

	"github.com/valyala/fasthttp"
)

// openBrowser asks the desktop to open url.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// listenURLs returns the localhost URL first, then one per LAN IPv4 address.
func listenURLs(port int) []string {
	urls := []string{fmt.Sprintf("http://localhost:%d", port)}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() || ipn.IP.To4() == nil {
			continue
		}
		urls = append(urls, fmt.Sprintf("http://%s:%d", ipn.IP, port))
	}
	return urls
}

// serveMoved answers on the old setup port for grace, pointing at url.
func serveMoved(ln net.Listener, url string, grace time.Duration) {
	srv := &fasthttp.Server{Name: "Whatomate setup", Handler: func(ctx *fasthttp.RequestCtx) {
		if string(ctx.Path()) == "/api/setup/status" {
			writeData(ctx, statusResponse{Ready: true, URL: url})
			return
		}
		ctx.Redirect(url+string(ctx.RequestURI()), fasthttp.StatusFound)
	}}
	go func() { _ = srv.Serve(ln) }()
	time.AfterFunc(grace, func() { _ = srv.Shutdown() })
}
