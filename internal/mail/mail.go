// Package mail sends password-reset email over SMTP.
package mail

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	netmail "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"fedigest/internal/config"
)

// Send delivers a plain-text email via the configured SMTP server. Port
// 465 uses TLS from the start; any other port upgrades with STARTTLS when
// the server offers it. Credentials are only ever sent over TLS (or to
// localhost).
func Send(cfg *config.Config, to, subject, body string) error {
	if !cfg.EmailEnabled() {
		return errors.New("SMTP is not configured")
	}
	fromHeader := cfg.SMTPFrom
	if fromHeader == "" {
		fromHeader = cfg.SMTPUser
	}
	from, err := netmail.ParseAddress(fromHeader)
	if err != nil {
		return fmt.Errorf("SMTP_FROM %q isn't an email address: %w", fromHeader, err)
	}
	rcpt, err := netmail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("recipient %q isn't an email address: %w", to, err)
	}

	addr := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort))
	tlsConfig := &tls.Config{ServerName: cfg.SMTPHost}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if cfg.SMTPPort == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(time.Minute))
	c, err := smtp.NewClient(conn, cfg.SMTPHost)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok && cfg.SMTPPort != 465 {
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if cfg.SMTPUser != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("the SMTP server doesn't offer login (AUTH)")
		}
		if err := c.Auth(smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPHost)); err != nil {
			return err
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(rcpt.Address); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(message(from, rcpt, subject, body))); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// message builds the email: headers, then the plain-text body.
func message(from, to *netmail.Address, subject, body string) string {
	id := make([]byte, 12)
	rand.Read(id)
	domain := "localhost"
	if i := strings.LastIndex(from.Address, "@"); i >= 0 {
		domain = from.Address[i+1:]
	}
	var msg strings.Builder
	fmt.Fprintf(&msg, "From: %s\r\n", from.String())
	fmt.Fprintf(&msg, "To: %s\r\n", to.String())
	fmt.Fprintf(&msg, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&msg, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&msg, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(id), domain)
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	msg.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)
	return msg.String()
}
