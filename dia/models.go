package dia

import "blossm.local/backend/eventstore"

// Ref uses the same complete identity as persisted events.
type Ref = eventstore.Root

type Device struct {
	Type     string `json:"type"`
	Platform string `json:"platform"`
	Agent    string `json:"agent"`
	IP       string `json:"ip"`
}

type SessionContext struct {
	Network   string `json:"network"`
	Session   Ref    `json:"session"`
	Principal Ref    `json:"principal"`
	Account   Ref    `json:"account"`
	Scene     Ref    `json:"scene"`
	Subject   Ref    `json:"subject"`
	Device    Device `json:"device"`
}

type SessionState struct {
	Started    int64  `json:"started"`
	Expires    int64  `json:"expires"`
	Device     Device `json:"device"`
	Principal  Ref    `json:"principal"`
	Account    Ref    `json:"account"`
	Scene      Ref    `json:"scene"`
	Terminated string `json:"terminated"`
}

type AccountState struct {
	EmailHMAC      string `json:"emailHmac"`
	EmailEncrypted string `json:"emailEncrypted"`
	Password       string `json:"password"`
	Principal      Ref    `json:"principal"`
}

type ChallengeState struct {
	RecoveryAttempts int                `json:"recoveryAttempts"`
	AnswerKey        string             `json:"answerKey"`
	ProofHash        string             `json:"proofHash"`
	RequestHash      string             `json:"requestHash"`
	Attempts         int                `json:"attempts"`
	Code             string             `json:"code"`
	Expires          int64              `json:"expires"`
	Answered         string             `json:"answered"`
	Session          Ref                `json:"session"`
	Upgrade          SessionContext     `json:"upgrade"`
	Events           []eventstore.Input `json:"events"`
	Delivery         string             `json:"delivery"`
}

type PrincipalState struct {
	Subject Ref         `json:"subject"`
	Roles   []RoleGrant `json:"roles"`
}

type RoleGrant struct {
	ID      string `json:"id"`
	Subject Ref    `json:"subject"`
}

type GroupState struct {
	Principals []Ref    `json:"principals"`
	Networks   []string `json:"networks"`
}

type SceneState struct {
	Subject Ref    `json:"subject"`
	Group   Ref    `json:"group"`
	Deleted string `json:"deleted"`
}

type RoleState struct {
	Principal   Ref      `json:"principal"`
	Subject     Ref      `json:"subject"`
	ID          string   `json:"id"`
	Network     string   `json:"network"`
	Permissions []string `json:"permissions"`
}

type NodeState struct {
	Network string `json:"network"`
	Scene   Ref    `json:"scene"`
}

type KeyState struct {
	Revoked   string `json:"revoked"`
	Name      string `json:"name"`
	Network   string `json:"network"`
	Secret    string `json:"secret"`
	Scene     Ref    `json:"scene"`
	Principal Ref    `json:"principal"`
	Subject   Ref    `json:"subject"`
}

// CommsState retains encrypted provider input for explicit delivery retries.
type CommsState struct {
	Delivery    string `json:"delivery"`
	Subject     Ref    `json:"subject"`
	RequestHash string `json:"requestHash"`
}
