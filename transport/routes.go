package transport

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/mejango/blossm-toolbox-go/dia"
)

// partialResultError carries recovery data, not a commitment or delivery claim.
// Only explicitly supported public result types may cross this boundary.
type partialResultError struct {
	err    error
	result any
}

func (e partialResultError) Error() string      { return e.err.Error() }
func (e partialResultError) Unwrap() error      { return e.err }
func (e partialResultError) PartialResult() any { return e.result }

func post[T any](mux *http.ServeMux, path string, fn func(context.Context, string, string, T) (any, error)) {
	mux.HandleFunc("POST "+path, wrap(func(w http.ResponseWriter, r *http.Request) error {
		var in T
		if e := decode(w, r, &in); e != nil {
			return e
		}
		out, e := fn(r.Context(), token(r), r.Header.Get("Idempotency-Key"), in)
		if e != nil {
			switch result := out.(type) {
			case dia.ChallengeResult:
				if result.Challenge.ID != "" && result.Token != "" {
					return partialResultError{e, result}
				}
			case dia.Ref:
				if result.ID != "" {
					return partialResultError{e, result}
				}
			}
			return e
		}
		return write(w, 200, out)
	}))
}
func (a API) commands(mux *http.ServeMux) {
	type credentials struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	post(mux, "/commands/session/save", func(c context.Context, t, k string, in credentials) (any, error) {
		return a.Dia.SaveSession(c, t, k, in.Email, in.Password)
	})
	post(mux, "/commands/challenge/issue", func(c context.Context, t, k string, in credentials) (any, error) {
		return a.Dia.IssueChallenge(c, t, k, in.Email, in.Password)
	})
	post(mux, "/commands/challenge/answer", func(c context.Context, t, k string, in struct {
		Code string `json:"code"`
	}) (any, error) {
		return a.Dia.AnswerChallenge(c, t, k, in.Code)
	})
	post(mux, "/commands/challenge/retry-delivery", func(c context.Context, t, k string, in struct{}) (any, error) {
		return nil, a.Dia.RetryChallengeDelivery(c, t)
	})
	post(mux, "/commands/account/set-password", func(c context.Context, t, k string, in struct {
		Password string `json:"password"`
	}) (any, error) {
		return nil, a.Dia.SetPassword(c, t, k, in.Password)
	})
	post(mux, "/commands/scene/register", func(c context.Context, t, k string, in struct {
		Subject dia.Ref `json:"subject"`
	}) (any, error) {
		return a.Dia.RegisterScene(c, t, k, in.Subject)
	})
	type scene struct {
		Scene dia.Ref `json:"scene"`
	}
	post(mux, "/commands/scene/delete", func(c context.Context, t, k string, in scene) (any, error) {
		return nil, a.Dia.DeleteScene(c, t, k, in.Scene)
	})
	post(mux, "/commands/session/change-scene", func(c context.Context, t, k string, in scene) (any, error) {
		return a.Dia.ChangeScene(c, t, k, in.Scene)
	})
	type membership struct {
		Group      dia.Ref   `json:"group"`
		Principals []dia.Ref `json:"principals"`
	}
	post(mux, "/commands/group/add-principals", func(c context.Context, t, k string, in membership) (any, error) {
		return a.Dia.AddPrincipals(c, t, k, in.Group, in.Principals)
	})
	post(mux, "/commands/group/remove-principals", func(c context.Context, t, k string, in membership) (any, error) {
		return nil, a.Dia.RemovePrincipals(c, t, k, in.Group, in.Principals)
	})
	type grants struct {
		Principal dia.Ref         `json:"principal"`
		Roles     []dia.RoleGrant `json:"roles"`
	}
	post(mux, "/commands/principal/add-roles", func(c context.Context, t, k string, in grants) (any, error) {
		return nil, a.Dia.ChangeRoles(c, t, k, in.Principal, in.Roles, false)
	})
	post(mux, "/commands/principal/remove-roles", func(c context.Context, t, k string, in struct {
		Principal dia.Ref         `json:"principal"`
		Roles     []dia.RoleGrant `json:"roles"`
		Subjects  []dia.Ref       `json:"subjects"`
	}) (any, error) {
		return nil, a.Dia.RemoveRoles(c, t, k, in.Principal, in.Roles, in.Subjects)
	})
	type role struct {
		ID          string   `json:"id"`
		Permissions []string `json:"permissions"`
	}
	post(mux, "/commands/role/create", func(c context.Context, t, k string, in role) (any, error) {
		return nil, a.Dia.CreateRole(c, t, k, in.ID, in.Permissions)
	})
	post(mux, "/commands/role/add-permissions", func(c context.Context, t, k string, in role) (any, error) {
		return nil, a.Dia.ChangeRolePermissions(c, t, k, in.ID, in.Permissions, false)
	})
	post(mux, "/commands/role/remove-permissions", func(c context.Context, t, k string, in role) (any, error) {
		return nil, a.Dia.ChangeRolePermissions(c, t, k, in.ID, in.Permissions, true)
	})
	post(mux, "/commands/node/register", func(c context.Context, t, k string, in dia.NodeInput) (any, error) {
		return a.Dia.RegisterNode(c, t, k, in)
	})
	post(mux, "/commands/key/create", func(c context.Context, t, k string, in dia.KeyInput) (any, error) {
		return a.Dia.RegisterKey(c, t, "access", k, in)
	})
	post(mux, "/commands/key/revoke", func(c context.Context, t, k string, in struct {
		Key dia.Ref `json:"key"`
	}) (any, error) {
		return nil, a.Dia.RevokeKey(c, t, "access", k, in.Key)
	})
	post(mux, "/commands/group/add-members", func(c context.Context, t, k string, in struct {
		Group   dia.Ref              `json:"group"`
		Members []dia.GroupPrincipal `json:"members"`
	}) (any, error) {
		return a.Dia.AddGroupPrincipals(c, t, k, in.Group, in.Members)
	})
	post(mux, "/commands/connection/open", func(c context.Context, t, k string, in dia.ConnectionInput) (any, error) {
		return a.Dia.OpenConnection(c, in)
	})
	post(mux, "/commands/updates/create-token", func(c context.Context, t, k string, in struct {
		Kind string `json:"kind"`
	}) (any, error) {
		return a.Dia.IssueUpdatesToken(c, t, in.Kind)
	})
	post(mux, "/commands/updates/push", func(c context.Context, t, k string, in struct {
		Kind    string           `json:"kind"`
		Channel dia.ChannelInput `json:"channel"`
		Update  dia.Update       `json:"update"`
	}) (any, error) {
		return nil, a.Dia.PublishUpdate(c, t, in.Kind, in.Channel, in.Update)
	})
	post(mux, "/commands/email/send", func(c context.Context, t, k string, in struct {
		Subject dia.Ref  `json:"subject"`
		Message dia.Mail `json:"message"`
	}) (any, error) {
		return a.Dia.SendMailCommand(c, t, "access", k, in.Subject, in.Message)
	})
	post(mux, "/commands/sms/send", func(c context.Context, t, k string, in struct {
		Subject dia.Ref `json:"subject"`
		To      string  `json:"to"`
		Message string  `json:"message"`
	}) (any, error) {
		return a.Dia.SendSMSCommand(c, t, "access", k, in.Subject, in.To, in.Message)
	})
	post(mux, "/facts/group/principals", func(c context.Context, t, k string, in struct {
		Group dia.Ref `json:"group"`
	}) (any, error) {
		return a.Dia.GroupPrincipals(c, t, in.Group)
	})
	mux.HandleFunc("POST /facts/group/principals/stream", wrap(func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			Group dia.Ref `json:"group"`
		}
		if err := decode(w, r, &in); err != nil {
			return err
		}
		return ndjson(w, func(yield func(any) error) error {
			return a.Dia.StreamGroupPrincipals(r.Context(), token(r), in.Group, func(principal dia.Ref) error { return yield(principal) })
		})
	}))
	post(mux, "/facts/principal/groups", func(c context.Context, t, k string, in struct{}) (any, error) { return a.Dia.PrincipalGroups(c, t) })
	post(mux, "/facts/principal/roles", func(c context.Context, t, k string, in struct {
		Subjects []dia.Ref `json:"subjects"`
	}) (any, error) {
		return a.Dia.PrincipalRoles(c, t, in.Subjects)
	})
	post(mux, "/facts/role/permissions", func(c context.Context, t, k string, in struct {
		ID string `json:"id"`
	}) (any, error) {
		return a.Dia.RolePermissions(c, t, in.ID)
	})
	post(mux, "/facts/session/terminated", func(c context.Context, t, k string, in struct{}) (any, error) { return a.Dia.SessionTerminated(c, t) })
	post(mux, "/facts/scene/deleted", func(c context.Context, t, k string, in scene) (any, error) { return a.Dia.DeletedScene(c, t, in.Scene) })
	post(mux, "/facts/key/state", func(c context.Context, t, k string, in struct {
		Key dia.Ref `json:"key"`
	}) (any, error) {
		return a.Dia.ReadKey(c, t, "access", in.Key)
	})
	type channelRequest struct {
		Kind    string           `json:"kind"`
		Channel dia.ChannelInput `json:"channel"`
	}
	post(mux, "/facts/updates/channel", func(c context.Context, t, k string, in channelRequest) (any, error) {
		return a.Dia.UpdatesChannel(c, t, in.Kind, in.Channel)
	})
	// This is a GRIP proxy handshake, not an in-process subscription loop.
	// Only the channel derived from the authenticated subject reaches GRIP.
	mux.HandleFunc("POST /facts/updates/channel/stream", wrap(func(w http.ResponseWriter, r *http.Request) error {
		var in channelRequest
		if err := decode(w, r, &in); err != nil {
			return err
		}
		channel, err := a.Dia.UpdatesChannel(r.Context(), token(r), in.Kind, in.Channel)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Grip-Hold", "stream")
		w.Header().Set("Grip-Channel", channel)
		w.Header().Set("Grip-Keep-Alive", ":\\n\\n; format=cstring; timeout=20")
		_, err = io.WriteString(w, ":"+strings.Repeat(" ", 2047)+"\n\n")
		return err
	}))
}
