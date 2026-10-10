package domain

import "crypto/rand"

const (
	DeviceClientID  = "keel-cli"
	DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"
	DeviceCodeTTL   = int64(30 * 60 * 1000)
	DeviceIntervalS = 5
	DeviceCodeKeep  = int64(60 * 60 * 1000)
)

type DeviceStatus string

const (
	DevicePending  DeviceStatus = "pending"
	DeviceApproved DeviceStatus = "approved"
	DeviceDenied   DeviceStatus = "denied"
)

type DeviceCode struct {
	ID           string
	UserCode     string
	ClientID     string
	Status       DeviceStatus
	UserID       string
	IntervalS    int
	LastPolledAt *int64
	ExpiresAt    int64
	CreatedAt    int64
}

func (d DeviceCode) Expired(now int64) bool { return d.ExpiresAt < now }

func NewUserCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

type DeviceRefusal struct {
	Status      int
	Code        string
	Description string
}

func (r *DeviceRefusal) Error() string { return r.Description }

const (
	MsgDeviceInvalidClient = "Invalid client ID"
	MsgDeviceProcessed     = "Device code already processed"
	MsgDeviceNotClaimed    = "Device code has not been claimed by a verifying session; call `GET /device` with the `user_code` while signed in before approving or denying"
)

type PollAction int

const (
	PollRefuse PollAction = iota
	PollTouch
	PollTouchDelete
	PollIssue
)

func DecidePoll(dc DeviceCode, now int64) (PollAction, *DeviceRefusal) {
	if dc.LastPolledAt != nil && now-*dc.LastPolledAt < int64(dc.IntervalS)*1000 {
		return PollRefuse, &DeviceRefusal{400, "slow_down", "Polling too frequently"}
	}
	if dc.Expired(now) {
		return PollTouchDelete, &DeviceRefusal{400, "expired_token", "Device code has expired"}
	}
	switch dc.Status {
	case DevicePending:
		return PollTouch, &DeviceRefusal{400, "authorization_pending", "Authorization pending"}
	case DeviceDenied:
		return PollTouchDelete, &DeviceRefusal{400, "access_denied", "Access denied"}
	default:
		return PollIssue, nil
	}
}

func DecideDevice(dc DeviceCode, userID string, approve bool) *DeviceRefusal {
	if dc.Status != DevicePending {
		return &DeviceRefusal{400, "invalid_request", MsgDeviceProcessed}
	}
	if dc.UserID == "" {
		return &DeviceRefusal{400, "invalid_request", MsgDeviceNotClaimed}
	}
	if dc.UserID == userID {
		return nil
	}
	if approve {
		return &DeviceRefusal{403, "access_denied", "You are not authorized to approve this device authorization"}
	}
	return &DeviceRefusal{403, "access_denied", "You are not authorized to deny this device authorization"}
}
