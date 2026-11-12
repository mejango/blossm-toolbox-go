//go:build integration

package wallet

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/evm"
	mongoStore "github.com/mejango/blossm-toolbox-go/storage/mongo"
	"github.com/mejango/blossm-toolbox-go/storage/postgres"
)

type nativeFixture struct {
	kind, uri, namespace string
	backend              eventstore.Backend
}

func newNative(t *testing.T, kind string) *nativeFixture {
	t.Helper()
	name := "POSTGRES_URL"
	if kind == "mongo" {
		name = "MONGODB_URI"
	}
	uri := os.Getenv(name)
	if uri == "" {
		t.Fatal(name + " required")
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal("invalid native fixture URI")
	}
	switch parsed.Hostname() {
	case "localhost", "127.0.0.1", "::1":
	default:
		t.Fatal("native wallet tests require a loopback database fixture")
	}
	fixture := &nativeFixture{kind: kind, uri: uri, namespace: fmt.Sprintf("wallet_%d", time.Now().UnixNano())}
	t.Cleanup(func() {
		if fixture.backend != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := fixture.backend.Close(ctx); err != nil {
				t.Error(err)
			}
		}
	})
	return fixture
}
func (f *nativeFixture) open(t *testing.T) *eventstore.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if f.backend != nil {
		if err := f.backend.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if f.kind == "postgres" {
		f.backend, err = postgres.Open(ctx, f.uri, f.namespace)
	} else {
		f.backend, err = mongoStore.Open(ctx, mongoStore.Config{URI: f.uri, Database: "blossm_test", Namespace: f.namespace, MaxAttempts: 20})
	}
	if err != nil {
		t.Fatal(err)
	}
	reducers := dia.Reducers()
	for name, reducer := range Reducers() {
		reducers[name] = reducer
	}
	return &eventstore.Store{Backend: f.backend, Reducers: reducers}
}
func nativeService(t *testing.T, store *eventstore.Store) (Service, *dia.Service, *fixtureRPC) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := &dia.Service{Store: store, Network: "local", Mail: &walletMail{}, HMACKey: bytes.Repeat([]byte{31}, 32), EncryptionKey: bytes.Repeat([]byte{32}, 32), JWT: &dia.JWT{Signer: dia.LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "dia.local", Audience: "local"}}
	cipher, err := evm.NewAESGCMCipher(bytes.Repeat([]byte{73}, 32))
	if err != nil {
		t.Fatal(err)
	}
	node := &fixtureRPC{}
	node.pending.Store(7)
	return Service{Auth: auth, Store: store, Keys: cipher, Chains: map[string]evm.Engine{"1": {RPC: node, ChainID: big.NewInt(1)}, "10": {RPC: node, ChainID: big.NewInt(10)}}}, auth, node
}
func ownerToken(t *testing.T, ctx context.Context, auth *dia.Service, label string) string {
	t.Helper()
	secret, err := dia.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	session, err := auth.StartSession(ctx, secret, dia.Device{Type: "desktop", Platform: "browser", Agent: "wallet-native-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	scene, err := auth.RegisterScene(ctx, session.Token, "scene-"+label, eventstore.Root{Network: "local", Service: "example", Domain: "workspace", ID: label})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = auth.AuthenticateAccess(ctx, scene.Token); err != nil {
		t.Fatal(err)
	}
	// A valid scene token is still anonymous and must fail every custody entry point.
	anonymous := Service{Auth: auth, Store: auth.Store.(*eventstore.Store), Keys: failedKeys{}}
	assertCustodyForbidden(t, ctx, anonymous, scene.Token)
	challenge, err := auth.SaveSession(ctx, scene.Token, "save-"+label, label+"@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	code := strings.Fields(auth.Mail.(*walletMail).last.Body)[0]
	upgraded, err := auth.AnswerChallenge(ctx, challenge.Token, "answer-"+label, code)
	if err != nil {
		t.Fatal(err)
	}
	return upgraded.Token
}

func TestNativeCustodialJourney(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			fixture := newNative(t, kind)
			store := fixture.open(t)
			service, auth, node := nativeService(t, store)
			token := ownerToken(t, ctx, auth, "owner")
			otherToken := ownerToken(t, ctx, auth, "stranger")
			ownerClaims, err := auth.AuthenticateAccess(ctx, token)
			if err != nil {
				t.Fatal(err)
			}
			otherClaims, err := auth.AuthenticateAccess(ctx, otherToken)
			if err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(*dia.Claims){
				func(c *dia.Claims) { c.Context.Account = otherClaims.Context.Account },
				func(c *dia.Claims) { c.Context.Principal = otherClaims.Context.Principal },
				func(c *dia.Claims) { c.Context.Account.ID = "missing-account" },
				func(c *dia.Claims) { c.Context.Principal.ID = "missing-principal" },
				func(c *dia.Claims) { c.Context.Account.Network = "foreign" },
				func(c *dia.Claims) { c.Context.Principal.Service = "foreign" },
			} {
				claims := ownerClaims
				mutate(&claims)
				invalid := service
				invalid.Auth = fixtureClaims{claims}
				assertCustodyForbidden(t, ctx, invalid, "authenticated-fixture")
			}
			address, err := service.Create(ctx, token, "primary")
			if err != nil {
				t.Fatal(err)
			}
			replay, err := service.Create(ctx, token, "primary")
			if err != nil || replay != address {
				t.Fatal("custody retry changed wallet", replay, err)
			}
			if _, err = service.Prepare(ctx, otherToken, address.ID, "1", transfer("attempt")); !errors.Is(err, dia.ErrForbidden) {
				t.Fatal("stranger accessed custody", err)
			}
			if _, err = service.Prepare(ctx, "invalid-token", address.ID, "1", transfer("attempt")); !errors.Is(err, dia.ErrUnauthorized) {
				t.Fatal("invalid access token accepted", err)
			}
			for _, key := range []string{"", "  "} {
				if _, err = service.Prepare(ctx, token, address.ID, "1", transfer(key)); !errors.Is(err, dia.ErrInvalid) {
					t.Fatal("empty original key hashed", err)
				}
			}
			// Above safe53, the signed tx nonce and durable payload still retain every bit.
			nonce := uint64(1<<53) + 17
			node.pending.Store(nonce)
			prepared, err := service.Prepare(ctx, token, address.ID, "1", transfer("payment"))
			if err != nil || prepared.Nonce != nonce {
				t.Fatal("prepare", prepared, err)
			}
			var tx types.Transaction
			if err = tx.UnmarshalBinary(prepared.Raw); err != nil {
				t.Fatal(err)
			}
			sender, err := types.Sender(types.LatestSignerForChainID(big.NewInt(1)), &tx)
			if err != nil || sender != address.Address || tx.Hash() != prepared.Hash {
				t.Fatal("actual local signature mismatch", err)
			}
			journal := Journal{Store: store, Network: "local"}
			saved, err := journal.Get(ctx, prepared.ID)
			if err != nil || !bytes.Equal(saved.Raw, prepared.Raw) {
				t.Fatal("signed bytes not durable", err)
			}
			if _, err = service.Submit(ctx, token, address.ID, "10", prepared.ID); !errors.Is(err, dia.ErrInvalid) {
				t.Fatal("wrong chain accepted", err)
			}
			alternate, err := service.Create(ctx, token, "secondary")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.Submit(ctx, token, alternate.ID, "1", prepared.ID); !errors.Is(err, dia.ErrForbidden) {
				t.Fatal("different wallet could submit", err)
			}
			node.onSend = func(sent *types.Transaction) error {
				fromDisk, err := journal.Get(ctx, prepared.ID)
				if err != nil || fromDisk.Hash != sent.Hash() {
					t.Error("broadcast preceded durable commit", err)
				}
				raw, _ := sent.MarshalBinary()
				if !bytes.Equal(raw, prepared.Raw) {
					t.Error("retry changed transaction")
				}
				return errors.New("provider accepted transaction, connection reset")
			}
			observed, err := service.Submit(ctx, token, address.ID, "1", prepared.ID)
			if err == nil || observed.State != evm.Unknown {
				t.Fatal("uncertain send marked terminal", observed, err)
			}
			observationRoot := root("local", "observation", prepared.ID)
			state, err := store.Aggregate(ctx, observationRoot)
			if err != nil {
				t.Fatal(err)
			}
			var durable evm.Observation
			if err = json.Unmarshal(state.State, &durable); err != nil || durable != observed {
				t.Fatal("unknown outcome not recorded", durable, err)
			}
			// Reopen the actual database adapter, rebuild services, retain only durable state.
			store = fixture.open(t)
			auth.Store = store
			service.Store = store
			journal.Store = store
			service.Keys = failedKeys{} // recovery can succeed while custody decrypt is unavailable
			node.pending.Store(0)
			again, err := service.Prepare(ctx, token, address.ID, "1", transfer("payment"))
			if err != nil || !bytes.Equal(again.Raw, prepared.Raw) {
				t.Fatal("restart re-signed or decrypted", err)
			}
			node.onSend = func(sent *types.Transaction) error {
				if sent.Hash() != prepared.Hash {
					t.Error("retry hash changed")
				}
				return nil
			}
			observed, err = service.Submit(ctx, token, address.ID, "1", prepared.ID)
			if err != nil || observed.State != evm.Pending || node.sends.Load() != 2 {
				t.Fatal("durable resubmit failed", observed, err)
			}
			if next, err := journal.NextNonce(ctx, "1", address.Address, 0); err != nil || next != nonce+1 {
				t.Fatal("stale pending reset durable frontier", next, err)
			}
			number := uint64(1<<53) + 100
			node.block = &types.Header{Number: new(big.Int).SetUint64(number), Time: 1000}
			node.head = &types.Header{Number: new(big.Int).SetUint64(number + 1), Time: 1012, ParentHash: node.block.Hash()}
			node.receipt = &types.Receipt{TxHash: prepared.Hash, BlockNumber: new(big.Int).SetUint64(number), BlockHash: node.block.Hash(), Status: types.ReceiptStatusSuccessful}
			policy := evm.PollPolicy{Attempts: 2, Timeout: time.Second, Confirmations: 2}
			observed, err = service.Reconcile(ctx, token, address.ID, "1", prepared.ID, policy)
			if err != nil || observed.State != evm.Succeeded || observed.BlockNumber != number {
				t.Fatal("reconcile", observed, err)
			}
			before, err := store.Aggregate(ctx, observationRoot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = service.Reconcile(ctx, token, address.ID, "1", prepared.ID, policy); err != nil {
				t.Fatal(err)
			}
			after, err := store.Aggregate(ctx, observationRoot)
			if err != nil || after.Version != before.Version || !bytes.Equal(after.State, before.State) {
				t.Fatal("identical observation replay changed state", err)
			}
			node.receipt.Status = types.ReceiptStatusFailed
			observed, err = service.Reconcile(ctx, token, address.ID, "1", prepared.ID, policy)
			if err != nil || observed.State != evm.Failed {
				t.Fatal("confirmed revert not distinguished", observed, err)
			}
			// Neither accepted retries nor uncertain sends may erase receipt evidence.
			for _, terminal := range []evm.State{evm.Succeeded, evm.Failed} {
				if terminal == evm.Succeeded {
					node.receipt.Status = types.ReceiptStatusSuccessful
				} else {
					node.receipt.Status = types.ReceiptStatusFailed
				}
				confirmed, err := service.Reconcile(ctx, token, address.ID, "1", prepared.ID, policy)
				if err != nil {
					t.Fatal(err)
				}
				before, err := store.Aggregate(ctx, observationRoot)
				if err != nil {
					t.Fatal(err)
				}
				for _, sendErr := range []error{nil, errors.New("nonce too low")} {
					node.onSend = func(*types.Transaction) error { return sendErr }
					retried, err := service.Submit(ctx, token, address.ID, "1", prepared.ID)
					if retried != confirmed || (sendErr != nil && err == nil) || (sendErr == nil && err != nil) {
						t.Fatal("submit replaced receipt evidence", retried, err)
					}
					after, err := store.Aggregate(ctx, observationRoot)
					if err != nil || before.Version != after.Version || !bytes.Equal(before.State, after.State) {
						t.Fatal("submit changed confirmed journal", err)
					}
				}
			}
			// Authentication remains live, not only an address-ID check.
			if err = auth.TerminateSession(ctx, token, "terminate-wallet-session"); err != nil {
				t.Fatal(err)
			}
			if _, err = service.Submit(ctx, token, address.ID, "1", prepared.ID); !errors.Is(err, dia.ErrUnauthorized) {
				t.Fatal("terminated session retained custody access", err)
			}
		})
	}
}

