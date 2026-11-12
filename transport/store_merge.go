package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"blossm.local/backend/eventstore"
)

var ErrStoreMergeBudget = errors.New("sorted aggregate record budget exceeded")

// SelectedStore is explicit addressing and selection, not service discovery.
// Headers are authenticated by the existing remote boundary. History=nil selects
// current aggregates; nonnil selects historical replay, including context/events.
type SelectedStore struct {
	Client                         StoreClient
	Headers                        http.Header
	Network, Service, Domain, Root string
	History                        *eventstore.HistoryOptions
}

// StoreRecord retains source identity and distinguishes historical evidence from
// current state. Historical Version is the last included event number, and must
// not be used as a current projection frontier. Current streams omit Context/Events.
type StoreRecord struct {
	eventstore.Aggregate
	Source     int
	Historical bool
	Context    json.RawMessage
	Events     []eventstore.Event
}

type StoreMergeOptions struct {
	// Less must be a strict weak ordering. Equal records retain source/input order.
	Less func(StoreRecord, StoreRecord) bool
	// Required and positive with Less. RPC body limits separately bound record bytes.
	MaxRecords int
}

// StreamStoreAggregates awaits callbacks serially. Without Less it emits each
// source incrementally in selection order. With Less it collects under MaxRecords
// and emits nothing until every source succeeds; arbitrary fields cannot be merged
// incrementally because the underlying stores do not promise that ordering.
// Callback failure/cancellation stops immediately, but cannot undo prior callbacks.
func StreamStoreAggregates(ctx context.Context, sources []SelectedStore, options StoreMergeOptions, yield func(context.Context, StoreRecord) error) error {
	if yield == nil || options.MaxRecords < 0 || (options.Less != nil && options.MaxRecords < 1) {
		return eventstore.ErrInvalid
	}
	for _, source := range sources {
		if source.Client.RPC == nil || strings.TrimSpace(source.Network) == "" || strings.TrimSpace(source.Service) == "" || strings.TrimSpace(source.Domain) == "" {
			return eventstore.ErrInvalid
		}
		if source.History != nil && source.History.ThroughNumber != nil && *source.History.ThroughNumber < 0 {
			return eventstore.ErrInvalid
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var records []StoreRecord
	emit := func(callbackCtx context.Context, record StoreRecord) error {
		if err := callbackCtx.Err(); err != nil {
			return err
		}
		if options.Less == nil {
			return yield(callbackCtx, record)
		}
		if len(records) >= options.MaxRecords {
			return ErrStoreMergeBudget
		}
		records = append(records, record)
		return nil
	}
	for i, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		check := func(root eventstore.Root) error {
			if root.Network != source.Network || root.Service != source.Service || root.Domain != source.Domain || root.ID == "" || (source.Root != "" && root.ID != source.Root) {
				return errors.New("aggregate source scope mismatch")
			}
			return nil
		}
		var err error
		if source.History == nil {
			err = source.Client.Aggregates(ctx, source.Service, source.Domain, source.Root, source.Headers, func(callCtx context.Context, a eventstore.Aggregate) error {
				if err := check(a.Root); err != nil {
					return err
				}
				return emit(callCtx, StoreRecord{Aggregate: a, Source: i})
			})
		} else {
			historical := func(callCtx context.Context, root eventstore.Root) error {
				if err := check(root); err != nil {
					return err
				}
				h, err := source.Client.History(callCtx, root, *source.History, source.Headers)
				if err != nil {
					return err
				}
				if err = check(h.Root); err != nil {
					return err
				}
				if h.Root != root {
					return errors.New("historical root mismatch")
				}
				if h.LastIncludedNumber < 0 {
					return nil
				}
				return emit(callCtx, StoreRecord{Aggregate: eventstore.Aggregate{Root: h.Root, Version: h.LastIncludedNumber, State: h.State}, Source: i, Historical: true, Context: h.Context, Events: h.Events})
			}
			if source.Root != "" {
				err = historical(ctx, eventstore.Root{Network: source.Network, Service: source.Service, Domain: source.Domain, ID: source.Root})
			} else {
				err = source.Client.Roots(ctx, source.Service, source.Domain, source.Headers, historical)
			}
		}
		if err != nil {
			return fmt.Errorf("aggregate source %d: %w", i, err)
		}
	}
	if options.Less != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		sort.SliceStable(records, func(i, j int) bool { return options.Less(records[i], records[j]) })
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := yield(ctx, record); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
