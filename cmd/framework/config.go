package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/mejango/blossm-toolbox-go/runtimeenv"
)

type config struct {
	Network, Service, InternalSubject, Mode        string
	DocumentNamespace, IdentityNamespace, Port     string
	Database                                       runtimeenv.Database
	HMACKey, EncryptionKey                         []byte
	Origins                                        []string
	OutboundURL, OutboundService, DelegatedSubject string
}

func loadConfig() (config, error) {
	var c config
	var err error
	for name, dst := range map[string]*string{"NETWORK": &c.Network, "SERVICE": &c.Service, "INTERNAL_SUBJECT": &c.InternalSubject, "PUBLICATION_MODE": &c.Mode, "DATABASE_NAMESPACE": &c.DocumentNamespace, "IDENTITY_DATABASE_NAMESPACE": &c.IdentityNamespace} {
		*dst, err = runtimeenv.Required(name)
		if err != nil {
			return c, err
		}
	}
	if c.DocumentNamespace == c.IdentityNamespace {
		return c, errors.New("identity and document namespaces must differ")
	}
	switch c.Mode {
	case "local", "inline", "gcp", "aws":
	default:
		return c, errors.New("PUBLICATION_MODE must be local, inline, gcp or aws")
	}
	if c.Mode == "aws" && os.Getenv("DELIVERY_QUEUE_URL") != "" && os.Getenv("DELIVERY_QUEUE_URL") == os.Getenv("PUBLICATION_QUEUE_URL") {
		return c, errors.New("delivery and publication queues must differ")
	}
	c.Database, err = runtimeenv.LoadDatabase()
	if err != nil {
		return c, err
	}
	c.HMACKey, err = runtimeenv.Secret32("DIA_HMAC_KEY")
	if err != nil {
		return c, err
	}
	c.EncryptionKey, err = runtimeenv.Secret32("DIA_ENCRYPTION_KEY")
	if err != nil {
		return c, err
	}
	c.Port = os.Getenv("PORT")
	if c.Port == "" {
		c.Port = "8080"
	}
	port, err := strconv.Atoi(c.Port)
	if err != nil || port < 1 || port > 65535 {
		return c, errors.New("invalid PORT")
	}
	if value := os.Getenv("CORS_ORIGINS"); value != "" {
		if err = json.Unmarshal([]byte(value), &c.Origins); err != nil {
			return c, errors.New("invalid CORS_ORIGINS")
		}
	}
	c.OutboundURL, c.OutboundService, c.DelegatedSubject = os.Getenv("OUTBOUND_URL"), os.Getenv("OUTBOUND_SERVICE"), os.Getenv("DELEGATED_SUBJECT")
	if (c.OutboundURL == "") != (c.OutboundService == "") {
		return c, errors.New("OUTBOUND_URL and OUTBOUND_SERVICE must be configured together")
	}
	if c.OutboundURL != "" || c.DelegatedSubject != "" {
		if strings.ContainsAny(c.Network+c.Service+c.OutboundService, "/\\ \t\r\n") {
			return c, errors.New("delegated network and service must be unambiguous identity segments")
		}
	}
	if c.OutboundURL != "" {
		u, err := url.Parse(c.OutboundURL)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
			return c, errors.New("OUTBOUND_URL requires HTTPS or loopback HTTP")
		}
	}
	if c.DelegatedSubject != "" {
		peer, ok := strings.CutPrefix(c.DelegatedSubject, c.Network+"/")
		if !ok || peer == "" || strings.ContainsAny(peer, "/\\ \t\r\n") {
			return c, errors.New("DELEGATED_SUBJECT must be a peer in the configured network")
		}
	}
	return c, nil
}