type failedKeys struct{}

func (failedKeys) Seal(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errors.New("custody service unavailable")
}
func (failedKeys) Open(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errors.New("custody service unavailable")
}

var errNonceWrite = errors.New("injected failure during nonce event write")

type failNonceBackend struct {
	eventstore.Backend
	written *bool
}
type failNonceTx struct {
	eventstore.Tx
	written *bool
}

func (b failNonceBackend) Update(ctx context.Context, fn func(eventstore.Tx) error) error {
	return b.Backend.Update(ctx, func(tx eventstore.Tx) error { return fn(failNonceTx{Tx: tx, written: b.written}) })
}
func (tx failNonceTx) PutEvent(ctx context.Context, event eventstore.Event) error {
	if event.Input.Root.Domain == "nonce" {
		return errNonceWrite
	}
	if err := tx.Tx.PutEvent(ctx, event); err != nil {
		return err
	}
	if event.Input.Root.Domain == "transaction" {
		*tx.written = true
	}
	return nil
}
func TestNativeJournalAtomicity(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			fixture := newNative(t, kind)
			store := fixture.open(t)
			journal := Journal{Store: store, Network: "local"}
			key, err := crypto.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			defer key.D.SetInt64(0)
			first := signedFixture(t, key, "first", 7)
			second := signedFixture(t, key, "second", 7)
			// Failure after the signed transaction row was written must roll back that row,
			// nonce counter and notification outbox in the real database transaction.
			written := false
			failingStore := *store
			failingStore.Backend = failNonceBackend{Backend: store.Backend, written: &written}
			if err = (Journal{Store: &failingStore, Network: "local"}).Put(ctx, first); !errors.Is(err, errNonceWrite) || !written {
				t.Fatal("failure did not exercise partially written transaction", written, err)
			}
			if _, err = journal.Get(ctx, first.ID); !errors.Is(err, evm.ErrNotFound) {
				t.Fatal("rolled back signed bytes survived", err)
			}
			if next, err := journal.NextNonce(ctx, "1", first.From, 0); err != nil || next != 0 {
				t.Fatal("rolled back nonce survived", next, err)
			}
			if err = store.Backend.View(ctx, func(reader eventstore.Reader) error {
				pending, err := reader.Pending(ctx, 100)
				if err == nil && len(pending) != 0 {
					return errors.New("rolled back publication survived")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// Two processes race to assign one nonce; exactly one signed record survives.
			type outcome struct {
				p   evm.Prepared
				err error
			}
			results := make(chan outcome, 2)
			gate := make(chan struct{})
			var wg sync.WaitGroup
			for _, p := range []evm.Prepared{first, second} {
				wg.Add(1)
				go func(p evm.Prepared) { defer wg.Done(); <-gate; results <- outcome{p, journal.Put(ctx, p)} }(p)
			}
			close(gate)
			wg.Wait()
			close(results)
			var winner, loser evm.Prepared
			wins := 0
			for result := range results {
				if result.err == nil {
					wins++
					winner = result.p
				} else if errors.Is(result.err, evm.ErrNonceConflict) {
					loser = result.p
				} else {
					t.Fatal("unexpected collision error", result.err)
				}
			}
			if wins != 1 || loser.ID == "" {
				t.Fatal("nonce uniqueness failed", wins)
			}
			if _, err = journal.Get(ctx, loser.ID); !errors.Is(err, evm.ErrNotFound) {
				t.Fatal("nonce loser leaked immutable tx", err)
			}
			if err = journal.Put(ctx, winner); err != nil {
				t.Fatal("exact retry failed", err)
			}
			if next, err := journal.NextNonce(ctx, "1", winner.From, 0); err != nil || next != 8 {
				t.Fatal("nonce frontier", next, err)
			}
			changed := winner
			changed.Intent = common.HexToHash("0x1234")
			if err = journal.Put(ctx, changed); !errors.Is(err, evm.ErrConflict) {
				t.Fatal("changed intent reused ID", err)
			}
			// Reopening proves hash/raw bytes and nonce assignment are not process state.
			journal.Store = fixture.open(t)
			recovered, err := journal.Get(ctx, winner.ID)
			if err != nil || !bytes.Equal(recovered.Raw, winner.Raw) {
				t.Fatal("journal restart", err)
			}
			if next, err := journal.NextNonce(ctx, "1", winner.From, 0); err != nil || next != 8 {
				t.Fatal("restart frontier", next, err)
			}
		})
	}
}

// Mail is captured locally; the real Dia challenge still proves the account transition.
type walletMail struct{ last dia.Mail }

func (m *walletMail) Send(_ context.Context, message dia.Mail) error { m.last = message; return nil }

type fixtureClaims struct{ claims dia.Claims }

func (f fixtureClaims) AuthenticateAccess(context.Context, string) (dia.Claims, error) {
	return f.claims, nil
}
func assertCustodyForbidden(t *testing.T, ctx context.Context, s Service, token string) {
	t.Helper()
	checks := []func() error{
		func() error { _, e := s.Create(ctx, token, "anonymous"); return e },
		func() error { _, e := s.Prepare(ctx, token, "wallet", "1", transfer("anonymous")); return e },
		func() error { _, e := s.Submit(ctx, token, "wallet", "1", "transaction"); return e },
		func() error {
			_, e := s.Reconcile(ctx, token, "wallet", "1", "transaction", evm.PollPolicy{})
			return e
		},
	}
	for i, check := range checks {
		if err := check(); !errors.Is(err, dia.ErrForbidden) {
			t.Fatalf("custody operation %d accepted unbound principal: %v", i, err)
		}
	}
}
