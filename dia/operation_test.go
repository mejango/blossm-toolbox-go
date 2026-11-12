package dia

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mejango/blossm-toolbox-go/eventstore"
)

// Controls only the append/lookup boundary; real domain reducers commit the batch.
type receiptErrorStore struct {
	*systemTestStore
	appendErr, lookupErr error
	commit, mismatch     bool
	cancel               context.CancelFunc
	appends, lookups     int
}

func (s *receiptErrorStore) Append(ctx context.Context, inputs []eventstore.Input) ([]eventstore.Receipt, error) {
	s.appends++
	if s.commit {
		if _, err := s.systemTestStore.Append(ctx, inputs); err != nil {
			return nil, err
		}
		if s.mismatch {
			root := inputs[len(inputs)-1].Root
			var op operationState
			if err := decode(s.snapshot(root).State, &op); err != nil {
				return nil, err
			}
			op.Digest = strings.Repeat("0", 64)
			s.seed(root, op)
		}
	}
	if s.cancel != nil {
		s.cancel()
	}
	return nil, s.appendErr
}
func (s *receiptErrorStore) Aggregate(ctx context.Context, root Ref) (eventstore.Aggregate, error) {
	s.lookups++
	if s.appends > 0 && s.lookupErr != nil {
		return eventstore.Aggregate{}, s.lookupErr
	}
	return s.systemTestStore.Aggregate(ctx, root)
}

func TestAppendErrorReconcilesOnlyExactVisibleReceipt(t *testing.T) {
	raw := errors.New("unclassified backend write conflict")
	lookup := errors.New("receipt read unavailable")
	for _, tc := range []struct {
		name                       string
		commit, mismatch, cancel   bool
		appendErr, lookupErr, want error
	}{
		{name: "raw committed", commit: true, appendErr: raw},
		{name: "uncertain committed", commit: true, appendErr: eventstore.ErrUncertain},
		{name: "conflict committed", commit: true, appendErr: eventstore.ErrConflict},
		{name: "different receipt", commit: true, mismatch: true, appendErr: raw, want: eventstore.ErrIdempotency},
		{name: "absent raw", appendErr: raw, want: raw},
		{name: "absent uncertain", appendErr: eventstore.ErrUncertain, want: eventstore.ErrUncertain},
		{name: "unreadable raw", commit: true, appendErr: raw, lookupErr: lookup, want: raw},
		{name: "unreadable uncertain", commit: true, appendErr: eventstore.ErrUncertain, lookupErr: lookup, want: eventstore.ErrUncertain},
		{name: "canceled raw", commit: true, cancel: true, appendErr: raw, want: raw},
		{name: "canceled uncertain", commit: true, cancel: true, appendErr: eventstore.ErrUncertain, want: eventstore.ErrUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, explicit := range []bool{false, true} {
				t.Run(map[bool]string{false: "event-digest", true: "request-digest"}[explicit], func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					store := &receiptErrorStore{systemTestStore: newSystemTestStore(), appendErr: tc.appendErr, lookupErr: tc.lookupErr, commit: tc.commit, mismatch: tc.mismatch}
					if tc.cancel {
						store.cancel = cancel
					}
					service := &Service{Store: store, Network: "local"}
					root := service.root("core", "principal", "principal")
					event := service.event(root, "set-subject", "request", 0, map[string]Ref{"subject": service.root("core", "account", "account")})
					var err error
					if explicit {
						err = service.appendAtomicRequest(ctx, []eventstore.Input{event}, mustJSON(map[string]string{"request": "original"}), service.root("core", "operation", "explicit"))
					} else {
						err = service.append(ctx, event)
					}
					if err != tc.want {
						t.Fatalf("got %v want original/exact %v", err, tc.want)
					}
					if store.appends != 1 || store.lookups != 2 {
						t.Fatalf("unexpected replay/lookup count: %d/%d", store.appends, store.lookups)
					}
					expectedBatches := 0
					if tc.commit {
						expectedBatches = 1
					}
					if len(store.batches) != expectedBatches {
						t.Fatal("reconciliation wrote effects")
					}
				})
			}
		})
	}
}

func TestCommsUncertainUnreadableReceiptDoesNotSend(t *testing.T) {
	ctx := context.Background()
	service, base, token := systemTestService(t)
	service.EncryptionKey = []byte(strings.Repeat("e", 32))
	claims, err := service.JWT.Verify(token, "access")
	if err != nil {
		t.Fatal(err)
	}
	subject := service.root("system", "node", "comms-node")
	base.seed(service.root("core", "role", "CommsSender"), RoleState{ID: "CommsSender", Network: service.Network, Principal: claims.Context.Principal, Subject: subject, Permissions: []string{"comms:email:send"}})
	base.seed(claims.Context.Principal, PrincipalState{Roles: []RoleGrant{{ID: "CommsSender", Subject: subject}}})
	provider := &systemTestMailer{}
	service.Mail = provider
	failing := &receiptErrorStore{systemTestStore: base, commit: true, appendErr: eventstore.ErrUncertain, lookupErr: errors.New("receipt unavailable")}
	service.Store = failing
	message := Mail{From: "sender@example.com", To: "recipient@example.com", Subject: "hello", Body: "private"}
	ref, err := service.SendMailCommand(ctx, token, "access", "mail", subject, message)
	if err != eventstore.ErrUncertain || ref.ID == "" || provider.calls != 0 || failing.appends != 1 {
		t.Fatal("unproved preparation submitted mail", err, provider.calls)
	}
	service.Store = base
	repeated, err := service.SendMailCommand(ctx, token, "access", "mail", subject, message)
	if err != nil || repeated != ref || provider.calls != 1 {
		t.Fatal("explicit recovery failed", err)
	}
}
