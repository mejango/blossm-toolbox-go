package dia

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/mejango/blossm-toolbox-go/eventstore"
)

type NodeInput struct {
	Network string `json:"network"`
}
type NodeResult struct {
	Node      Ref    `json:"node"`
	Scene     Ref    `json:"scene"`
	Group     Ref    `json:"group"`
	Principal Ref    `json:"principal"`
	Token     string `json:"token"`
}

// RegisterNode commits its scene, membership, privileges and session context together.
func (s *Service) RegisterNode(ctx context.Context, token, key string, in NodeInput) (NodeResult, error) {
	var result NodeResult
	if validKey(key) != nil || strings.TrimSpace(in.Network) == "" || len(in.Network) > 253 {
		return result, ErrInvalid
	}
	c, err := s.authenticate(ctx, token, "access")
	recovering := err != nil
	if recovering {
		if s.JWT == nil {
			return result, err
		}
		c, err = s.JWT.Verify(token, "access")
		if err != nil {
			return result, ErrUnauthorized
		}
	}
	operation := stableID(c.Context.Session.Key(), "node/"+key)
	result.Node = s.root("system", "node", stableID(operation, "node"))
	result.Scene = s.root("core", "scene", stableID(operation, "scene"))
	result.Group = s.root("core", "group", stableID(operation, "group"))
	result.Principal = c.Context.Principal
	if result.Principal.ID == "" {
		result.Principal = s.root("core", "principal", stableID(operation, "principal"))
	}
	sc := c.Context
	sc.Principal = result.Principal
	sc.Scene = result.Scene
	sc.Subject = result.Node
	var node NodeState
	a, err := s.load(ctx, result.Node, &node)
	if err != nil && !errors.Is(err, eventstore.ErrNotFound) {
		return NodeResult{}, err
	}
	if a.Version > 0 {
		if node.Network != in.Network || node.Scene != result.Scene {
			return NodeResult{}, eventstore.ErrIdempotency
		}
		result.Token = token
		if recovering {
			result.Token, err = s.recoverAccess(ctx, token, sc)
			if err != nil {
				return NodeResult{}, err
			}
			c, err = s.authenticate(ctx, result.Token, "access")
			if err != nil {
				return NodeResult{}, err
			}
		}
		if c.Context.Principal != result.Principal || c.Context.Scene != result.Scene {
			return NodeResult{}, ErrUnauthorized
		}
		if err = s.require(ctx, c, result.Node, "system:node:update"); err != nil {
			return NodeResult{}, err
		}
		return result, nil
	}
	if recovering {
		return NodeResult{}, ErrUnauthorized
	}
	var principal PrincipalState
	pa, err := s.load(ctx, result.Principal, &principal)
	if err != nil && !errors.Is(err, eventstore.ErrNotFound) {
		return NodeResult{}, err
	}
	events := []eventstore.Input{}
	grants := []RoleGrant{{ID: "SceneAdmin", Subject: result.Scene}, {ID: "GroupAdmin", Subject: result.Group}, {ID: "NodeAdmin", Subject: result.Node}}
	events = append(events, s.event(result.Principal, "add-roles", operation+"/roles", pa.Version, map[string]any{"roles": grants}),
		s.event(result.Scene, "register", operation+"/scene", 0, SceneState{Subject: result.Node, Group: result.Group}),
		s.event(result.Group, "add-principals", operation+"/group", 0, map[string]any{"principals": []Ref{result.Principal}}),
		s.event(result.Group, "add-networks", operation+"/networks", 1, map[string]any{"networks": []string{in.Network}}),
		s.event(result.Node, "register", operation+"/node", 0, NodeState{Network: in.Network, Scene: result.Scene}))
	var session SessionState
	sa, err := s.load(ctx, c.Context.Session, &session)
	if err != nil {
		return NodeResult{}, err
	}
	events = append(events, s.event(c.Context.Session, "upgrade", operation+"/session", sa.Version, map[string]Ref{"principal": result.Principal, "scene": result.Scene}))
	if err = s.append(ctx, events...); err != nil {
		return NodeResult{}, err
	}
	result.Token, err = s.sign(ctx, c, "access", sc, c.Expires)
	if err != nil {
		return NodeResult{}, err
	}
	return result, nil
}

