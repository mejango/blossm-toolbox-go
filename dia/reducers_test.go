package dia

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mejango/blossm-toolbox-go/eventstore"
)

func reducerJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func reducerRef(id string) Ref {
	return Ref{Network: "test", Service: "core", Domain: "principal", ID: id}
}
func reduceTest(t *testing.T, r eventstore.Reducer, state json.RawMessage, action string, payload any) json.RawMessage {
	t.Helper()
	out, e := r.Apply(state, eventstore.Event{Input: eventstore.Input{Action: action, Payload: reducerJSON(t, payload)}, Number: 0})
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func TestReducerActions(t *testing.T) {
	ref := reducerRef("one")
	role := RoleGrant{ID: "admin", Subject: ref}
	cases := []struct {
		domain, action string
		payload        any
	}{
		{"core/session", "start", SessionState{Device: Device{Type: "desktop", Platform: "browser"}, Started: 10, Expires: 20}},
		{"core/session", "upgrade", map[string]Ref{"principal": ref, "account": ref, "scene": ref}},
		{"core/session", "change-scene", map[string]Ref{"scene": ref}},
		{"core/session", "terminate", map[string]string{"terminated": "now"}},
		{"core/account", "register", AccountState{EmailHMAC: "hmac", EmailEncrypted: "ciphertext", Principal: ref}},
		{"core/account", "set-password", map[string]string{"password": "hash"}},
		{"core/challenge", "issue", ChallengeState{Code: "hash", Expires: 20, Session: ref, Delivery: "encrypted-mail"}},
		{"core/challenge", "answer", map[string]string{"answered": "now"}},
		{"core/principal", "set-subject", map[string]Ref{"subject": ref}},
		{"core/principal", "add-roles", map[string]any{"roles": []RoleGrant{role, role}}},
		{"core/principal", "remove-roles", map[string]any{"roles": []RoleGrant{role, role}}},
		{"core/group", "add-principals", map[string]any{"principals": []Ref{ref, ref}}},
		{"core/group", "remove-principals", map[string]any{"principals": []Ref{ref, ref}}},
		{"core/group", "add-networks", map[string]any{"networks": []string{"test", "test"}}},
		{"core/group", "remove-networks", map[string]any{"networks": []string{"test", "test"}}},
		{"core/scene", "register", SceneState{Subject: ref, Group: ref}},
		{"core/scene", "delete", map[string]string{"deleted": "now"}},
		{"core/role", "create", RoleState{ID: "admin", Network: "test", Permissions: []string{"read", "read"}, Principal: ref, Subject: ref}},
		{"core/role", "add-permissions", map[string]any{"permissions": []string{"read", "read"}}},
		{"core/role", "remove-permissions", map[string]any{"permissions": []string{"read", "read"}}},
		{"system/node", "register", NodeState{Network: "test", Scene: ref}},
		{"system/key", "create", KeyState{Name: "key", Network: "test", Secret: "hash", Scene: ref, Principal: ref, Subject: ref}},
		{"system/key", "revoke", map[string]string{"revoked": "now"}},
		{"comms/email", "send-auth-code", Mail{From: "from@example.org", To: "to@example.org", Subject: "auth", Body: "redacted"}},
		{"comms/sms", "send", map[string]string{"to": "123", "message": "hello"}},
	}
	reducers := Reducers()
	if len(reducers) != 12 {
		t.Fatalf("got %d domains", len(reducers))
	}
	for _, tc := range cases {
		t.Run(tc.domain+"/"+tc.action, func(t *testing.T) {
			r := reducers[tc.domain]
			raw := reducerJSON(t, tc.payload)
			normalized, e := r.Normalize(tc.action, raw)
			if e != nil {
				t.Fatal(e)
			}
			again, e := r.Normalize(tc.action, normalized)
			if e != nil || !bytes.Equal(normalized, again) {
				t.Fatalf("unstable normalization: %s %v", again, e)
			}
			event := eventstore.Event{Input: eventstore.Input{Action: tc.action, Payload: normalized}, Number: 0}
			first, e := r.Apply(nil, event)
			if e != nil {
				t.Fatal(e)
			}
			second, e := r.Apply(nil, event)
			if e != nil || !bytes.Equal(first, second) {
				t.Fatalf("nondeterministic apply: %s %v", second, e)
			}
			if tc.action != "answer" {
				twice, e := r.Apply(first, event)
				if e != nil || !bytes.Equal(first, twice) {
					t.Fatalf("duplicate changed state: %s %v", twice, e)
				}
			}
			bads := []string{"null", "[]", "{}", string(raw) + " {}", string(raw[:len(raw)-1]) + `,"unknown":true}`}
			for _, bad := range bads {
				if _, e := r.Normalize(tc.action, []byte(bad)); e == nil {
					t.Errorf("accepted %s", bad)
				}
			}
			event.Input.Payload = []byte(`{"unknown":true}`)
			if _, e := r.Apply(first, event); e == nil {
				t.Error("Apply bypassed validation")
			}
			if _, e := r.Normalize("unsupported", raw); e == nil {
				t.Error("accepted unknown action")
			}
			event.Input.Action = "unsupported"
			if _, e := r.Apply(first, event); e == nil {
				t.Error("Apply accepted unknown action")
			}
		})
	}
}

func TestReducerStrictPayloads(t *testing.T) {
	cases := []struct{ domain, action, payload string }{
		{"core/session", "start", `{"device":{"type":"watch"},"started":1,"expires":2}`},
		{"core/session", "start", `{"device":{"platform":"unknown"},"started":1,"expires":2}`},
		{"core/session", "start", `{"device":{"unknown":"value"},"started":1,"expires":2}`},
		{"core/session", "start", `{"device":{},"started":2,"expires":2}`},
		{"core/session", "start", `{"device":{},"started":"1","expires":2}`},
		{"core/session", "start", `{"device":{},"started":1,"expires":2,"terminated":"now"}`},
		{"core/session", "upgrade", `{"principal":{"id":"missing-address"}}`},
		{"core/session", "upgrade", `{"principal":null}`},
		{"core/session", "upgrade", `{"principal":{"id":"id","domain":"principal","service":"core","network":"test","root":"legacy"}}`},
		{"core/group", "add-networks", `{"networks":null}`},
		{"core/group", "add-networks", `{"networks":[""]}`},
		{"core/group", "add-principals", `{"principals":[{}]}`},
		{"core/principal", "add-roles", `{"roles":[{"id":"admin","subject":{}}]}`},
		{"core/role", "create", `{"id":"admin","network":"test","permissions":["read"]}`},
		{"core/role", "add-permissions", `{"permissions":[1]}`},
		{"core/challenge", "answer", `{"answered":""}`},
		{"comms/sms", "send", `{"to":"123","message":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.domain+"/"+tc.action, func(t *testing.T) {
			if _, e := Reducers()[tc.domain].Normalize(tc.action, []byte(tc.payload)); e == nil {
				t.Fatalf("accepted %s", tc.payload)
			}
		})
	}
}

func TestReducerCollectionsAndPreservation(t *testing.T) {
	r := Reducers()
	a, b := reducerRef("a:with:delimiters"), reducerRef("b")
	// Domain is part of identity even if all other fields match.
	c := a
	c.Domain = "scene"
	group := reduceTest(t, r["core/group"], nil, "add-networks", map[string]any{"networks": []string{"a", "b", "a"}})
	before := append([]byte(nil), group...)
	group = reduceTest(t, r["core/group"], group, "add-principals", map[string]any{"principals": []Ref{a, b, a, c}})
	if !bytes.Equal(before, reduceTest(t, r["core/group"], nil, "add-networks", map[string]any{"networks": []string{"a", "b", "a"}})) {
		t.Fatal("input mutated")
	}
	group = reduceTest(t, r["core/group"], group, "remove-principals", map[string]any{"principals": []Ref{a, a}})
	group = reduceTest(t, r["core/group"], group, "remove-networks", map[string]any{"networks": []string{"a"}})
	var gs GroupState
	if e := decode(group, &gs); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(gs.Principals, []Ref{b, c}) || !reflect.DeepEqual(gs.Networks, []string{"b"}) {
		t.Fatalf("wrong group %s", group)
	}
	roles := []RoleGrant{{ID: "role:one", Subject: a}, {ID: "role:one", Subject: c}}
	principal := reduceTest(t, r["core/principal"], nil, "set-subject", map[string]Ref{"subject": b})
	principal = reduceTest(t, r["core/principal"], principal, "add-roles", map[string]any{"roles": append(roles, roles[0])})
	principal = reduceTest(t, r["core/principal"], principal, "remove-roles", map[string]any{"roles": roles[:1]})
	var ps PrincipalState
	if e := decode(principal, &ps); e != nil {
		t.Fatal(e)
	}
	if ps.Subject != b || !reflect.DeepEqual(ps.Roles, roles[1:]) {
		t.Fatalf("wrong principal %s", principal)
	}
	role := reduceTest(t, r["core/role"], nil, "create", RoleState{ID: "role", Network: "test", Principal: a, Subject: b, Permissions: []string{"read", "write", "read"}})
	role = reduceTest(t, r["core/role"], role, "remove-permissions", map[string]any{"permissions": []string{"read"}})
	var rs RoleState
	if e := decode(role, &rs); e != nil {
		t.Fatal(e)
	}
	if rs.ID != "role" || rs.Principal != a || rs.Subject != b || !reflect.DeepEqual(rs.Permissions, []string{"write"}) {
		t.Fatalf("wrong role %s", role)
	}
}

func TestReducerLifecycleAndSparseUpgrade(t *testing.T) {
	r := Reducers()
	ref := reducerRef("principal")
	start := SessionState{Device: Device{Type: "mobile", Platform: "ios"}, Started: 10, Expires: 20}
	session := reduceTest(t, r["core/session"], nil, "start", start)
	session = reduceTest(t, r["core/session"], session, "upgrade", map[string]Ref{"principal": ref})
	session = reduceTest(t, r["core/session"], session, "upgrade", map[string]Ref{"scene": ref})
	var ss SessionState
	if e := decode(session, &ss); e != nil {
		t.Fatal(e)
	}
	if ss.Principal != ref || ss.Scene != ref || ss.Device != start.Device || ss.Started != 10 || ss.Expires != 20 {
		t.Fatalf("upgrade erased state: %s", session)
	}
	event := func(action string, p any) eventstore.Event {
		return eventstore.Event{Input: eventstore.Input{Action: action, Payload: reducerJSON(t, p)}}
	}
	if _, e := r["core/session"].Apply(session, event("upgrade", map[string]Ref{"principal": reducerRef("other")})); e != nil {
		t.Fatalf("provisional principal replacement: %v", e)
	}
	session = reduceTest(t, r["core/session"], session, "upgrade", map[string]Ref{"account": ref})
	if _, e := r["core/session"].Apply(session, event("upgrade", map[string]Ref{"principal": reducerRef("other")})); e == nil {
		t.Fatal("switched principal")
	}
	session = reduceTest(t, r["core/session"], session, "terminate", map[string]string{"terminated": "now"})
	for _, ev := range []eventstore.Event{event("start", start), event("upgrade", map[string]Ref{"principal": ref}), event("change-scene", map[string]Ref{"scene": ref})} {
		if _, e := r["core/session"].Apply(session, ev); e == nil {
			t.Fatalf("terminated session accepted %s", ev.Input.Action)
		}
	}
	challenge := ChallengeState{Code: "hash", Expires: 1, Session: ref, Delivery: "ciphertext", Upgrade: SessionContext{Principal: ref}, Events: []eventstore.Input{{Root: ref, Action: "set-subject", Payload: reducerJSON(t, map[string]Ref{"subject": ref})}}}
	state := reduceTest(t, r["core/challenge"], nil, "issue", challenge)
	state = reduceTest(t, r["core/challenge"], state, "answer", map[string]string{"answered": "now"})
	var cs ChallengeState
	if e := decode(state, &cs); e != nil {
		t.Fatal(e)
	}
	if cs.Delivery != "ciphertext" || cs.Code != "hash" || cs.Expires != 1 || len(cs.Events) != 1 || cs.Upgrade.Principal != ref {
		t.Fatalf("answer erased challenge %s", state)
	}
	for _, ev := range []eventstore.Event{event("answer", map[string]string{"answered": "now"}), event("issue", challenge)} {
		if _, e := r["core/challenge"].Apply(state, ev); e == nil {
			t.Fatal("reused answered challenge")
		}
	}
	key := KeyState{Name: "name", Network: "test", Secret: "hash", Scene: ref, Subject: ref, Principal: ref}
	state = reduceTest(t, r["system/key"], nil, "create", key)
	state = reduceTest(t, r["system/key"], state, "revoke", map[string]string{"revoked": "now"})
	var ks KeyState
	if e := decode(state, &ks); e != nil {
		t.Fatal(e)
	}
	if ks.Secret != "hash" || ks.Revoked != "now" {
		t.Fatal("revocation erased key")
	}
	if _, e := r["system/key"].Apply(state, event("create", key)); e == nil {
		t.Fatal("revived key")
	}
	scene := SceneState{Subject: ref, Group: ref}
	state = reduceTest(t, r["core/scene"], nil, "register", scene)
	state = reduceTest(t, r["core/scene"], state, "delete", map[string]string{"deleted": "now"})
	if _, e := r["core/scene"].Apply(state, event("register", scene)); e == nil {
		t.Fatal("revived scene")
	}
}

func TestReducerJSONModels(t *testing.T) {
	raw := reducerJSON(t, AccountState{EmailHMAC: "hash", Principal: reducerRef("id")})
	if !strings.Contains(string(raw), `"emailHmac"`) || !strings.Contains(string(raw), `"id":"id"`) || strings.Contains(string(raw), `"root"`) {
		t.Fatalf("wrong JSON contract %s", raw)
	}
	var cs ChallengeState
	if e := decode([]byte(`{"delivery":{"body":"plaintext"}}`), &cs); e == nil {
		t.Fatal("accepted unencrypted delivery object")
	}
	if e := decode([]byte(`{"session":{"unexpected":1}}`), &cs); e == nil {
		t.Fatal("accepted nested unknown field")
	}
	if e := decode([]byte(`{} trailing`), &cs); e == nil {
		t.Fatal("accepted trailing bytes")
	}
	if _, e := Reducers()["core/account"].Apply([]byte(`{"unknown":1}`), eventstore.Event{Input: eventstore.Input{Action: "set-password", Payload: []byte(`{"password":"hash"}`)}}); e == nil {
		t.Fatal("accepted unknown state field")
	}
}

func TestReducerEncryptedComms(t *testing.T) {
	for _, domain := range []string{"comms/email", "comms/sms"} {
		t.Run(domain, func(t *testing.T) {
			r := Reducers()[domain]
			p := CommsState{Delivery: "encrypted-provider-input", Subject: reducerRef("sender"), RequestHash: "semantic-request-digest"}
			raw := reducerJSON(t, p)
			normalized, e := r.Normalize("prepare", raw)
			if e != nil {
				t.Fatal(e)
			}
			event := eventstore.Event{Input: eventstore.Input{Action: "prepare", Payload: normalized}, Number: 0}
			state, e := r.Apply(nil, event)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(raw, state) {
				t.Fatalf("changed prepared payload: %s", state)
			}
			if _, e := r.Apply(state, event); e == nil {
				t.Fatal("repeated preparation accepted")
			}
			changed := p
			changed.Delivery = "other-ciphertext"
			event.Input.Payload = reducerJSON(t, changed)
			if _, e := r.Apply(state, event); e == nil {
				t.Fatal("overwrote prepared delivery")
			}
			event.Number = 1
			if _, e := r.Apply(nil, event); e == nil {
				t.Fatal("accepted preparation after first event")
			}
			for _, bad := range []string{`{}`, `null`, string(raw) + ` {}`, `{"delivery":"cipher","requestHash":"digest","subject":{}}`, string(raw[:len(raw)-1]) + `,"body":"plaintext"}`} {
				if _, e := r.Normalize("prepare", []byte(bad)); e == nil {
					t.Fatalf("accepted %s", bad)
				}
			}
			for _, missing := range []CommsState{{Subject: p.Subject, RequestHash: p.RequestHash}, {Delivery: p.Delivery, Subject: p.Subject}, {Delivery: p.Delivery, RequestHash: p.RequestHash}} {
				if _, e := r.Normalize("prepare", reducerJSON(t, missing)); e == nil {
					t.Fatal("accepted missing required field")
				}
			}
			legacy := eventstore.Event{Input: eventstore.Input{Action: "send", Payload: reducerJSON(t, smsPayload{To: "recipient", Message: "body"})}}
			if domain == "comms/email" {
				legacy.Input.Action = "send-auth-code"
				legacy.Input.Payload = reducerJSON(t, Mail{From: "sender", To: "recipient", Subject: "subject", Body: "body"})
			}
			if _, e := r.Apply(state, legacy); e == nil {
				t.Fatal("legacy action overwrote prepared delivery")
			}
			legacyState, e := r.Apply(nil, legacy)
			if e != nil {
				t.Fatal(e)
			}
			event.Number = 0
			if _, e := r.Apply(legacyState, event); e == nil {
				t.Fatal("prepare overwrote legacy state")
			}
		})
	}
}
