package domain

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestValidUserEmail(t *testing.T) {
	for email, want := range map[string]bool{
		"ci@example.com":                true,
		"First.Last+tag@Sub.Example.io": true,
		"o'brien@example.com":           true,
		"a@b.co":                        true,
		"":                              false,
		"plain":                         false,
		"a@b":                           false,
		"a@b.c":                         false,
		".a@example.com":                false,
		"a..b@example.com":              false,
		"a.@example.com":                false,
		"a@-example.com":                false,
		" a@example.com":                false,
		"a@example.com ":                false,
		"a b@example.com":               false,
		"a@exa_mple.com":                false,
	} {
		if got := ValidUserEmail(email); got != want {
			t.Errorf("ValidUserEmail(%q) = %v, want %v", email, got, want)
		}
	}
}

func TestSameUserEmail(t *testing.T) {
	if !SameUserEmail("  Ci@Example.COM ", "ci@example.com") {
		t.Error("case and spaces should not matter")
	}
	if SameUserEmail("ci@example.com", "cj@example.com") {
		t.Error("different emails matched")
	}
}

func TestValidPassword(t *testing.T) {
	cases := []struct {
		pw   string
		want string
	}{
		{"1234567", MsgPasswordTooShort},
		{"12345678", ""},
		{strings.Repeat("a", 128), ""},
		{strings.Repeat("a", 129), MsgPasswordTooLong},
		{"😀😀😀😀", ""},
		{"😀😀😀", MsgPasswordTooShort},
		{strings.Repeat("😀", 64), ""},
		{strings.Repeat("😀", 65), MsgPasswordTooLong},
		{"éééééééé", ""},
	}
	for _, c := range cases {
		err := ValidPassword(c.pw)
		got := ""
		if err != nil {
			got = err.Error()
			if CodeOf(err) != CodeInvalidInput {
				t.Errorf("%q: code %s", c.pw, CodeOf(err))
			}
		}
		if got != c.want {
			t.Errorf("ValidPassword(%q) = %q, want %q", c.pw, got, c.want)
		}
	}
}

func TestHashSecret(t *testing.T) {
	if a := HashSecret("abc"); a != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("sha256(abc) = %s", a)
	}
}

func TestSessionRenewal(t *testing.T) {
	now := int64(100 * SessionTTL)
	cases := []struct {
		name      string
		expiresAt int64
		renew     bool
		expired   bool
	}{
		{"fresh", now + SessionTTL, false, false},
		{"used 23h after renewal", now + SessionTTL - SessionUpdateAge + 1, false, false},
		{"used exactly a day after renewal", now + SessionTTL - SessionUpdateAge, true, false},
		{"used 3 days after renewal", now + SessionTTL - 3*SessionUpdateAge, true, false},
		{"last millisecond", now + 1, true, false},
		{"expires now", now, true, true},
		{"expired", now - 1, true, true},
	}
	for _, c := range cases {
		if got := SessionNeedsRenewal(c.expiresAt, now); got != c.renew {
			t.Errorf("%s: renew = %v, want %v", c.name, got, c.renew)
		}
		if got := SessionExpired(c.expiresAt, now); got != c.expired {
			t.Errorf("%s: expired = %v, want %v", c.name, got, c.expired)
		}
	}
}

func TestInvitationStanding(t *testing.T) {
	cases := []struct {
		status  InvitationStatus
		expires int64
		want    bool
	}{
		{InvitationPending, 10, true},
		{InvitationPending, 9, true},
		{InvitationPending, 8, false},
		{InvitationAccepted, 100, false},
		{InvitationCanceled, 100, false},
	}
	for _, c := range cases {
		if got := (Invitation{Status: c.status, ExpiresAt: c.expires}).Standing(9); got != c.want {
			t.Errorf("%s expiring %d: %v, want %v", c.status, c.expires, got, c.want)
		}
	}
}

