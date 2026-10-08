package domain

import (
	"crypto/rand"
	"strings"
)

// Device authorization (RFC 8628) for `keel login`, exactly as better-auth 1.6.17's
// deviceAuthorization plugin behaves with Keel's options (docs/go/spec/cli-install.md §A8,
// auth-orgs.md §5.3).
const (
	DeviceClientID      = "keel-cli"
	DeviceGrantType     = "urn:ietf:params:oauth:grant-type:device_code"
	DeviceCodeTTL       = int64(30 * 60 * 1000) // 30 minutes
	DeviceIntervalS     = 5                     // seconds between polls
	DeviceCodeLength    = 40
	DeviceUserCodeChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	DeviceUserCodeLen   = 8

	// DeviceCodeKeep is how long an expired code is kept before a new login sweeps it. Better
	// Auth deleted expired codes only when they were polled, so a CLI still polling (keel login
	// --wait polls every few seconds) or a /device tab left open gets "Device code has expired" /
	// "User code has expired", not "Invalid device code" / "Invalid user code". Short, because
	// creating codes needs no session: the sweep is what bounds the table.
	DeviceCodeKeep = int64(60 * 60 * 1000)
)

type DeviceStatus string

const (
	DevicePending  DeviceStatus = "pending"
	DeviceApproved DeviceStatus = "approved"
	DeviceDenied   DeviceStatus = "denied"
)

// DeviceCode is one `keel login` link. The device code itself (the CLI's secret) is stored only
// as its hash, so it is not here.
type DeviceCode struct {
	ID           string
	UserCode     string
	ClientID     string
	Status       DeviceStatus
	UserID       string // "" until a signed-in user looks at the code (binding)
	IntervalS    int
	LastPolledAt *int64
	ExpiresAt    int64
	CreatedAt    int64
}

// DeviceExpired: better-auth's `expiresAt < now`.
func (d DeviceCode) Expired(now int64) bool { return d.ExpiresAt < now }

const deviceAlnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// NewDeviceCode is 40 random characters from [a-zA-Z0-9] (better-auth deviceCodeLength 40).
func NewDeviceCode() string {
	out := make([]byte, 0, DeviceCodeLength)
	buf := make([]byte, 64)
	for len(out) < DeviceCodeLength {
		if _, err := rand.Read(buf); err != nil {
			panic(err)
		}
		for _, b := range buf {
			// 62 symbols: reject the top of the byte range so every symbol is equally likely.
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

// NewUserCode is 8 characters, each DeviceUserCodeChars[randomByte % 32], as better-auth makes it.
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

// CleanUserCode is how the server looks a user code up: dashes removed, nothing else (case is
// not normalised; the dashboard upper-cases before calling).
func CleanUserCode(code string) string { return strings.ReplaceAll(code, "-", "") }

// DeviceRefusal is an RFC 8628 error: HTTP status, `error` and `error_description`.
type DeviceRefusal struct {
	Status      int
	Code        string
	Description string
}

func (r *DeviceRefusal) Error() string { return r.Description }

func deviceRefuse(status int, code, description string) *DeviceRefusal {
	return &DeviceRefusal{Status: status, Code: code, Description: description}
}

// Device error descriptions (better-auth DEVICE_AUTHORIZATION_ERROR_CODES).
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

// CheckDeviceClient: POST /device/code accepts only the keel CLI.
func CheckDeviceClient(clientID string) *DeviceRefusal {
	if clientID != DeviceClientID {
		return deviceRefuse(400, "invalid_client", MsgDeviceInvalidClient)
	}
	return nil
}

// PollAction is what one POST /device/token poll does to the stored code.
type PollAction int

const (
	PollRefuse      PollAction = iota // answer Refusal; the row is untouched
	PollTouch                         // set last_polled_at = now, answer Refusal
	PollTouchDelete                   // delete the row, answer Refusal
	PollIssue                         // consume the row (single use) and issue a session to UserID
)

// PollDecision is the outcome of a poll.
type PollDecision struct {
	Action  PollAction
	Refusal *DeviceRefusal // nil for PollIssue
}

// DecidePoll evaluates POST /device/token in better-auth's order (cli-install.md §A8). dc is nil
// when no row has the device code. Note: a slow_down does not move last_polled_at.
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

// CheckUserCode is GET /device: the code must exist and not be expired. Binding happens when it
// returns nil, the caller is signed in, the code is pending and nobody claimed it yet.
func CheckUserCode(dc *DeviceCode, now int64) *DeviceRefusal {
	if dc == nil {
		return deviceRefuse(400, "invalid_request", MsgDeviceInvalidUserCode)
	}
	if dc.Expired(now) {
		return deviceRefuse(400, "expired_token", MsgDeviceExpiredUserCode)
	}
	return nil
}

// ShouldBind: GET /device by a signed-in user claims an unclaimed pending code for them.
func (d DeviceCode) ShouldBind(userID string) bool {
	return userID != "" && d.UserID == "" && d.Status == DevicePending
}

// DecideDevice is POST /device/approve and /device/deny: only the user the code is bound to may
// decide, once.
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
