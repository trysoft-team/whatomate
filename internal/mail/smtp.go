package mail

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
)

// SMTPConfig holds outbound mail server settings. Password must already be
// decrypted by the caller.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	Security string // "tls" (STARTTLS), "ssl" (implicit TLS), or "none"
}

// Message is a simple HTML email.
type Message struct {
	From    string
	To      []string
	Subject string
	HTML    string
}

// Send delivers msg using cfg. Empty From falls back to cfg.From.
func Send(cfg SMTPConfig, msg Message) error {
	if cfg.Host == "" {
		return fmt.Errorf("smtp host not configured")
	}
	if cfg.Port <= 0 {
		cfg.Port = 587
	}
	from := strings.TrimSpace(msg.From)
	if from == "" {
		from = strings.TrimSpace(cfg.From)
	}
	if from == "" {
		return fmt.Errorf("smtp from address required")
	}
	if len(msg.To) == 0 {
		return fmt.Errorf("no recipients")
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	header := strings.Builder{}
	fmt.Fprintf(&header, "From: %s\r\n", from)
	fmt.Fprintf(&header, "To: %s\r\n", strings.Join(msg.To, ", "))
	fmt.Fprintf(&header, "Subject: %s\r\n", msg.Subject)
	header.WriteString("MIME-Version: 1.0\r\n")
	header.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	header.WriteString("\r\n")
	header.WriteString(msg.HTML)
	body := []byte(header.String())

	security := strings.ToLower(strings.TrimSpace(cfg.Security))
	if security == "" {
		security = "tls"
	}

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	switch security {
	case "ssl":
		return sendSSL(addr, cfg.Host, auth, from, msg.To, body)
	case "none":
		return smtp.SendMail(addr, auth, from, msg.To, body)
	default: // tls / starttls
		return sendSTARTTLS(addr, cfg.Host, auth, from, msg.To, body)
	}
}

func sendSTARTTLS(addr, host string, auth smtp.Auth, from string, to []string, body []byte) error {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
	}
	return writeMessage(c, from, to, body)
}

func sendSSL(addr, host string, auth smtp.Auth, from string, to []string, body []byte) error {
	tlsConn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	defer func() { _ = tlsConn.Close() }()

	c, err := smtp.NewClient(tlsConn, host)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
	}
	return writeMessage(c, from, to, body)
}

func writeMessage(c *smtp.Client, from string, to []string, body []byte) error {
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// SplitAddresses splits a comma/semicolon-separated recipient list.
func SplitAddresses(s string) []string {
	s = strings.ReplaceAll(s, ";", ",")
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
