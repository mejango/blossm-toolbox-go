package main

import (
	"net/http"

	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/rpc"
	"github.com/mejango/blossm-toolbox-go/transport"
)

type peers struct {
	outbound  *transport.CommandClient
	delegated transport.TokenVerifier
	close     func()
}

// configurePeers fixes both ends of delegation at startup. The outbound target
// is separate from the issuing peer identity; body fields never select either.
func configurePeers(c config, jwt dia.JWT, identity *dia.Service) (peers, error) {
	result := peers{close: func() {}}
	if c.DelegatedSubject != "" {
		incoming := jwt
		incoming.Audience = c.Network + "/" + c.Service
		incoming.Signer = nil
		verify, err := transport.DelegatedCommandVerifier(incoming, identity, c.DelegatedSubject)
		if err != nil {
			return result, err
		}
		result.delegated = verify
	}
	if c.OutboundURL == "" {
		return result, nil
	}
	target := c.Network + "/" + c.OutboundService
	outgoing := jwt
	outgoing.Audience = target
	authorize, err := transport.DelegatedCommandAuthorizer(outgoing, identity, c.Network+"/"+c.Service)
	if err != nil {
		return result, err
	}
	httpTransport := http.DefaultTransport.(*http.Transport).Clone()
	wire, err := rpc.New(c.OutboundURL, rpc.Options{HTTP: &http.Client{Transport: httpTransport}})
	if err != nil {
		httpTransport.CloseIdleConnections()
		return result, err
	}
	result.outbound = &transport.CommandClient{RPC: wire, Target: target, Authorize: authorize}
	result.close = httpTransport.CloseIdleConnections
	return result, nil
}
