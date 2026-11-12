package dia

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

type Mail struct{ From, To, Subject, Body string }
type Mailer interface {
	Send(context.Context, Mail) error
}
type SMSSender interface {
	Send(ctx context.Context, to, message string) error
}
type Update struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	View  json.RawMessage `json:"view"`
	Trace []string        `json:"trace"`
}
type UpdatesPublisher interface {
	Publish(context.Context, string, Update) error
}

// SMTPMailer requires verified TLS, either STARTTLS or implicit TLS.
// Addr is host:port. Timeout defaults to 30 seconds and bounds the whole send.
type SMTPMailer struct {
	Addr, Username, Password string
	ImplicitTLS              bool
	TLSConfig                *tls.Config
	Timeout                  time.Duration
}

func (s SMTPMailer) Send(ctx context.Context, m Mail) error {
	for _, v := range []string{m.From, m.To, m.Subject} {
		if strings.ContainsAny(v, "\r\n\x00") {
			return errors.New("invalid mail header")
		}
	}
	from, e := mail.ParseAddress(m.From)
	if e != nil {
		return errors.New("invalid sender")
	}
	to, e := mail.ParseAddress(m.To)
	if e != nil {
		return errors.New("invalid recipient")
	}
	host, _, e := net.SplitHostPort(s.Addr)
	if e != nil || host == "" {
		return errors.New("invalid SMTP address")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if s.TLSConfig != nil {
		cfg = s.TLSConfig.Clone()
		if cfg.InsecureSkipVerify {
			return errors.New("SMTP requires certificate verification")
		}
		if cfg.MinVersion < tls.VersionTLS12 {
			cfg.MinVersion = tls.VersionTLS12
		}
		if cfg.ServerName == "" {
			cfg.ServerName = host
		}
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, e := (&net.Dialer{}).DialContext(ctx, "tcp", s.Addr)
	if e != nil {
		return e
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if e = conn.SetDeadline(deadline); e != nil {
		return e
	}
	var wire net.Conn = conn
	if s.ImplicitTLS {
		tc := tls.Client(conn, cfg)
		if e = tc.HandshakeContext(ctx); e != nil {
			return e
		}
		wire = tc
	}
	client, e := smtp.NewClient(wire, host)
	if e != nil {
		return e
	}
	defer client.Close()
	if !s.ImplicitTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support STARTTLS")
		}
		if e = client.StartTLS(cfg); e != nil {
			return e
		}
	}
	if s.Username != "" {
		if e = client.Auth(smtp.PlainAuth("", s.Username, s.Password, host)); e != nil {
			return e
		}
	}
	if e = client.Mail(from.Address); e != nil {
		return e
	}
	if e = client.Rcpt(to.Address); e != nil {
		return e
	}
	writer, e := client.Data()
	if e != nil {
		return e
	}
	// Base64 keeps arbitrary UTF-8 bodies and long lines within SMTP's wire limits.
	body := mimeBody(m.Body)
	message := "From: " + from.String() + "\r\nTo: " + to.String() + "\r\nSubject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + body
	if _, e = io.WriteString(writer, message); e != nil {
		return e
	}
	if e = writer.Close(); e != nil {
		return e
	}
	return client.Quit()
}

func mimeBody(body string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	var b strings.Builder
	for len(encoded) > 76 {
		b.WriteString(encoded[:76])
		b.WriteString("\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded)
	b.WriteString("\r\n")
	return b.String()
}

// TwilioSMS uses the Messages REST API. BaseURL defaults to Twilio's API origin.
type TwilioSMS struct {
	AccountSID, AuthToken, From string
	BaseURL                     string
	Client                      *http.Client
}

func (s TwilioSMS) Send(ctx context.Context, to, message string) error {
	if s.AccountSID == "" || strings.ContainsAny(s.AccountSID, "/?#%\\") || s.AuthToken == "" || s.From == "" || to == "" || message == "" {
		return errors.New("invalid SMS configuration or message")
	}
	base := s.BaseURL
	if base == "" {
		base = "https://api.twilio.com"
	}
	endpoint := strings.TrimRight(base, "/") + "/2010-04-01/Accounts/" + url.PathEscape(s.AccountSID) + "/Messages.json"
	form := url.Values{"From": {s.From}, "To": {to}, "Body": {message}}
	req, e := adapterRequest(ctx, endpoint, "application/x-www-form-urlencoded", []byte(form.Encode()))
	if e != nil {
		return e
	}
	req.SetBasicAuth(s.AccountSID, s.AuthToken)
	return adapterSend(s.Client, req)
}

// GRIPPublisher posts to a full /publish endpoint. Token is an optional bearer token.
// Published IDs are stable across retries; callers own retry scheduling.
type GRIPPublisher struct {
	URL, Token string
	Client     *http.Client
}

func (p GRIPPublisher) Publish(ctx context.Context, channel string, u Update) error {
	if channel == "" || u.ID == "" || u.Type == "" || !json.Valid(u.View) {
		return errors.New("invalid update")
	}
	content, e := json.Marshal(u)
	if e != nil {
		return e
	}
	item := map[string]any{"channel": channel, "id": u.ID, "formats": map[string]any{
		"http-response": map[string]any{"body": string(content), "headers": map[string]string{"Content-Type": "application/json"}},
		"http-stream":   map[string]string{"content": string(content) + "\n"},
		"ws-message":    map[string]string{"content": string(content)},
	}}
	body, e := json.Marshal(map[string]any{"items": []any{item}})
	if e != nil {
		return e
	}
	req, e := adapterRequest(ctx, p.URL, "application/json", body)
	if e != nil {
		return e
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	return adapterSend(p.Client, req)
}

func adapterRequest(ctx context.Context, endpoint, contentType string, body []byte) (*http.Request, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("invalid adapter endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return nil, errors.New("adapter endpoint requires HTTPS (except loopback)")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if e != nil {
		return nil, errors.New("invalid adapter request")
	}
	req.Header.Set("Content-Type", contentType)
	return req, nil
}

func adapterSend(client *http.Client, req *http.Request) error {
	c := http.Client{Timeout: 30 * time.Second}
	if client != nil {
		c = *client
		if c.Timeout <= 0 {
			c.Timeout = 30 * time.Second
		}
	}
	// Never replay a credential-bearing side effect at a redirect destination.
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := c.Do(req)
	if e != nil {
		if req.Context().Err() != nil {
			return req.Context().Err()
		}
		return errors.New("adapter transport failed")
	}
	defer resp.Body.Close()
	_, e = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if e != nil {
		return errors.New("adapter response read failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("adapter returned HTTP %d", resp.StatusCode)
	}
	return nil
}
