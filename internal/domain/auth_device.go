package domain

import (
	"crypto/rand"
	"strings"
)

const (
	DeviceClientID      = "keel-cli"
	DeviceGrantType     = "urn:ietf:params:oauth:grant-type:device_code"
	DeviceCodeTTL       = int64(30 * 60 * 1000)
	DeviceIntervalS     = 5
	DeviceCodeLength    = 40
	DeviceUserCodeChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	DeviceUserCodeLen   = 8

	DeviceCodeKeep = int64(60 * 60 * 1000)
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

const deviceAlnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func NewDeviceCode() string {
	out := make([]byte, 0, DeviceCodeLength)
	buf := make([]byte, 64)
	for len(out) < DeviceCodeLength {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		for _, b := range buf {
			if b >= 248 {
				continue
			}
			out = append(out, deviceAlnum[int(b)%len(deviceAlnum)])
			if len(out) == DeviceCodeLength {
				break
			}
		}
	}
	return string(out)
}

func NewUserCode() string {
	b := make([]byte, DeviceUserCodeLen)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = DeviceUserCodeChars[int(b[i])%len(DeviceUserCodeChars)]
	}
	return string(b)
}

func CleanUserCode(code string) string { return strings.ReplaceAll(code, "-", "") }

type DeviceRefusal struct {
	Status      int
	Code        string
	Description string
}

func (r *DeviceRefusal) Error() string { return r.Description }

func deviceRefuse(status int, code, description string) *DeviceRefusal {
	return &DeviceRefusal{Status: status, Code: code, Description: description}
}

const (
	MsgDeviceInvalidClient   = "Invalid client ID"
	MsgDeviceInvalidCode     = "Invalid device code"
	MsgDeviceClientMismatch  = "Client ID mismatch"
	MsgDeviceSlowDown        = "Polling too frequently"
	MsgDeviceExpiredCode     = "Device code has expired"
	MsgDevicePending         = "Authorization pending"
	MsgDeviceAccessDenied    = "Access denied"
	MsgDeviceUserNotFound    = "User not found"
	MsgDeviceBadStatus       = "Invalid device code status"
	MsgDeviceInvalidUserCode = "Invalid user code"
	MsgDeviceExpiredUserCode = "User code has expired"
	MsgDeviceProcessed       = "Device code already processed"
	MsgDeviceNotClaimed      = "Device code has not been claimed by a verifying session; call `GET /device` with the `user_code` while signed in before approving or denying"
	MsgDeviceAuthRequired    = "Authentication required"
	MsgDeviceNotYoursApprove = "You are not authorized to approve this device authorization"
	MsgDeviceNotYoursDeny    = "You are not authorized to deny this device authorization"
	MsgDeviceGrantType       = "Unsupported grant type"
)

func CheckDeviceClient(clientID string) *DeviceRefusal {
	if clientID != DeviceClientID {
		return deviceRefuse(400, "invalid_client", MsgDeviceInvalidClient)
	}
	return nil
}

type PollAction int

const (
	PollRefuse PollAction = iota
	PollTouch
	PollTouchDelete
	PollIssue
)

type PollDecision struct {
	Action  PollAction
	Refusal *DeviceRefusal
}

func DecidePoll(dc *DeviceCode, clientID string, now int64) PollDecision {
	reject := func(a PollAction, status int, code, desc string) PollDecision {
		return PollDecision{Action: a, Refusal: deviceRefuse(status, code, desc)}
	}
	if clientID != DeviceClientID {
		return reject(PollRefuse, 400, "invalid_grant", MsgDeviceInvalidClient)
	}
	if dc == nil {
		return reject(PollRefuse, 400, "invalid_grant", MsgDeviceInvalidCode)
	}
	if dc.ClientID != "" && dc.ClientID != clientID {
		return reject(PollRefuse, 400, "invalid_grant", MsgDeviceClientMismatch)
	}
	if dc.LastPolledAt != nil && dc.IntervalS > 0 && now-*dc.LastPolledAt < int64(dc.IntervalS)*1000 {
		return reject(PollRefuse, 400, "slow_down", MsgDeviceSlowDown)
	}
	if dc.Expired(now) {
		return reject(PollTouchDelete, 400, "expired_token", MsgDeviceExpiredCode)
	}
	switch {
	case dc.Status == DevicePending:
		return reject(PollTouch, 400, "authorization_pending", MsgDevicePending)
	case dc.Status == DeviceDenied:
		return reject(PollTouchDelete, 400, "access_denied", MsgDeviceAccessDenied)
	case dc.Status == DeviceApproved && dc.UserID != "":
		return PollDecision{Action: PollIssue}
	}
	return reject(PollTouch, 500, "server_error", MsgDeviceBadStatus)
}

func CheckUserCode(dc *DeviceCode, now int64) *DeviceRefusal {
	if dc == nil {
		return deviceRefuse(400, "invalid_request", MsgDeviceInvalidUserCode)
	}
	if dc.Expired(now) {
		return deviceRefuse(400, "expired_token", MsgDeviceExpiredUserCode)
	}
	return nil
}

func (d DeviceCode) ShouldBind(userID string) bool {
	return userID != "" && d.UserID == "" && d.Status == DevicePending
}

func DecideDevice(dc *DeviceCode, userID string, approve bool, now int64) *DeviceRefusal {
	if userID == "" {
		return deviceRefuse(401, "unauthorized", MsgDeviceAuthRequired)
	}
	if r := CheckUserCode(dc, now); r != nil {
		return r
	}
	if dc.Status != DevicePending {
		return deviceRefuse(400, "invalid_request", MsgDeviceProcessed)
	}
	if dc.UserID == "" {
		return deviceRefuse(400, "invalid_request", MsgDeviceNotClaimed)
	}
	if dc.UserID != userID {
		if approve {
			return deviceRefuse(403, "access_denied", MsgDeviceNotYoursApprove)
		}
		return deviceRefuse(403, "access_denied", MsgDeviceNotYoursDeny)
	}
	return nil
}
