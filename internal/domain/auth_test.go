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

func TestNewUserCode(t *testing.T) {
	re := regexp.MustCompile(`^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}$`)
	for range 200 {
		if u := NewUserCode(); !re.MatchString(u) {
			t.Fatalf("user code %q", u)
		}
	}
}

func TestDecidePoll(t *testing.T) {
	const now = int64(1_000_000)
	const later = now + 60_000
	slowDown := &DeviceRefusal{400, "slow_down", "Polling too frequently"}
	pending := &DeviceRefusal{400, "authorization_pending", "Authorization pending"}
	cases := []struct {
		name         string
		status       DeviceStatus
		lastPolledAt *int64
		expiresAt    int64
		action       PollAction
		refusal      *DeviceRefusal
	}{
		{"slow down", DevicePending, new(now - 4_999), later, PollRefuse, slowDown},
		{"slow down beats expiry", DevicePending, new(now - 1), now - 1, PollRefuse, slowDown},
		{"interval elapsed", DevicePending, new(now - 5_000), later, PollTouch, pending},
		{"pending", DevicePending, nil, later, PollTouch, pending},
		{"expired", DevicePending, nil, now - 1, PollTouchDelete, &DeviceRefusal{400, "expired_token", "Device code has expired"}},
		{"expires now is still valid", DevicePending, nil, now, PollTouch, pending},
		{"denied", DeviceDenied, nil, later, PollTouchDelete, &DeviceRefusal{400, "access_denied", "Access denied"}},
		{"approved", DeviceApproved, nil, later, PollIssue, nil},
	}
	for _, c := range cases {
		dc := DeviceCode{Status: c.status, IntervalS: 5, LastPolledAt: c.lastPolledAt, ExpiresAt: c.expiresAt}
		action, refusal := DecidePoll(dc, now)
		if action != c.action || !reflect.DeepEqual(refusal, c.refusal) {
			t.Errorf("%s: %d %+v, want %d %+v", c.name, action, refusal, c.action, c.refusal)
		}
	}
}

func TestDecideDevice(t *testing.T) {
	claimed := DeviceCode{Status: DevicePending, UserID: "u1"}
	cases := []struct {
		name    string
		dc      DeviceCode
		user    string
		approve bool
		refusal *DeviceRefusal
	}{
		{"processed", DeviceCode{Status: DeviceApproved, UserID: "u1"}, "u1", true, &DeviceRefusal{400, "invalid_request", "Device code already processed"}},
		{"unclaimed", DeviceCode{Status: DevicePending}, "u1", true, &DeviceRefusal{400, "invalid_request", MsgDeviceNotClaimed}},
		{"someone else approves", claimed, "u2", true, &DeviceRefusal{403, "access_denied", "You are not authorized to approve this device authorization"}},
		{"someone else denies", claimed, "u2", false, &DeviceRefusal{403, "access_denied", "You are not authorized to deny this device authorization"}},
		{"approve", claimed, "u1", true, nil},
		{"deny", claimed, "u1", false, nil},
	}
	for _, c := range cases {
		if r := DecideDevice(c.dc, c.user, c.approve); !reflect.DeepEqual(r, c.refusal) {
			t.Errorf("%s: %+v, want %+v", c.name, r, c.refusal)
		}
	}
}

func TestRateLimitError(t *testing.T) {
	var de *Error
	if !errors.As(&RateLimitError{RetryAfterSeconds: 30}, &de) || de.Code != CodeRateLimited || de.Message != MsgTooManyRequests {
		t.Fatalf("RateLimitError unwraps to %+v", de)
	}
}
