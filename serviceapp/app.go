package serviceapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"

	"blossm.local/backend/command"
	"blossm.local/backend/delivery"
	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/fact"
	"blossm.local/backend/job"
	"blossm.local/backend/projection"
	"blossm.local/backend/transport"
	"blossm.local/backend/view"
)

type Config struct {
	Store             *eventstore.Store
	Identity          *dia.Service
	Service           string
	Internal          transport.TokenVerifier
	Delegated         transport.TokenVerifier
	Outbound          *transport.CommandClient
	DeliveryVerifier  transport.TokenVerifier
	Publisher         eventstore.Publisher
	InlinePublication bool
	Queue             command.Queue
	Replay            projection.Producer
	Push              func(context.Context, projection.Notification) error
	OnRejected        func(context.Context, delivery.Rejection) error
}
type App struct {
	Store            *eventstore.Store
	Identity         *dia.Service
	Engine           *projection.Engine
	Publication      *projection.PublicationAdapter
	Delivery         *delivery.Consumer
	Publisher        eventstore.Publisher
	Handler          http.Handler
	network, service string
}

// New composes the real procedure gateway, ownership policies, durable store,
// projection delivery and named view. It borrows clients/backend; caller owns
// worker cancellation and closure. Configuration is immutable after creation.
func New(ctx context.Context, c Config) (*App, error) {
	if c.Store == nil || c.Store.Backend == nil || c.Identity == nil || c.Identity.Network == "" || c.Internal == nil || strings.TrimSpace(c.Service) == "" {
		return nil, errors.New("store, Dia identity, internal verifier and service required")
	}
	identityStore, ok := c.Identity.Store.(*eventstore.Store)
	if !ok || identityStore.Backend == nil {
		return nil, errors.New("identity HTTP requires its event store")
	}
	if identityStore == c.Store || (reflect.TypeOf(identityStore.Backend) == reflect.TypeOf(c.Store.Backend) && reflect.TypeOf(c.Store.Backend).Comparable() && identityStore.Backend == c.Store.Backend) {
		return nil, errors.New("identity and document stores require separate namespaces")
	}
	if c.InlinePublication && (c.Store.Local || c.Publisher != nil) {
		return nil, errors.New("inline publication requires remote-shaped store and no external publisher")
	}
	if !c.Store.Local && !c.InlinePublication && c.Publisher == nil {
		return nil, errors.New("remote publisher required")
	}
	if c.Queue != nil && c.DeliveryVerifier == nil {
		return nil, errors.New("queued commands require a separate delivery verifier")
	}
	if c.Outbound != nil && (c.Outbound.RPC == nil || c.Outbound.Authorize == nil || !strings.HasPrefix(c.Outbound.Target, c.Identity.Network+"/") || len(strings.Split(c.Outbound.Target, "/")) != 2) {
		return nil, errors.New("outbound client requires a same-network service target and authenticator")
	}
	store := *c.Store
	store.Reducers = make(map[string]eventstore.Reducer, len(c.Store.Reducers)+1)
	for key, r := range c.Store.Reducers {
		store.Reducers[key] = r
	}
	key := c.Service + "/" + Domain
	if _, exists := store.Reducers[key]; exists {
		return nil, errors.New("document reducer already registered")
	}
	store.Reducers[key] = documentReducer{}
	a := &App{Store: &store, Identity: c.Identity, network: c.Identity.Network, service: c.Service}
	networkValue, _ := eventstore.Encode(a.network)
	writer := &view.Store{Name: ViewName, Backend: store.Backend, Scope: view.Scope{Network: a.network, Service: a.service, Domain: Domain, Owner: []eventstore.Predicate{{Path: "owner.network", Value: networkValue}}}}
	source := projection.Source{Network: a.network, Service: a.service, Domain: Domain}
	a.Engine = &projection.Engine{Aggregates: a.Store, Views: writer, Push: c.Push, Handlers: map[projection.Source]projection.Handler{source: func(_ context.Context, in projection.Input) (projection.Plan, error) {
		return projection.Plan{Mutations: []projection.Mutation{{ID: in.Aggregate.Root.ID, Body: in.Aggregate.State}}}, nil
	}}}
	a.Publication = &projection.PublicationAdapter{Backend: store.Backend, Engine: a.Engine}
	a.Publisher = c.Publisher
	if c.InlinePublication {
		a.Publisher = a.Publication
	}
	a.Delivery = &delivery.Consumer{Commands: a.Store, Projections: a.Engine, Publications: a.Publication, Admit: a.admit,
		Rejections: delivery.RejectionJournal{Backend: store.Backend, Scope: eventstore.Root{Network: a.network, Service: a.service, Domain: "$delivery", ID: "consumer"}},
		Forbidden:  func(err error) bool { return errors.Is(err, dia.ErrForbidden) || errors.Is(err, command.ErrForbidden) },
		OnRejected: c.OnRejected,
	}
	verifier, err := transport.DiaVerifier(c.Identity, "access")
	if err != nil {
		return nil, err
	}
	scope := func(_ context.Context, id transport.Identity, _ transport.GatewayRequest) (view.Scope, error) {
		owner, err := principal(id.Context, a.network)
		if err != nil {
			return view.Scope{}, err
		}
		raw, _ := eventstore.Encode(owner)
		// Match every Ref component through the typed owning state, not only ID.
		var fields map[string]json.RawMessage
		_ = eventstore.Decode(raw, &fields)
		predicates := make([]eventstore.Predicate, 0, len(fields))
		for name, value := range fields {
			predicates = append(predicates, eventstore.Predicate{Path: "owner." + name, Value: value})
		}
		return view.Scope{Network: a.network, Service: a.service, Domain: Domain, Owner: predicates}, nil
	}
	check := func(_ context.Context, id transport.Identity, request transport.GatewayRequest) error {
		var claims dia.Claims
		if eventstore.Decode(id.Claims, &claims) != nil || (claims.Kind != "access" && (request.Request == nil || request.Request.Pattern != "POST /commands/{name}")) {
			return dia.ErrUnauthorized
		}
		_, err := principal(id.Context, a.network)
		return err
	}
	userRoutes := map[transport.GatewayRoute]transport.GatewayPolicy{
		{Method: "POST", Pattern: "/commands/{name}", Name: "set"}:                              {RequiredContexts: []string{"principal"}, Check: check},
		{Method: "GET", Pattern: "/facts/{name}", Name: "document"}:                             {RequiredContexts: []string{"principal"}, Check: check},
		{Method: "POST", Pattern: "/jobs/{name}", Name: "verify"}:                               {RequiredContexts: []string{"principal"}, Check: check},
		{Method: "GET", Pattern: "/view-stores/{name}", Name: ViewName, Operation: "list"}:      {Check: check, Scope: scope},
		{Method: "GET", Pattern: "/view-stores/{name}/{id}", Name: ViewName, Operation: "read"}: {Check: check, Scope: scope},
	}
	if c.Outbound != nil {
		userRoutes[transport.GatewayRoute{Method: "POST", Pattern: "/commands/{name}", Name: "copy"}] = transport.GatewayPolicy{RequiredContexts: []string{"principal"}, Check: check}
	}
	commandVerifier := func(ctx context.Context, token string) (transport.Identity, error) {
		id, err := verifier(ctx, token)
		if err == nil || c.Delegated == nil {
			return id, err
		}
		return c.Delegated(ctx, token)
	}
	for route, policy := range userRoutes {
		if route.Pattern == "/commands/{name}" {
			policy.Check = func(ctx context.Context, id transport.Identity, request transport.GatewayRequest) error {
				if err := check(ctx, id, request); err != nil {
					return err
				}
				var claims dia.Claims
				if err := eventstore.Decode(id.Claims, &claims); err != nil {
					return dia.ErrUnauthorized
				}
				if claims.Kind == "service" {
					return transport.DelegatedCommandCheck(ctx, id, request)
				}
				return nil
			}
			userRoutes[route] = policy
		}
	}
	authority, err := transport.NewGatewayAuthority(transport.Authenticator{Bearer: commandVerifier}, userRoutes)
	if err != nil {
		return nil, err
	}
	internalCheck := func(context.Context, transport.Identity, transport.GatewayRequest) error { return nil } // Verifier binds exact configured peer identity/audience.
	internalRoutes := map[transport.GatewayRoute]transport.GatewayPolicy{}
	for _, path := range []string{"/events", "/aggregate", "/count", "/query", "/history"} {
		internalRoutes[transport.GatewayRoute{Method: "POST", Pattern: path}] = transport.GatewayPolicy{Check: internalCheck}
	}
	for _, path := range []string{"/roots", "/aggregates"} {
		internalRoutes[transport.GatewayRoute{Method: "GET", Pattern: path}] = transport.GatewayPolicy{Check: internalCheck}
	}
	replay := c.Replay
	if replay == nil && (store.Local || c.InlinePublication) {
		replay = directReplay{a.Engine}
	}
	if replay == nil {
		return nil, errors.New("remote replay producer required")
	}
	scheduler := &projection.Scheduler{Roots: projection.BackendRoots{Backend: store.Backend}, Producer: replay}
	for _, operation := range []string{"play", "replay"} {
		internalRoutes[transport.GatewayRoute{Method: "POST", Pattern: "/projections/{name}/" + operation, Name: ViewName, Operation: operation}] = transport.GatewayPolicy{Check: internalCheck, Projection: func(context.Context, transport.Identity, transport.GatewayRequest) (transport.ProjectionBinding, error) {
			return transport.ProjectionBinding{Source: source, Engine: a.Engine, Scheduler: scheduler}, nil
		}}
	}
	internal, err := transport.NewGatewayAuthority(transport.Authenticator{Bearer: c.Internal}, internalRoutes)
	if err != nil {
		return nil, err
	}
	authenticate := func(r *http.Request) (transport.Identity, error) {
		if r.PathValue("name") != "" {
			return authority.Framework(r)
		}
		return internal.Framework(r)
	}
	commands := map[string]command.Procedure{"set": a.command(c.Queue)}
	if c.Outbound != nil {
		commands["copy"] = a.copyCommand(*c.Outbound)
	}
	framework := transport.Framework{Authorize: authenticate, Store: a.Store, Network: a.network, Publisher: a.Publisher,
		Commands: commands, Facts: map[string]fact.Procedure{"document": a.fact()}, Jobs: map[string]job.Procedure{"verify": a.verifyJob()},
		Views: &transport.ViewGateway{Authorize: authority.View, Stores: map[string]eventstore.Backend{ViewName: store.Backend}}, Projections: &transport.ProjectionRoutes{Resolve: internal.Projection}}
	handler, err := framework.Handler(ctx)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	if c.DeliveryVerifier != nil {
		auth := transport.Authenticator{Bearer: c.DeliveryVerifier}
		deliveryHandler, err := a.Delivery.Handler(func(r *http.Request) error { _, err := auth.Authenticate(r); return err })
		if err != nil {
			return nil, err
		}
		mux.Handle("/deliveries", deliveryHandler)
	}
	mux.HandleFunc("GET /delivery-rejections/{id}", func(w http.ResponseWriter, r *http.Request) {
		auth := transport.Authenticator{Bearer: c.Internal}
		if _, err := auth.Authenticate(r); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		record, err := a.Delivery.Rejections.Lookup(r.Context(), r.PathValue("id"))
		if errors.Is(err, eventstore.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "rejection unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(record)
	})
	mux.Handle("/identity/", http.StripPrefix("/identity", transport.API{Dia: c.Identity, Store: identityStore}.Handler()))
	mux.Handle("/", handler)
	a.Handler = mux
	return a, nil
}

type directReplay struct{ engine *projection.Engine }

func (p directReplay) Enqueue(ctx context.Context, r projection.Request) error {
	_, err := p.engine.Play(ctx, r)
	return err
}
func (a *App) admit(_ context.Context, e delivery.Envelope) error {
	allowed := func(r eventstore.Root) bool {
		return r.Network == a.network && r.Service == a.service && r.Domain == Domain && r.ID != ""
	}
	if e.Command != nil {
		for _, in := range e.Command.Events {
			if !allowed(in.Root) || in.Action != "set" {
				return dia.ErrForbidden
			}
		}
		return nil
	}
	if e.Replay != nil && allowed(e.Replay.Root) {
		return nil
	}
	if e.Publication != nil && allowed(e.Publication.Event.Input.Root) {
		return nil
	}
	return dia.ErrForbidden
}
