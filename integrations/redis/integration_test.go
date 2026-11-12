//go:build redis_integration

package redis

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestNativeRedisHashAndExpiry(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Fatal("REDIS_ADDR is required for redis_integration")
	}
	c := Client{Address: addr, Password: os.Getenv("REDIS_PASSWORD"), Database: 15}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	key := fmt.Sprintf("blossm-native-%d", time.Now().UnixNano())
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := c.SetExpiry(cleanup, key, 0); err != nil {
			t.Error(err)
		}
	}()
	if err := c.WriteObject(ctx, key, map[string]string{"a": "one", "binary": "\x00\r\n"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := c.SetExpiry(ctx, key, 60); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := c.WriteObject(ctx, key, map[string]string{"b": "two"}); err != nil {
		t.Fatal(err)
	}
	v, err := c.ReadObject(ctx, key)
	if err != nil || v["a"] != "one" || v["b"] != "two" || v["binary"] != "\x00\r\n" {
		t.Fatal(v, err)
	}
	if ok, err := c.SetExpiry(ctx, key, 0); err != nil || !ok {
		t.Fatal(ok, err)
	}
	v, err = c.ReadObject(ctx, key)
	if err != nil || len(v) != 0 {
		t.Fatal(v, err)
	}
}
