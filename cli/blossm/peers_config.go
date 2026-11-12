package cli

import (
	"errors"
	"net/url"
	"strings"
)

// Peers contains fixed application routing, never an independently chosen signing subject.
type Peers struct {
	Network          string `json:"network"`
	Service          string `json:"service"`
	OutboundURL      string `json:"outboundURL,omitempty"`
	OutboundService  string `json:"outboundService,omitempty"`
	DelegatedSubject string `json:"delegatedSubject,omitempty"`
}

func (c Config) validatePeers() error {
	if c.Peers == nil {
		return nil
	}
	if c.Peers.Network == "" || !resourceName.MatchString(c.Peers.Service) {
		return errors.New("peers require network and valid service")
	}
	p := c.Peers
	if (p.OutboundURL == "") != (p.OutboundService == "") {
		return errors.New("peers outboundURL and outboundService must be configured together")
	}
	if strings.ContainsAny(p.Network+p.Service+p.OutboundService, "/\\ \t\r\n\x00") {
		return errors.New("peer identity segments must be unambiguous")
	}
	if p.OutboundURL != "" {
		u, err := url.Parse(p.OutboundURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("deployment peer outboundURL requires HTTPS without credentials, query or fragment")
		}
	}
	if p.DelegatedSubject != "" {
		peer, ok := strings.CutPrefix(p.DelegatedSubject, p.Network+"/")
		if !ok || peer == "" || strings.ContainsAny(peer, "/\\ \t\r\n\x00") {
			return errors.New("delegatedSubject must be one exact peer in the configured network")
		}
	}
	return nil
}