type KeyInput struct {
	Node  Ref      `json:"node"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}
type KeyResult struct {
	Key       Ref    `json:"key"`
	Principal Ref    `json:"principal"`
	Secret    string `json:"secret"`
}

// RegisterKey only delegates the retained NodeNetwork role, never administrative roles.
// The HMAC-derived secret permits recovery after an uncertain commit without storing plaintext.
// Derivation is namespaced by network, authenticated principal and request key.
// Rotating HMACKey prevents recovery of old secrets; existing keys remain usable
// with their original secrets until explicitly revoked.
func (s *Service) RegisterKey(ctx context.Context, token, kind, key string, in KeyInput) (KeyResult, error) {
	var result KeyResult
	if validKey(key) != nil || strings.TrimSpace(in.Name) == "" || len(in.Name) > 256 || in.Node != s.root("system", "node", in.Node.ID) || in.Node.ID == "" || len(in.Roles) == 0 {
		return result, ErrInvalid
	}
	for _, role := range in.Roles {
		if role != "NodeNetwork" {
			return result, ErrForbidden
		}
	}
	c, err := s.systemAuthority(ctx, token, kind, in.Node, "system:key:create")
	if err != nil {
		return result, err
	}
	var node NodeState
	na, err := s.load(ctx, in.Node, &node)
	if err != nil {
		return result, err
	}
	if na.Version == 0 {
		return result, eventstore.ErrNotFound
	}
	if len(s.HMACKey) < 32 {
		return result, fmt.Errorf("%w: key HMAC secret", ErrUnavailable)
	}
	operation := stableID(c.Context.Principal.Key(), "key/"+key)
	result.Key = s.root("system", "key", stableID(operation, "key"))
	result.Principal = s.root("core", "principal", stableID(operation, "principal"))
	mac := hmac.New(sha256.New, s.HMACKey)
	mac.Write([]byte("dia/api-key/v1\x00" + result.Key.Key()))
	result.Secret = hex.EncodeToString(mac.Sum(nil))
	digest := sha256.Sum256([]byte(result.Secret))
	state := KeyState{Name: in.Name, Network: node.Network, Secret: hex.EncodeToString(digest[:]), Scene: node.Scene, Principal: result.Principal, Subject: in.Node}
	var old KeyState
	a, err := s.load(ctx, result.Key, &old)
	if err != nil && !errors.Is(err, eventstore.ErrNotFound) {
		return KeyResult{}, err
	}
	if a.Version > 0 {
		if old.Revoked != "" {
			return KeyResult{}, ErrForbidden
		}
		if old != state {
			return KeyResult{}, eventstore.ErrIdempotency
		}
		return result, nil
	}
	err = s.append(ctx,
		s.event(result.Principal, "set-subject", operation+"/principal", 0, map[string]Ref{"subject": result.Key}),
		s.event(result.Principal, "add-roles", operation+"/roles", 1, map[string]any{"roles": []RoleGrant{{ID: "NodeNetwork", Subject: in.Node}}}),
		s.event(result.Key, "create", operation+"/key", 0, state))
	if err != nil {
		return KeyResult{}, err
	}
	return result, nil
}

// ReadKey never returns the stored verifier or the plaintext secret.
func (s *Service) ReadKey(ctx context.Context, token, kind string, key Ref) (KeyState, error) {
	c, err := s.systemClaims(ctx, token, kind)
	if err != nil {
		return KeyState{}, err
	}
	if key != s.root("system", "key", key.ID) || key.ID == "" {
		return KeyState{}, ErrInvalid
	}
	var state KeyState
	a, err := s.load(ctx, key, &state)
	if err != nil {
		return KeyState{}, err
	}
	if a.Version == 0 {
		return KeyState{}, eventstore.ErrNotFound
	}
	if err = s.require(ctx, c, state.Subject, "system:key:read"); err != nil {
		return KeyState{}, err
	}
	state.Secret = ""
	return state, nil
}

func (s *Service) RevokeKey(ctx context.Context, token, kind, requestKey string, key Ref) error {
	if validKey(requestKey) != nil || key != s.root("system", "key", key.ID) || key.ID == "" {
		return ErrInvalid
	}
	c, err := s.systemClaims(ctx, token, kind)
	if err != nil {
		return err
	}
	var state KeyState
	a, err := s.load(ctx, key, &state)
	if err != nil {
		return err
	}
	if a.Version == 0 {
		return eventstore.ErrNotFound
	}
	if err = s.require(ctx, c, state.Subject, "system:key:create"); err != nil {
		return err
	}
	if state.Revoked != "" {
		return nil
	}
	return s.append(ctx, s.event(key, "revoke", stableID(key.Key(), "revoke/"+requestKey), a.Version, map[string]string{"revoked": s.now().Format(time.RFC3339Nano)}))
}

type ConnectionInput struct {
	Key    Ref    `json:"key"`
	Secret string `json:"secret"`
}
type ConnectionResult struct {
	Token     string `json:"token"`
	Principal Ref    `json:"principal"`
	Key       Ref    `json:"key"`
}

func (s *Service) OpenConnection(ctx context.Context, in ConnectionInput) (ConnectionResult, error) {
	var result ConnectionResult
	if in.Key != s.root("system", "key", in.Key.ID) || in.Key.ID == "" || len(in.Secret) != 64 {
		return result, ErrUnauthorized
	}
	var key KeyState
	a, err := s.load(ctx, in.Key, &key)
	if err != nil {
		return result, ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(in.Secret))
	expected, decodeErr := hex.DecodeString(key.Secret)
	if a.Version == 0 || decodeErr != nil || subtle.ConstantTimeCompare(expected, digest[:]) != 1 || key.Revoked != "" {
		return result, ErrUnauthorized
	}
	if key.Subject != s.root("system", "node", key.Subject.ID) || key.Subject.ID == "" || key.Principal != s.root("core", "principal", key.Principal.ID) || key.Principal.ID == "" {
		return result, ErrUnauthorized
	}
	var principal PrincipalState
	pa, err := s.load(ctx, key.Principal, &principal)
	if err != nil || pa.Version == 0 || principal.Subject != in.Key {
		return result, ErrUnauthorized
	}
	sc := SessionContext{Network: s.Network, Principal: key.Principal, Scene: key.Scene, Subject: in.Key}
	c := Claims{ID: stableID(in.Key.Key(), "connection"), Context: sc}
	if err = s.require(ctx, c, key.Subject, "system:connection:create"); err != nil {
		return result, err
	}
	result.Token, err = s.sign(ctx, c, "connection", sc, s.now().Add(90*24*time.Hour).Unix())
	if err != nil {
		return ConnectionResult{}, err
	}
	result.Key = in.Key
	result.Principal = key.Principal
	return result, nil
}

// systemClaims deliberately does not infer a kind from an untrusted token.
func (s *Service) systemClaims(ctx context.Context, token, kind string) (Claims, error) {
	if kind != "access" && kind != "connection" {
		return Claims{}, ErrUnauthorized
	}
	return s.authenticate(ctx, token, kind)
}

func (s *Service) connectionKey(ctx context.Context, c Claims) (KeyState, error) {
	var key KeyState
	ref := c.Context.Subject
	if ref.ID == "" || ref != s.root("system", "key", ref.ID) {
		return key, ErrUnauthorized
	}
	a, err := s.load(ctx, ref, &key)
	if err != nil || a.Version == 0 || key.Revoked != "" || key.Principal != c.Context.Principal || key.Scene != c.Context.Scene || c.Subject != key.Principal.ID {
		return KeyState{}, ErrUnauthorized
	}
	var principal PrincipalState
	pa, err := s.load(ctx, key.Principal, &principal)
	if err != nil || pa.Version == 0 || principal.Subject != ref {
		return KeyState{}, ErrUnauthorized
	}
	return key, nil
}
func (s *Service) systemAuthority(ctx context.Context, token, kind string, subject Ref, permission string) (Claims, error) {
	c, err := s.systemClaims(ctx, token, kind)
	if err != nil {
		return c, err
	}
	if subject.ID == "" || subject.Service == "" || subject.Domain == "" || subject.Network != s.Network {
		return c, ErrForbidden
	}
	if err = s.require(ctx, c, subject, permission); err != nil {
		return c, err
	}
	return c, nil
}

func (s *Service) updatesSubject(ctx context.Context, c Claims, kind string) (Ref, error) {
	subject := c.Context.Subject
	if kind == "connection" || kind == "connection-updates" {
		var key KeyState
		if _, err := s.load(ctx, subject, &key); err != nil {
			return Ref{}, err
		}
		subject = key.Subject
	}
	if subject.ID == "" || subject.Network != s.Network {
		return Ref{}, ErrForbidden
	}
	return subject, s.require(ctx, c, subject, "system:updates:create")
}

func (s *Service) IssueUpdatesToken(ctx context.Context, token, kind string) (string, error) {
	c, err := s.systemClaims(ctx, token, kind)
	if err != nil {
		return "", err
	}
	if _, err = s.updatesSubject(ctx, c, kind); err != nil {
		return "", err
	}
	expiry := s.now().Add(time.Hour).Unix()
	if c.Expires < expiry {
		expiry = c.Expires
	}
	targetKind := "updates"
	if kind == "connection" {
		targetKind = "connection-updates"
	}
	return s.sign(ctx, c, targetKind, c.Context, expiry)
}

type ChannelInput struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

func updateChannel(subject Ref, in ChannelInput) (string, error) {
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 256 || len(in.Key) > 256 || strings.ContainsAny(in.Name+in.Key, "\r\n\x00") {
		return "", ErrInvalid
	}
	b, _ := json.Marshal(struct {
		Subject   Ref
		Name, Key string
	}{subject, in.Name, in.Key})
	return "dia-" + stableID(string(b), "updates-channel"), nil
}

// UpdatesChannel binds a subscription to its authorized subject, not an arbitrary provider channel.
func (s *Service) UpdatesChannel(ctx context.Context, token, kind string, in ChannelInput) (string, error) {
	if kind != "updates" && kind != "connection-updates" {
		return "", ErrUnauthorized
	}
	c, err := s.authenticate(ctx, token, kind)
	if err != nil {
		return "", err
	}
	subject, err := s.updatesSubject(ctx, c, kind)
	if err != nil {
		return "", err
	}
	return updateChannel(subject, in)
}

func (s *Service) PublishUpdate(ctx context.Context, token, kind string, in ChannelInput, update Update) error {
	c, err := s.systemClaims(ctx, token, kind)
	if err != nil {
		return err
	}
	subject, err := s.updatesSubject(ctx, c, kind)
	if err != nil {
		return err
	}
	channel, err := updateChannel(subject, in)
	if err != nil {
		return err
	}
	if update.ID == "" || (update.Type != "create" && update.Type != "update" && update.Type != "delete") || (len(update.View) > 0 && !json.Valid(update.View)) {
		return ErrInvalid
	}
	if s.Updates == nil {
		return ErrUnavailable
	}
	return s.Updates.Publish(ctx, channel, update)
}

// Comms calls report provider errors directly. A provider timeout is an uncertain
// delivery; the caller must reconcile it before retrying (these interfaces carry no provider idempotency key).
func (s *Service) SendMail(ctx context.Context, token, kind string, subject Ref, message Mail) error {
	if _, err := s.systemAuthority(ctx, token, kind, subject, "comms:email:send"); err != nil {
		return err
	}
	if err := validateSystemMail(message); err != nil {
		return err
	}
	if s.Mail == nil {
		return ErrUnavailable
	}
	return s.Mail.Send(ctx, message)
}
func (s *Service) SendSMS(ctx context.Context, token, kind string, subject Ref, to, message string) error {
	var normalizeErr error
	to, normalizeErr = NormalizePhoneNumber(to)
	if normalizeErr != nil {
		return normalizeErr
	}
	if _, err := s.systemAuthority(ctx, token, kind, subject, "comms:sms:send"); err != nil {
		return err
	}
	if err := validateSystemSMS(to, message); err != nil {
		return err
	}
	if s.SMS == nil {
		return ErrUnavailable
	}
	return s.SMS.Send(ctx, to, message)
}

func validateSystemMail(message Mail) error {
	for _, v := range []string{message.From, message.To, message.Subject} {
		if strings.ContainsAny(v, "\r\n\x00") {
			return ErrInvalid
		}
	}
	if _, err := mail.ParseAddress(message.From); err != nil {
		return ErrInvalid
	}
	if _, err := mail.ParseAddress(message.To); err != nil {
		return ErrInvalid
	}
	if message.Subject == "" || message.Body == "" {
		return ErrInvalid
	}

	return nil
}

func validateSystemSMS(to, message string) error {
	if len(to) < 8 || len(to) > 16 || to[0] != '+' || strings.TrimSpace(message) == "" {
		return ErrInvalid
	}
	for _, r := range to[1:] {
		if r < '0' || r > '9' {
			return ErrInvalid
		}
	}

	return nil
}

// SendMailCommand records an encrypted intent before sending. An explicit repeat
// resends the same persisted message; SMTP cannot guarantee exactly-once delivery.
// The returned reference identifies the prepared intent even if delivery fails.
func (s *Service) SendMailCommand(ctx context.Context, token, kind, key string, subject Ref, message Mail) (Ref, error) {
	if err := validateSystemMail(message); err != nil {
		return Ref{}, err
	}
	r, data, err := s.prepareComms(ctx, token, kind, key, subject, "email", message)
	if err != nil {
		return r, err
	}
	var persisted Mail
	if err = decode(data, &persisted); err != nil {
		return r, err
	}
	return r, s.SendMail(ctx, token, kind, subject, persisted)
}

type systemSMSMessage struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

// SendSMSCommand has the same explicit resend semantics as SendMailCommand.
func (s *Service) SendSMSCommand(ctx context.Context, token, kind, key string, subject Ref, to, message string) (Ref, error) {
	var normalizeErr error
	to, normalizeErr = NormalizePhoneNumber(to)
	if normalizeErr != nil {
		return Ref{}, normalizeErr
	}
	if err := validateSystemSMS(to, message); err != nil {
		return Ref{}, err
	}
	r, data, err := s.prepareComms(ctx, token, kind, key, subject, "sms", systemSMSMessage{To: to, Message: message})
	if err != nil {
		return r, err
	}
	var persisted systemSMSMessage
	if err = decode(data, &persisted); err != nil {
		return r, err
	}
	return r, s.SendSMS(ctx, token, kind, subject, persisted.To, persisted.Message)
}

func (s *Service) prepareComms(ctx context.Context, token, kind, key string, subject Ref, domain string, payload any) (Ref, []byte, error) {
	if validKey(key) != nil {
		return Ref{}, nil, ErrInvalid
	}
	c, err := s.systemAuthority(ctx, token, kind, subject, "comms:"+domain+":send")
	if err != nil {
		return Ref{}, nil, err
	}
	if len(s.HMACKey) < 32 {
		return Ref{}, nil, fmt.Errorf("%w: comms HMAC key", ErrUnavailable)
	}
	operation := stableID(c.Context.Principal.Key(), "comms/"+domain+"/"+key)
	r := s.root("comms", domain, operation)
	data, err := json.Marshal(payload)
	if err != nil {
		return r, nil, err
	}
	mac := hmac.New(sha256.New, s.HMACKey)
	mac.Write([]byte("dia/comms/v1\x00" + r.Key() + "\x00" + subject.Key() + "\x00"))
	mac.Write(data)
	digest := hex.EncodeToString(mac.Sum(nil))
	var state CommsState
	a, err := s.load(ctx, r, &state)
	if err != nil {
		return r, nil, err
	}
	if a.Version == 0 {
		encrypted, err := Seal(s.EncryptionKey, data)
		if err != nil {
			return r, nil, fmt.Errorf("%w: comms encryption", ErrUnavailable)
		}
		state = CommsState{Delivery: hex.EncodeToString(encrypted), Subject: subject, RequestHash: digest}
		if err = s.append(ctx, s.event(r, "prepare", operation+"/prepare", 0, state)); err != nil {
			return r, nil, err
		}
		if _, err = s.load(ctx, r, &state); err != nil {
			return r, nil, err
		}
	}
	if state.Subject != subject || state.RequestHash != digest {
		return r, nil, eventstore.ErrIdempotency
	}
	ciphertext, err := hex.DecodeString(state.Delivery)
	if err != nil {
		return r, nil, err
	}
	plaintext, err := Open(s.EncryptionKey, ciphertext)
	if err != nil {
		return r, nil, err
	}
	if subtle.ConstantTimeCompare(plaintext, data) != 1 {
		return r, nil, eventstore.ErrIdempotency
	}
	return r, plaintext, nil
}
