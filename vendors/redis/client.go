// Package redis supplies explicit remote Redis hashes and a separate local memory cache.
package redis

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client is a bounded RESP2 adapter for HSET/HGETALL/EXPIRE, not a general Redis SDK.
// Each operation owns a connection; callers needing pooling can replace this adapter.
type Client struct {
	Address, Username, Password string
	Database                    int
	TLS                         *tls.Config
	Timeout                     time.Duration
}

func (c *Client) do(ctx context.Context, args ...string) (any, error) {
	if c.Address == "" || c.Database < 0 {
		return nil, errors.New("Redis requires an address and nonnegative database")
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", c.Address)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Redis connection failed")
	}
	defer conn.Close()
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if c.TLS != nil {
		cfg := c.TLS.Clone()
		if cfg.MinVersion < tls.VersionTLS12 {
			cfg.MinVersion = tls.VersionTLS12
		}
		if cfg.ServerName == "" {
			cfg.ServerName, _, err = net.SplitHostPort(c.Address)
			if err != nil {
				return nil, errors.New("invalid Redis address")
			}
		}
		secured := tls.Client(conn, cfg)
		if err = secured.HandshakeContext(ctx); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("Redis TLS handshake failed")
		}
		conn = secured
	}
	r := bufio.NewReader(io.LimitReader(conn, 8<<20))
	call := func(a ...string) (any, error) {
		if err := writeCommand(conn, a); err != nil {
			return nil, errors.New("Redis request write failed")
		}
		v, err := readReply(r, 0)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		return v, nil
	}
	if c.Password != "" {
		auth := []string{"AUTH"}
		if c.Username != "" {
			auth = append(auth, c.Username)
		}
		auth = append(auth, c.Password)
		if _, err = call(auth...); err != nil {
			return nil, err
		}
	}
	if c.Database != 0 {
		if _, err = call("SELECT", strconv.Itoa(c.Database)); err != nil {
			return nil, err
		}
	}
	return call(args...)
}
func writeCommand(w io.Writer, args []string) error {
	size := 0
	for _, s := range args {
		size += len(s)
	}
	if size > 4<<20 {
		return errors.New("Redis request exceeds 4 MiB")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, s := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(s), s)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
func readReply(r *bufio.Reader, depth int) (any, error) {
	if depth > 2 {
		return nil, errors.New("Redis response nesting exceeds limit")
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, errors.New("Redis response read failed")
	}
	if len(line) < 3 || !strings.HasSuffix(line, "\r\n") {
		return nil, errors.New("invalid Redis response")
	}
	kind := line[0]
	text := line[1 : len(line)-2]
	switch kind {
	case '+':
		return text, nil
	case '-':
		return nil, errors.New("Redis command rejected")
	case ':':
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, errors.New("invalid Redis integer")
		}
		return n, nil
	case '$', '*':
		n, err := strconv.Atoi(text)
		if err != nil || n < -1 || n > 4<<20 {
			return nil, errors.New("invalid Redis response length")
		}
		if n == -1 {
			return nil, nil
		}
		if kind == '$' {
			b := make([]byte, n+2)
			if _, err = io.ReadFull(r, b); err != nil || string(b[n:]) != "\r\n" {
				return nil, errors.New("invalid Redis bulk response")
			}
			return string(b[:n]), nil
		}
		if n > 65536 {
			return nil, errors.New("Redis hash response exceeds field limit")
		}
		a := make([]any, n)
		for i := range a {
			a[i], err = readReply(r, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return a, nil
	default:
		return nil, errors.New("unsupported Redis response")
	}
}
func (c *Client) WriteObject(ctx context.Context, key string, fields map[string]string) error {
	if len(fields) == 0 {
		return errors.New("hash requires at least one field")
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	a := []string{"HSET", key}
	for _, k := range keys {
		a = append(a, k, fields[k])
	}
	v, err := c.do(ctx, a...)
	if err != nil {
		return err
	}
	if _, ok := v.(int64); !ok {
		return errors.New("invalid Redis HSET response")
	}
	return nil
}
func (c *Client) ReadObject(ctx context.Context, key string) (map[string]string, error) {
	v, err := c.do(ctx, "HGETALL", key)
	if err != nil {
		return nil, err
	}
	pairs, ok := v.([]any)
	if !ok || len(pairs)%2 != 0 {
		return nil, errors.New("invalid Redis hash response")
	}
	result := map[string]string{}
	for i := 0; i < len(pairs); i += 2 {
		k, ok := pairs[i].(string)
		if !ok {
			return nil, errors.New("invalid Redis field")
		}
		value, ok := pairs[i+1].(string)
		if !ok {
			return nil, errors.New("invalid Redis value")
		}
		result[k] = value
	}
	return result, nil
}
func (c *Client) SetExpiry(ctx context.Context, key string, seconds int64) (bool, error) {
	v, err := c.do(ctx, "EXPIRE", key, strconv.FormatInt(seconds, 10))
	if err != nil {
		return false, err
	}
	n, ok := v.(int64)
	if !ok || (n != 0 && n != 1) {
		return false, errors.New("invalid Redis expiration response")
	}
	return n == 1, nil
}

type entry struct {
	fields  map[string]string
	expires time.Time
}

// Memory is explicit process-local storage. A remote failure never silently selects it.
type Memory struct {
	mu      sync.Mutex
	entries map[string]entry
	now     func() time.Time
}

func NewMemory() *Memory { return &Memory{entries: map[string]entry{}, now: time.Now} }
func (m *Memory) get(key string) (entry, bool) {
	e, ok := m.entries[key]
	if ok && !e.expires.IsZero() && !m.now().Before(e.expires) {
		delete(m.entries, key)
		return entry{}, false
	}
	return e, ok
}
func (m *Memory) WriteObject(ctx context.Context, key string, fields map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(fields) == 0 {
		return errors.New("hash requires at least one field")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.get(key)
	if !ok {
		e.fields = map[string]string{}
	}
	for k, v := range fields {
		e.fields[k] = v
	}
	m.entries[key] = e
	return nil
}
func (m *Memory) ReadObject(ctx context.Context, key string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, _ := m.get(key)
	out := map[string]string{}
	for k, v := range e.fields {
		out[k] = v
	}
	return out, nil
}
func (m *Memory) SetExpiry(ctx context.Context, key string, seconds int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.get(key)
	if !ok {
		return false, nil
	}
	if seconds <= 0 {
		delete(m.entries, key)
		return true, nil
	}
	if seconds > int64((1<<63-1)/int64(time.Second)) {
		return false, errors.New("expiry exceeds supported duration")
	}
	e.expires = m.now().Add(time.Duration(seconds) * time.Second)
	m.entries[key] = e
	return true, nil
}
