// Package mail sends transactional email over SMTP using only the
// standard library. Port 465 uses implicit TLS, anything else uses
// STARTTLS when the server offers it.
package mail

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"html/template"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"github.com/ps-wizard/revserp/internal/config"
)

// ErrNotConfigured is returned by Send when no SMTP host is configured.
var ErrNotConfigured = errors.New("mail: SMTP_HOST not configured")

// Mailer sends multipart/alternative email.
type Mailer struct {
	host     string
	port     int
	username string
	password string
	from     string
}

// New builds a Mailer from explicit settings.
func New(host string, port int, username, password, from string) *Mailer {
	return &Mailer{host: strings.TrimSpace(host), port: port, username: username, password: password, from: from}
}

// NewFromConfig builds a Mailer from application config.
func NewFromConfig(cfg config.Config) *Mailer {
	return New(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPFrom)
}

// Configured reports whether sending is possible.
func (m *Mailer) Configured() bool {
	return m != nil && m.host != ""
}

// Send delivers a multipart/alternative message (plain text + HTML).
func (m *Mailer) Send(to, subject, textBody, htmlBody string) error {
	if !m.Configured() {
		return ErrNotConfigured
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return errors.New("mail: empty recipient")
	}
	from := strings.TrimSpace(m.from)
	if from == "" {
		from = "RevSERP <noreply@revserp.ai>"
	}
	var buf bytes.Buffer
	if err := writeMessage(&buf, from, to, subject, textBody, htmlBody); err != nil {
		return err
	}

	addr := fmt.Sprintf("%s:%d", m.host, m.port)
	if m.port == 465 {
		return m.sendImplicitTLS(addr, from, []string{to}, buf.Bytes())
	}
	return m.sendSTARTTLS(addr, from, []string{to}, buf.Bytes())
}

func (m *Mailer) sendImplicitTLS(addr, from string, to []string, msg []byte) error {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return fmt.Errorf("mail: dial: %w", err)
	}
	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return fmt.Errorf("mail: new client: %w", err)
	}
	defer client.Close()
	return m.deliver(client, from, to, msg)
}

func (m *Mailer) sendSTARTTLS(addr, from string, to []string, msg []byte) error {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("mail: dial: %w", err)
	}
	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return fmt.Errorf("mail: new client: %w", err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	}
	return m.deliver(client, from, to, msg)
}

func (m *Mailer) deliver(client *smtp.Client, from string, to []string, msg []byte) error {
	if m.username != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("mail: server does not support auth")
		}
		if err := client.Auth(smtp.PlainAuth("", m.username, m.password, m.host)); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := client.Mail(fromAddress(from)); err != nil {
		return fmt.Errorf("mail: mail from: %w", err)
	}
	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("mail: rcpt to: %w", err)
		}
	}
	data, err := client.Data()
	if err != nil {
		return fmt.Errorf("mail: data: %w", err)
	}
	defer data.Close()
	if _, err := data.Write(msg); err != nil {
		return fmt.Errorf("mail: write: %w", err)
	}
	return nil
}

// fromAddress strips a display name, leaving the bare address for MAIL FROM.
func fromAddress(from string) string {
	if start, end := strings.Index(from, "<"), strings.Index(from, ">"); start >= 0 && end > start {
		return strings.TrimSpace(from[start+1 : end])
	}
	return strings.TrimSpace(from)
}

func writeMessage(buf *bytes.Buffer, from, to, subject, textBody, htmlBody string) error {
	writer := multipart.NewWriter(buf)
	header := textproto.MIMEHeader{}
	header.Set("From", from)
	header.Set("To", to)
	header.Set("Subject", subject)
	header.Set("MIME-Version", "1.0")
	header.Set("Content-Type", "multipart/alternative; boundary="+writer.Boundary())
	for key, values := range header {
		fmt.Fprintf(buf, "%s: %s\r\n", key, strings.Join(values, ", "))
	}
	fmt.Fprintf(buf, "\r\n")
	writePart := func(contentType, body string) error {
		partHeader := textproto.MIMEHeader{}
		partHeader.Set("Content-Type", contentType+"; charset=UTF-8")
		partHeader.Set("Content-Transfer-Encoding", "quoted-printable")
		part, err := writer.CreatePart(partHeader)
		if err != nil {
			return err
		}
		encoder := quotedprintable.NewWriter(part)
		if _, err := encoder.Write([]byte(body)); err != nil {
			return err
		}
		return encoder.Close()
	}
	if err := writePart("text/plain", textBody); err != nil {
		return err
	}
	if err := writePart("text/html", htmlBody); err != nil {
		return err
	}
	return writer.Close()
}

