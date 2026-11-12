package dia

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

type adapterTransport func(*http.Request) (*http.Response, error)

func (f adapterTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func adapterResponse(code int) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("private provider detail"))}
}

func TestHTTPAdapters(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: adapterTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" {
			t.Error("method")
		}
		if strings.Contains(r.URL.Path, "Messages.json") {
			if r.URL.Path != "/2010-04-01/Accounts/ACtest/Messages.json" {
				t.Error(r.URL.Path)
			}
			user, pass, ok := r.BasicAuth()
			if !ok || user != "ACtest" || pass != "secret" {
				t.Error("auth")
			}
			if e := r.ParseForm(); e != nil {
				t.Fatal(e)
			}
			if r.Form.Get("To") != "+222" || r.Form.Get("From") != "+111" || r.Form.Get("Body") != "hello & +" {
				t.Error(r.Form)
			}
		} else {
			if r.Header.Get("Authorization") != "Bearer secret" {
				t.Error("bearer")
			}
			var envelope struct {
				Items []struct {
					Channel, ID string
					Formats     map[string]map[string]any
				}
			}
			if e := json.NewDecoder(r.Body).Decode(&envelope); e != nil {
				t.Fatal(e)
			}
			if len(envelope.Items) != 1 {
				t.Fatal("items")
			}
			item := envelope.Items[0]
			if item.Channel != "channel" || item.ID != "update-1" {
				t.Error(item)
			}
			var update Update
			if e := json.Unmarshal([]byte(item.Formats["ws-message"]["content"].(string)), &update); e != nil {
				t.Fatal(e)
			}
			if update.Type != "changed" || string(update.View) != `{"ok":true}` || len(update.Trace) != 1 {
				t.Error(update)
			}
			if !strings.HasSuffix(item.Formats["http-stream"]["content"].(string), "\n") {
				t.Error("stream delimiter")
			}
		}
		return adapterResponse(201), nil
	})}
	sms := TwilioSMS{AccountSID: "ACtest", AuthToken: "secret", From: "+111", Client: client}
	if e := sms.Send(context.Background(), "+222", "hello & +"); e != nil {
		t.Fatal(e)
	}
	pub := GRIPPublisher{URL: "https://grip.example/publish", Token: "secret", Client: client}
	if e := pub.Publish(context.Background(), "channel", Update{ID: "update-1", Type: "changed", View: json.RawMessage(`{"ok":true}`), Trace: []string{"trace"}}); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestAdapterFailures(t *testing.T) {
	update := Update{ID: "id", Type: "type", View: json.RawMessage(`{}`)}
	for _, code := range []int{301, 302, 307, 400, 401, 429, 500} {
		client := &http.Client{Transport: adapterTransport(func(*http.Request) (*http.Response, error) { return adapterResponse(code), nil })}
		e := (GRIPPublisher{URL: "https://example.com/publish", Client: client}).Publish(context.Background(), "channel", update)
		if e == nil || strings.Contains(e.Error(), "private") {
			t.Fatalf("status %d: %v", code, e)
		}
	}
	called := false
	client := &http.Client{Transport: adapterTransport(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("secret transport details")
	})}
	pub := GRIPPublisher{URL: "https://example.com/publish", Client: client}
	if e := pub.Publish(context.Background(), "channel", update); e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal(e)
	}
	called = false
	for _, endpoint := range []string{"http://example.com/publish", "https://user:pass@example.com/publish", "://bad", "https://example.com/#fragment"} {
		pub.URL = endpoint
		if e := pub.Publish(context.Background(), "channel", update); e == nil {
			t.Fatal(endpoint)
		}
	}
	if called {
		t.Fatal("invalid endpoint contacted")
	}
	pub.URL = "https://example.com/publish"
	update.View = json.RawMessage(`bad`)
	if e := pub.Publish(context.Background(), "channel", update); e == nil {
		t.Fatal("invalid JSON")
	}
	if called {
		t.Fatal("invalid update contacted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	update.View = json.RawMessage(`{}`)
	if e := pub.Publish(ctx, "channel", update); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestAdapterRedirectDoesNotReplay(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: adapterTransport(func(*http.Request) (*http.Response, error) {
		calls++
		resp := adapterResponse(307)
		resp.Header.Set("Location", "https://attacker.example/")
		return resp, nil
	})}
	sms := TwilioSMS{AccountSID: "ACtest", AuthToken: "secret", From: "+111", Client: client}
	if e := sms.Send(context.Background(), "+222", "hello"); e == nil {
		t.Fatal("redirect accepted")
	}
	if calls != 1 {
		t.Fatal("redirect followed", calls)
	}
	if client.CheckRedirect != nil || client.Timeout != 0 {
		t.Fatal("mutated caller client")
	}
}

// A loopback SMTP peer exercises actual TLS, SMTP framing and DATA completion.
func TestSMTP(t *testing.T) {
	certSource := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := certSource.TLS.Certificates[0]
	pool := x509.NewCertPool()
	pool.AddCert(certSource.Certificate())
	certSource.Close()
	for _, implicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "STARTTLS", true: "implicit"}[implicit], func(t *testing.T) {
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer listener.Close()
			received := make(chan string, 1)
			done := make(chan error, 1)
			go func() {
				conn, e := listener.Accept()
				if e != nil {
					done <- e
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
				if implicit {
					conn = tls.Server(conn, cfg)
				}
				reader := bufio.NewReader(conn)
				write := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
				write("220 test ESMTP")
				for {
					line, e := reader.ReadString('\n')
					if e != nil {
						done <- e
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"):
						write("250-test")
						write("250 STARTTLS")
					case strings.HasPrefix(line, "STARTTLS"):
						write("220 ready")
						conn = tls.Server(conn, cfg)
						reader = bufio.NewReader(conn)
					case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
						write("250 ok")
					case strings.HasPrefix(line, "DATA"):
						write("354 send")
						data, e := io.ReadAll(textproto.NewReader(reader).DotReader())
						if e != nil {
							done <- e
							return
						}
						received <- string(data)
						write("250 queued")
					case strings.HasPrefix(line, "QUIT"):
						write("221 bye")
						done <- nil
						return
					default:
						done <- errors.New("unexpected SMTP command")
						return
					}
				}
			}()
			sender := SMTPMailer{Addr: listener.Addr().String(), ImplicitTLS: implicit, TLSConfig: &tls.Config{RootCAs: pool}, Timeout: 5 * time.Second}
			if e = sender.Send(context.Background(), Mail{From: "sender@example.com", To: "recipient@example.com", Subject: "hello", Body: "héllo\n.world"}); e != nil {
				t.Fatal(e)
			}
			if e = <-done; e != nil {
				t.Fatal(e)
			}
			data := <-received
			pieces := strings.SplitN(data, "\n\n", 2)
			if len(pieces) != 2 {
				t.Fatal(data)
			}
			decoded, e := base64.StdEncoding.DecodeString(strings.TrimSpace(pieces[1]))
			if e != nil || string(decoded) != "héllo\n.world" {
				t.Fatal(string(decoded), e)
			}
		})
	}
}

