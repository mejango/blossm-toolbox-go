// Package runtimeenv owns shared environment and native backend construction.
package runtimeenv

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"github.com/mejango/blossm-toolbox-go/eventstore"
	mongoStore "github.com/mejango/blossm-toolbox-go/storage/mongo"
	"github.com/mejango/blossm-toolbox-go/storage/postgres"
)

func Required(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("%s required", name)
	}
	return value, nil
}
func Secret32(name string) ([]byte, error) {
	value, err := Required(name)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, fmt.Errorf("%s must be base64 encoded32bytes", name)
	}
	return decoded, nil
}

// Database captures configuration once so multiple namespaces use one endpoint.
type Database struct{ Driver, URL, Name string }

func LoadDatabase() (Database, error) {
	cfg := Database{Driver: os.Getenv("DATABASE_DRIVER")}
	var err error
	switch cfg.Driver {
	case "postgres":
		cfg.URL, err = Required("POSTGRES_URL")
	case "mongo":
		cfg.URL, err = Required("MONGODB_URI")
		if err == nil {
			cfg.Name, err = Required("MONGODB_DATABASE")
		}
	default:
		err = errors.New("DATABASE_DRIVER must be postgres or mongo")
	}
	return cfg, err
}
func (c Database) Open(ctx context.Context, namespace string) (eventstore.Backend, error) {
	switch c.Driver {
	case "postgres":
		return postgres.Open(ctx, c.URL, namespace)
	case "mongo":
		return mongoStore.Open(ctx, mongoStore.Config{URI: c.URL, Database: c.Name, Namespace: namespace})
	default:
		return nil, errors.New("DATABASE_DRIVER must be postgres or mongo")
	}
}