// baseLayout renders the shared single-card email shell: neutral-on-light,
// table-based for client safety, one heading, one button, plain URL fallback.
var baseLayout = template.Must(template.New("base").Parse(`<!DOCTYPE html>
<html><body style="margin:0;padding:0;background-color:#f4f4f2;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background-color:#f4f4f2;padding:32px 16px;">
<tr><td align="center">
<table role="presentation" width="480" cellpadding="0" cellspacing="0" style="background-color:#ffffff;border:1px solid #e8e6e1;border-radius:12px;padding:32px;font-family:Arial,Helvetica,sans-serif;">
<tr><td style="font-size:15px;font-weight:bold;letter-spacing:0.04em;color:#111111;">RevSERP</td></tr>
<tr><td style="padding-top:16px;font-size:20px;font-weight:bold;color:#111111;">{{.Heading}}</td></tr>
<tr><td style="padding-top:8px;font-size:14px;line-height:1.6;color:#444444;">{{.Body}}</td></tr>
{{if .CTAURL}}<tr><td align="center" style="padding-top:24px;">
<a href="{{.CTAURL}}" style="display:inline-block;background-color:#111111;color:#ffffff;font-size:14px;font-weight:bold;text-decoration:none;padding:12px 28px;border-radius:999px;">{{.CTALabel}}</a>
</td></tr>
<tr><td style="padding-top:16px;font-size:12px;line-height:1.6;color:#6b6b6b;word-break:break-all;">Or paste this link into your browser:<br><a href="{{.CTAURL}}" style="color:#2140ff;">{{.CTAURL}}</a></td></tr>{{end}}
<tr><td style="padding-top:24px;font-size:12px;color:#6b6b6b;border-top:1px solid #e8e6e1;">RevSERP &mdash; website audits that read like advice.</td></tr>
</table>
</td></tr>
</table>
</body></html>`))

type layoutData struct {
	Heading  string
	Body     template.HTML
	CTAURL   string
	CTALabel string
}

func renderLayout(heading, bodyHTML, ctaURL, ctaLabel string) string {
	var out bytes.Buffer
	_ = baseLayout.Execute(&out, layoutData{
		Heading:  heading,
		Body:     template.HTML(bodyHTML), //nolint:gosec // bodyHTML is built internally from escaped fields
		CTAURL:   ctaURL,
		CTALabel: ctaLabel,
	})
	return out.String()
}

// InviteContent holds the rendered invite email.
type InviteContent struct {
	Subject string
	Text    string
	HTML    string
}

// BuildInvite renders the workspace invite email.
func BuildInvite(inviter, orgName, inviteURL string) InviteContent {
	heading := "You're invited"
	bodyHTML := template.HTMLEscapeString(inviter) + " invited you to join <strong>" +
		template.HTMLEscapeString(orgName) + "</strong> on RevSERP."
	text := inviter + " invited you to join " + orgName + " on RevSERP.\n\nAccept the invite here:\n" + inviteURL + "\n"
	return InviteContent{
		Subject: "You're invited to join " + orgName + " on RevSERP",
		Text:    text,
		HTML:    renderLayout(heading, bodyHTML, inviteURL, "Accept invite"),
	}
}

// CrawlContent holds the rendered crawl notification email.
type CrawlContent struct {
	Subject string
	Text    string
	HTML    string
}

// BuildCrawlFinished renders the crawl completed/failed notification.
func BuildCrawlFinished(completed bool, projectName, projectURL string) CrawlContent {
	statusWord := "completed"
	heading := "Crawl completed"
	if !completed {
		statusWord = "failed"
		heading = "Crawl failed"
	}
	bodyHTML := "Your crawl for <strong>" + template.HTMLEscapeString(projectName) +
		"</strong> has " + statusWord + ". Open the project to see the results."
	text := "Your crawl for " + projectName + " has " + statusWord + ".\n\nOpen the project here:\n" + projectURL + "\n"
	return CrawlContent{
		Subject: "Crawl " + statusWord + ": " + projectName,
		Text:    text,
		HTML:    renderLayout(heading, bodyHTML, projectURL, "Open project"),
	}
}