func TestSMTPRejectsUnsafeInputAndPlaintext(t *testing.T) {
	s := SMTPMailer{Addr: "127.0.0.1:1", Timeout: time.Second}
	for _, m := range []Mail{{From: "a@example.com\r\nBcc: victim@example.com", To: "b@example.com"}, {From: "a@example.com", To: "b@example.com", Subject: "hello\nBcc: victim@example.com"}, {From: "invalid", To: "b@example.com"}} {
		if e := s.Send(context.Background(), m); e == nil {
			t.Fatal("unsafe mail")
		}
	}
	s.TLSConfig = &tls.Config{InsecureSkipVerify: true}
	if e := s.Send(context.Background(), Mail{From: "a@example.com", To: "b@example.com"}); e == nil || !strings.Contains(e.Error(), "verification") {
		t.Fatal(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	done := make(chan string, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			done <- e.Error()
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(conn, "220 test ESMTP\r\n")
		r := bufio.NewReader(conn)
		_, _ = r.ReadString('\n')
		_, _ = io.WriteString(conn, "250 test\r\n")
		line, _ := r.ReadString('\n')
		done <- line
	}()
	s = SMTPMailer{Addr: listener.Addr().String(), Username: "secret", Password: "secret", Timeout: time.Second}
	if e = s.Send(context.Background(), Mail{From: "a@example.com", To: "b@example.com"}); e == nil || !strings.Contains(e.Error(), "STARTTLS") {
		t.Fatal(e)
	}
	if line := <-done; line != "" {
		t.Fatalf("sent command without TLS: %q", line)
	}
}

func TestSMTPRejectsUntrustedCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	s := SMTPMailer{Addr: server.Listener.Addr().String(), ImplicitTLS: true, Timeout: time.Second}
	if e := s.Send(context.Background(), Mail{From: "a@example.com", To: "b@example.com"}); e == nil {
		t.Fatal("accepted untrusted server")
	}
}

func TestSMTPCancellation(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	connected := make(chan struct{})
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		close(connected)
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- (SMTPMailer{Addr: listener.Addr().String(), Timeout: 2 * time.Second}).Send(ctx, Mail{From: "a@example.com", To: "b@example.com"})
	}()
	<-connected
	cancel()
	select {
	case e := <-result:
		if e == nil {
			t.Fatal("cancellation succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt SMTP")
	}
	<-closed
}
