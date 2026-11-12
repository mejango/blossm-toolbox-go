package redis

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRemoteWireAndErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 3; i++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				for step := 0; step < 3; step++ {
					v, err := readReply(r, 0)
					if err != nil {
						t.Error(err)
						return
					}
					a := v.([]any)
					switch a[0] {
					case "AUTH":
						if a[1] != "user" || a[2] != "secret" {
							t.Error("bad auth")
						}
						io.WriteString(conn, "+OK\r\n")
					case "SELECT":
						if a[1] != "2" {
							t.Error("bad DB")
						}
						io.WriteString(conn, "+OK\r\n")
					case "HSET":
						if a[2] != "a" || a[3] != "1" {
							t.Error("bad HSET")
						}
						io.WriteString(conn, ":1\r\n")
					case "HGETALL":
						io.WriteString(conn, "*2\r\n$1\r\na\r\n$1\r\n1\r\n")
					case "EXPIRE":
						io.WriteString(conn, "-ERR secret should never leak\r\n")
					default:
						t.Error("unexpected command")
					}
				}
			}()
		}
	}()
	c := Client{Address: listener.Addr().String(), Username: "user", Password: "secret", Database: 2}
	ctx := context.Background()
	if err = c.WriteObject(ctx, "k", map[string]string{"a": "1"}); err != nil {
		t.Fatal(err)
	}
	if v, err := c.ReadObject(ctx, "k"); err != nil || v["a"] != "1" {
		t.Fatal(v, err)
	}
	if _, err = c.SetExpiry(ctx, "k", 10); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	wg.Wait()
}
func TestMemoryExpiryAndIsolation(t *testing.T) {
	c := NewMemory()
	now := time.Now()
	c.now = func() time.Time { return now }
	ctx := context.Background()
	input := map[string]string{"a": "1"}
	if err := c.WriteObject(ctx, "k", input); err != nil {
		t.Fatal(err)
	}
	input["a"] = "2"
	c.SetExpiry(ctx, "k", 1)
	c.WriteObject(ctx, "k", map[string]string{"b": "3"})
	v, _ := c.ReadObject(ctx, "k")
	if v["a"] != "1" || v["b"] != "3" {
		t.Fatal(v)
	}
	v["a"] = "9"
	again, _ := c.ReadObject(ctx, "k")
	if again["a"] != "1" {
		t.Fatal("cache alias")
	}
	now = now.Add(time.Second)
	v, _ = c.ReadObject(ctx, "k")
	if len(v) != 0 {
		t.Fatal("expired value survived rewrite")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.WriteObject(ctx, fmt.Sprint(i), map[string]string{"a": "1"})
			c.ReadObject(ctx, fmt.Sprint(i))
		}(i)
	}
	wg.Wait()
}
func TestMalformedRESP(t *testing.T) {
	for _, wire := range []string{"$99999999\r\n", "*65537\r\n", ":wrong\r\n", "$2\r\nx\r\n", "?unknown\r\n", "+no-newline"} {
		if _, err := readReply(bufio.NewReader(strings.NewReader(wire)), 0); err == nil {
			t.Fatal("accepted malformed reply")
		}
	}
}