func TestInviteRole(t *testing.T) {
	forbidden := E(CodeForbidden, "You are not allowed to invite users to this organization")
	cases := []struct {
		inviter, role string
		want          string
		err           error
	}{
		{RoleOwner, "", RoleMember, nil},
		{RoleOwner, RoleMember, RoleMember, nil},
		{RoleOwner, RoleAdmin, RoleAdmin, nil},
		{RoleOwner, RoleOwner, RoleOwner, nil},
		{RoleAdmin, RoleMember, RoleMember, nil},
		{RoleAdmin, RoleAdmin, RoleAdmin, nil},
		{RoleAdmin, RoleOwner, "", E(CodeForbidden, "You are not allowed to invite a user with this role")},
		{RoleMember, RoleMember, "", forbidden},
		{"", RoleMember, "", forbidden},
		{RoleOwner, "superuser", "", Invalid("Role not found: superuser")},
	}
	for _, c := range cases {
		got, err := InviteRole(c.inviter, c.role)
		if got != c.want || !reflect.DeepEqual(err, c.err) {
			t.Errorf("InviteRole(%q, %q) = %q, %v; want %q, %v", c.inviter, c.role, got, err, c.want, c.err)
		}
	}
}

func TestDeviceCodes(t *testing.T) {
	dc := regexp.MustCompile(`^[a-zA-Z0-9]{40}$`)
	uc := regexp.MustCompile(`^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}$`)
	seen := map[string]bool{}
	for range 200 {
		d, u := NewDeviceCode(), NewUserCode()
		if !dc.MatchString(d) {
			t.Fatalf("device code %q", d)
		}
		if !uc.MatchString(u) {
			t.Fatalf("user code %q", u)
		}
		if seen[d] {
			t.Fatal("device code repeated")
		}
		seen[d] = true
	}
	if CleanUserCode("ABCD-EFGH") != "ABCDEFGH" || CleanUserCode("abcd-efgh") != "abcdefgh" {
		t.Fatal("CleanUserCode strips dashes only")
	}
	if CheckDeviceClient("keel-cli") != nil {
		t.Fatal("keel-cli refused")
	}
	if r := CheckDeviceClient("other"); r == nil || r.Status != 400 || r.Code != "invalid_client" || r.Description != "Invalid client ID" {
		t.Fatalf("other client: %+v", r)
	}
}

func authI64(v int64) *int64 { return &v }

func TestDecidePoll(t *testing.T) {
	const now = int64(1_000_000)
	base := DeviceCode{ClientID: DeviceClientID, Status: DevicePending, IntervalS: 5, ExpiresAt: now + 60_000}
	with := func(f func(*DeviceCode)) *DeviceCode { d := base; f(&d); return &d }
	cases := []struct {
		name   string
		dc     *DeviceCode
		client string
		action PollAction
		status int
		code   string
		desc   string
	}{
		{"wrong client", &base, "other", PollRefuse, 400, "invalid_grant", "Invalid client ID"},
		{"unknown code", nil, DeviceClientID, PollRefuse, 400, "invalid_grant", "Invalid device code"},
		{"client mismatch", with(func(d *DeviceCode) { d.ClientID = "x" }), DeviceClientID, PollRefuse, 400, "invalid_grant", "Client ID mismatch"},
		{"slow down", with(func(d *DeviceCode) { d.LastPolledAt = authI64(now - 4_999) }), DeviceClientID, PollRefuse, 400, "slow_down", "Polling too frequently"},
		{"slow down beats expiry", with(func(d *DeviceCode) { d.LastPolledAt = authI64(now - 1); d.ExpiresAt = now - 1 }), DeviceClientID, PollRefuse, 400, "slow_down", "Polling too frequently"},
		{"interval elapsed", with(func(d *DeviceCode) { d.LastPolledAt = authI64(now - 5_000) }), DeviceClientID, PollTouch, 400, "authorization_pending", "Authorization pending"},
		{"pending", &base, DeviceClientID, PollTouch, 400, "authorization_pending", "Authorization pending"},
		{"expired", with(func(d *DeviceCode) { d.ExpiresAt = now - 1 }), DeviceClientID, PollTouchDelete, 400, "expired_token", "Device code has expired"},
		{"expires now is still valid", with(func(d *DeviceCode) { d.ExpiresAt = now }), DeviceClientID, PollTouch, 400, "authorization_pending", "Authorization pending"},
		{"denied", with(func(d *DeviceCode) { d.Status = DeviceDenied; d.UserID = "u" }), DeviceClientID, PollTouchDelete, 400, "access_denied", "Access denied"},
		{"approved", with(func(d *DeviceCode) { d.Status = DeviceApproved; d.UserID = "u" }), DeviceClientID, PollIssue, 0, "", ""},
		{"approved without user", with(func(d *DeviceCode) { d.Status = DeviceApproved }), DeviceClientID, PollTouch, 500, "server_error", "Invalid device code status"},
		{"no stored client", with(func(d *DeviceCode) { d.ClientID = "" }), DeviceClientID, PollTouch, 400, "authorization_pending", "Authorization pending"},
	}
	for _, c := range cases {
		got := DecidePoll(c.dc, c.client, now)
		if got.Action != c.action {
			t.Errorf("%s: action %d, want %d", c.name, got.Action, c.action)
		}
		if c.action == PollIssue {
			if got.Refusal != nil {
				t.Errorf("%s: refusal %+v", c.name, got.Refusal)
			}
			continue
		}
		r := got.Refusal
		if r == nil || r.Status != c.status || r.Code != c.code || r.Description != c.desc {
			t.Errorf("%s: %+v, want %d %s %q", c.name, r, c.status, c.code, c.desc)
		}
	}
}

