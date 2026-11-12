package transport

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mejango/blossm-toolbox-go/procedures/fact"
	"github.com/mejango/blossm-toolbox-go/rpc"
	"github.com/mejango/blossm-toolbox-go/stores/event"
	"net/http"
	"net/url"
)

// StoreClient uses an explicit service URL and per-call credentials. There is no
// process-wide service discovery or identity shared between callers.
type StoreClient struct{ RPC *rpc.Client }

func storeRead[T any](ctx context.Context, c StoreClient, path string, in any, headers http.Header) (out T, err error) {
	if c.RPC == nil {
		return out, errors.New("store RPC client required")
	}
	response, err := c.RPC.Do(ctx, http.MethodPost, path, nil, in, headers)
	if err != nil {
		return out, err
	}
	if response.StatusCode != 200 {
		return out, errors.New("unexpected store response status")
	}
	err = eventstore.Decode(response.Body, &out)
	return out, err
}
func (c StoreClient) Append(ctx context.Context, in []eventstore.Input, headers http.Header) ([]eventstore.Receipt, error) {
	return storeRead[[]eventstore.Receipt](ctx, c, "/events", struct {
		Events []eventstore.Input `json:"events"`
	}{in}, headers)
}
func (c StoreClient) Aggregate(ctx context.Context, root eventstore.Root, headers http.Header) (eventstore.Aggregate, error) {
	return storeRead[eventstore.Aggregate](ctx, c, "/aggregate", root, headers)
}
func (c StoreClient) Count(ctx context.Context, root eventstore.Root, headers http.Header) (int64, error) {
	out, err := storeRead[struct {
		Count int64 `json:"count"`
	}](ctx, c, "/count", root, headers)
	return out.Count, err
}
func (c StoreClient) Query(ctx context.Context, service, domain string, predicates []eventstore.Predicate, headers http.Header) ([]eventstore.Aggregate, error) {
	return storeRead[[]eventstore.Aggregate](ctx, c, "/query", struct {
		Service    string                 `json:"service"`
		Domain     string                 `json:"domain"`
		Predicates []eventstore.Predicate `json:"predicates"`
	}{service, domain, predicates}, headers)
}
func (c StoreClient) History(ctx context.Context, root eventstore.Root, options eventstore.HistoryOptions, headers http.Header) (eventstore.HistoricalAggregate, error) {
	return storeRead[eventstore.HistoricalAggregate](ctx, c, "/history", map[string]any{"root": root, "throughNumber": options.ThroughNumber, "throughCreated": options.ThroughCreated, "includeEvents": options.IncludeEvents}, headers)
}
func (c StoreClient) Roots(ctx context.Context, service, domain string, headers http.Header, yield func(context.Context, eventstore.Root) error) error {
	if yield == nil {
		return errors.New("root consumer required")
	}
	return (fact.Client{RPC: c.RPC}).Stream(ctx, "/roots", url.Values{"service": {service}, "domain": {domain}}, headers, func(ctx context.Context, raw json.RawMessage) error {
		var root eventstore.Root
		if err := eventstore.Decode(raw, &root); err != nil {
			return err
		}
		return yield(ctx, root)
	})
}
func (c StoreClient) Aggregates(ctx context.Context, service, domain, root string, headers http.Header, yield func(context.Context, eventstore.Aggregate) error) error {
	if yield == nil {
		return errors.New("aggregate consumer required")
	}
	return (fact.Client{RPC: c.RPC}).Stream(ctx, "/aggregates", url.Values{"service": {service}, "domain": {domain}, "root": {root}}, headers, func(ctx context.Context, raw json.RawMessage) error {
		var aggregate eventstore.Aggregate
		if err := eventstore.Decode(raw, &aggregate); err != nil {
			return err
		}
		return yield(ctx, aggregate)
	})
}

// BuildBlock requires the server's configured producer signer.
func (c StoreClient) BuildBlock(ctx context.Context, maxRoots int, headers http.Header) (eventstore.Block, error) {
	return storeRead[eventstore.Block](ctx, c, "/blocks", struct {
		MaxRoots int `json:"maxRoots"`
	}{maxRoots}, headers)
}
