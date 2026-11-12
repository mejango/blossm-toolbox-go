package dia

import "context"

// AuthenticateConnection verifies signature, claims, and current key revocation.
func (s *Service) AuthenticateConnection(ctx context.Context, token string) (Claims, error) {
	return s.authenticate(ctx, token, "connection")
}

// RequirePermission checks current grants for claims obtained from this service's
// AuthenticateAccess or AuthenticateConnection. It is not token authentication.
func (s *Service) RequirePermission(ctx context.Context, claims Claims, subject Ref, permission string) error {
	return s.require(ctx, claims, subject, permission)
}

// AuthorizeDelegatedContext validates live end-user state after the caller has
// authenticated an access token, or verified a service JWT and bound its pinned
// peer to the incoming request. It does not verify signatures itself.
func (s *Service) AuthorizeDelegatedContext(ctx context.Context, verifiedClaims Claims) error {
	user := verifiedClaims
	switch user.Kind {
	case "access": // Preserve the authenticated end-user subject on the first hop.
	case "service":
		user.Subject = user.Context.Principal.ID
		if user.Subject == "" {
			user.Subject = user.Context.Session.ID
		}
	default:
		return ErrUnauthorized
	}
	return s.authorizeSessionContext(ctx, user, false)
}