func TestDecideDevice(t *testing.T) {
	const now = int64(1_000_000)
	pending := DeviceCode{Status: DevicePending, UserID: "u1", ExpiresAt: now + 1}
	with := func(f func(*DeviceCode)) *DeviceCode { d := pending; f(&d); return &d }
	cases := []struct {
		name    string
		dc      *DeviceCode
		user    string
		approve bool
		status  int
		code    string
		desc    string
	}{
		{"signed out", &pending, "", true, 401, "unauthorized", "Authentication required"},
		{"signed out before lookup", nil, "", true, 401, "unauthorized", "Authentication required"},
		{"unknown", nil, "u1", true, 400, "invalid_request", "Invalid user code"},
		{"expired", with(func(d *DeviceCode) { d.ExpiresAt = now - 1 }), "u1", true, 400, "expired_token", "User code has expired"},
		{"processed", with(func(d *DeviceCode) { d.Status = DeviceApproved }), "u1", true, 400, "invalid_request", "Device code already processed"},
		{"unclaimed", with(func(d *DeviceCode) { d.UserID = "" }), "u1", true, 400, "invalid_request", MsgDeviceNotClaimed},
		{"someone else approves", &pending, "u2", true, 403, "access_denied", "You are not authorized to approve this device authorization"},
		{"someone else denies", &pending, "u2", false, 403, "access_denied", "You are not authorized to deny this device authorization"},
		{"approve", &pending, "u1", true, 0, "", ""},
		{"deny", &pending, "u1", false, 0, "", ""},
	}
	for _, c := range cases {
		r := DecideDevice(c.dc, c.user, c.approve, now)
		if c.status == 0 {
			if r != nil {
				t.Errorf("%s: refused %+v", c.name, r)
			}
			continue
		}
		if r == nil || r.Status != c.status || r.Code != c.code || r.Description != c.desc {
			t.Errorf("%s: %+v, want %d %s %q", c.name, r, c.status, c.code, c.desc)
		}
	}
	if !strings.Contains(MsgDeviceNotClaimed, "`GET /device`") {
		t.Fatal("not-claimed message must keep its backticks")
	}
}

func TestCheckUserCodeAndBind(t *testing.T) {
	const now = int64(50)
	if r := CheckUserCode(nil, now); r == nil || r.Code != "invalid_request" || r.Description != "Invalid user code" {
		t.Fatalf("unknown: %+v", r)
	}
	if r := CheckUserCode(&DeviceCode{ExpiresAt: now - 1}, now); r == nil || r.Code != "expired_token" || r.Description != "User code has expired" {
		t.Fatalf("expired: %+v", r)
	}
	if r := CheckUserCode(&DeviceCode{ExpiresAt: now}, now); r != nil {
		t.Fatalf("valid: %+v", r)
	}
	cases := []struct {
		dc   DeviceCode
		user string
		want bool
	}{
		{DeviceCode{Status: DevicePending}, "u", true},
		{DeviceCode{Status: DevicePending}, "", false},
		{DeviceCode{Status: DevicePending, UserID: "v"}, "u", false},
		{DeviceCode{Status: DeviceApproved}, "u", false},
		{DeviceCode{Status: DeviceDenied}, "u", false},
	}
	for _, c := range cases {
		if got := c.dc.ShouldBind(c.user); got != c.want {
			t.Errorf("ShouldBind(%+v, %q) = %v", c.dc, c.user, got)
		}
	}
}
func TestRateLimitError(t *testing.T) {
	var de *Error
	if !errors.As(&RateLimitError{RetryAfterSeconds: 30}, &de) || de.Code != CodeRateLimited || de.Message != MsgTooManyRequests {
		t.Fatalf("RateLimitError unwraps to %+v", de)
	}
}
