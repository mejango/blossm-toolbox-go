package runtimeenv

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestEnvironmentConfiguration(t *testing.T) {
	t.Setenv("TEST_SECRET", "")
	if _, err := Required("TEST_SECRET"); err == nil {
		t.Fatal("missing accepted")
	}
	for _, value := range []string{"bad", base64.StdEncoding.EncodeToString(make([]byte, 31)), base64.StdEncoding.EncodeToString(make([]byte, 33))} {
		t.Setenv("TEST_SECRET", value)
		if _, err := Secret32("TEST_SECRET"); err == nil {
			t.Fatal("invalid secret accepted")
		}
	}
	t.Setenv("TEST_SECRET", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))))
	if value, err := Secret32("TEST_SECRET"); err != nil || len(value) != 32 {
		t.Fatal(err)
	}
	for _, driver := range []string{"", "other", "postgres", "mongo"} {
		t.Run(driver, func(t *testing.T) {
			t.Setenv("DATABASE_DRIVER", driver)
			t.Setenv("POSTGRES_URL", "")
			t.Setenv("MONGODB_URI", "")
			t.Setenv("MONGODB_DATABASE", "")
			if _, err := LoadDatabase(); err == nil {
				t.Fatal("incomplete database accepted")
			}
		})
	}
	t.Setenv("DATABASE_DRIVER", "postgres")
	t.Setenv("POSTGRES_URL", "postgres://fixture")
	pg, err := LoadDatabase()
	if err != nil || pg.URL != "postgres://fixture" || pg.Driver != "postgres" {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_DRIVER", "mongo")
	t.Setenv("MONGODB_URI", "mongodb://fixture")
	t.Setenv("MONGODB_DATABASE", "db")
	mongo, err := LoadDatabase()
	if err != nil || mongo.Name != "db" {
		t.Fatal(err)
	}
	if _, err := (Database{}).Open(context.Background(), "fixture"); err == nil {
		t.Fatal("invalid adapter accepted")
	}
}
