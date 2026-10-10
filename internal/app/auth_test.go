package app_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ThallesP/keel/internal/adapters/password"
	"github.com/ThallesP/keel/internal/adapters/sqlite"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

type authRecorder struct {
	mu     sync.Mutex
	topics map[string]map[string]bool
}

func (r *authRecorder) Publish(org string, topics []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.topics == nil {
		r.topics = map[string]map[string]bool{}
	}
	if r.topics[org] == nil {
		r.topics[org] = map[string]bool{}
	}
	for _, t := range topics {
		r.topics[org][t] = true
	}
}

func (r *authRecorder) take(org string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for t := range r.topics[org] {
		out = append(out, t)
	}
	delete(r.topics, org)
	sort.Strings(out)
	return out
}

type authFixture struct {
	t      *testing.T
	ctx    context.Context
	app    *app.App
	store  *sqlite.Store
	events *authRecorder
	now    int64
	hasher *password.Hasher
}

const authT0 = int64(1_800_000_000_000)

func authSetup(t *testing.T) *authFixture {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "keel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f := &authFixture{t: t, ctx: context.Background(), store: store, events: &authRecorder{}, now: authT0,
		hasher: &password.Hasher{Params: password.Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}}}
	f.app = app.New(app.App{
		Store: store, Events: f.events, Passwords: f.hasher,
		Config: app.Config{SiteURL: "http://keel.test"},
		Now:    func() int64 { return f.now },
	})
	return f
}

var authClient = app.ClientInfo{IP: "100.64.0.9", UserAgent: "test"}

func (f *authFixture) signUp(email, invitationID string) app.SignedIn {
	f.t.Helper()
	out, err := f.app.SignUp(f.ctx, app.SignUpInput{Email: email, Password: "correct-horse-battery", Name: "N " + email,
		InvitationID: invitationID, Client: authClient})
	if err != nil {
		f.t.Fatalf("sign up %s: %v", email, err)
	}
	return out
}

func (f *authFixture) actor(token string) domain.Actor {
	f.t.Helper()
	a, err := f.app.ResolveSession(f.ctx, token)
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

func (f *authFixture) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.store.DB().Exec(q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *authFixture) insertUser(id, email, hash string) {
	f.exec(`INSERT INTO users (id, email, name, password_hash, created_at, updated_at) VALUES (?, ?, 'Guest', ?, 1, 1)`, id, email, hash)
}

func (f *authFixture) hash(pw string) string {
	h, err := f.hasher.Hash(pw)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

func authWant(t *testing.T, err error, code, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s %q", code, msg)
	}
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("error %v (%T), want %s %q", err, err, code, msg)
	}
	if de.Code != code || de.Message != msg {
		t.Fatalf("got %s %q, want %s %q", de.Code, de.Message, code, msg)
	}
}

func authWantRefusal(t *testing.T, err error, status int, code, desc string) {
	t.Helper()
	var r *domain.DeviceRefusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v (%T), want refusal %d %s", err, err, status, code)
	}
	if r.Status != status || r.Code != code || r.Description != desc {
		t.Fatalf("got %d %s %q, want %d %s %q", r.Status, r.Code, r.Description, status, code, desc)
	}
}

func TestAuthSignUpFoundsThenInvites(t *testing.T) {
	f := authSetup(t)
	open, err := f.app.SignUpOpen(f.ctx)
	if err != nil || !open {
		t.Fatalf("sign-up open on a fresh install: %v %v", open, err)
	}

	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "not-an-email", Password: "correct-horse-battery"})
	authWant(t, err, domain.CodeInvalidInput, "Invalid email")
	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "a@example.com", Password: "short"})
	authWant(t, err, domain.CodeInvalidInput, "Password too short")
	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "a@example.com", Password: strings.Repeat("x", 129)})
	authWant(t, err, domain.CodeInvalidInput, "Password too long")

	owner := f.signUp(" Founder@Example.com ", "")
	if owner.User.Email != "founder@example.com" || len(owner.Token) < 50 {
		t.Fatalf("first account: %+v", owner)
	}
	if owner.Session.ExpiresAt != authT0+domain.SessionTTL || owner.Session.IP != authClient.IP {
		t.Fatalf("session: %+v", owner.Session)
	}
	oa := f.actor(owner.Token)
	if oa.UserID != owner.User.ID || oa.OrganizationID == "" || oa.Role != domain.RoleOwner {
		t.Fatalf("owner actor: %+v", oa)
	}
	me, err := f.app.GetMe(f.ctx, oa)
	if err != nil || me.User == nil || me.Organization == nil {
		t.Fatalf("me: %+v %v", me, err)
	}
	if *me.Organization != (app.MyOrganization{ID: oa.OrganizationID, Name: "Default", Slug: "default", Role: "owner"}) {
		t.Fatalf("organization: %+v", *me.Organization)
	}
	if got := f.events.take(oa.OrganizationID); strings.Join(got, ",") != "/api/me,/api/organization" {
		t.Fatalf("founding published %v", got)
	}
	if open, _ := f.app.SignUpOpen(f.ctx); open {
		t.Fatal("sign-up still open after the first account")
	}

	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "FOUNDER@example.com", Password: "correct-horse-battery"})
	authWant(t, err, domain.CodeConflict, "User already exists. Use another email.")
	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "second@example.com", Password: "correct-horse-battery"})
	authWant(t, err, domain.CodeForbidden, "Sign-up is by invitation. Ask a member for an invite link.")
	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "second@example.com", Password: "correct-horse-battery", InvitationID: "nope"})
	authWant(t, err, domain.CodeForbidden, "Sign-up is by invitation. Ask a member for an invite link.")

	inv, err := f.app.CreateInvitation(f.ctx, oa, "  Second@Example.com ", "")
	if err != nil {
		t.Fatal(err)
	}
	if inv.Email != "second@example.com" || inv.Role != "member" || inv.ExpiresAt != authT0+domain.InvitationTTL || len(inv.ID) != 26 {
		t.Fatalf("invitation: %+v", inv)
	}
	if got := f.events.take(oa.OrganizationID); strings.Join(got, ",") != "/api/organization" {
		t.Fatalf("invite published %v", got)
	}
	pub, err := f.app.GetInvitation(f.ctx, inv.ID)
	if err != nil || pub == nil || *pub != (app.PublicInvitation{Email: "second@example.com", Organization: "Default"}) {
		t.Fatalf("public invitation: %+v %v", pub, err)
	}

	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "third@example.com", Password: "correct-horse-battery", InvitationID: inv.ID})
	authWant(t, err, domain.CodeForbidden, "Sign-up is by invitation. Ask a member for an invite link.")

	f.now += 1000
	second := f.signUp("SECOND@example.com", inv.ID)
	sa := f.actor(second.Token)
	if sa.OrganizationID != oa.OrganizationID || sa.Role != domain.RoleMember {
		t.Fatalf("invited actor: %+v", sa)
	}
	if got := f.events.take(oa.OrganizationID); strings.Join(got, ",") != "/api/me,/api/organization" {
		t.Fatalf("join published %v", got)
	}
	if pub, _ := f.app.GetInvitation(f.ctx, inv.ID); pub != nil {
		t.Fatalf("spent invitation still shows: %+v", pub)
	}
	members, err := f.app.ListMembers(f.ctx, sa)
	if err != nil || len(members) != 2 || members[0].Email != "founder@example.com" || members[0].Role != "owner" ||
		members[1].Email != "second@example.com" || members[1].Role != "member" {
		t.Fatalf("members: %+v %v", members, err)
	}
	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "again@example.com", Password: "correct-horse-battery", InvitationID: inv.ID})
	authWant(t, err, domain.CodeForbidden, "Sign-up is by invitation. Ask a member for an invite link.")
}

func TestAuthSignUpWithExpiredInvitation(t *testing.T) {
	f := authSetup(t)
	oa := f.actor(f.signUp("owner@example.com", "").Token)
	inv, err := f.app.CreateInvitation(f.ctx, oa, "late@example.com", "member")
	if err != nil {
		t.Fatal(err)
	}
	f.now += domain.InvitationTTL
	if pub, _ := f.app.GetInvitation(f.ctx, inv.ID); pub == nil {
		t.Fatal("invitation should stand until its last millisecond")
	}
	f.now++
	if pub, _ := f.app.GetInvitation(f.ctx, inv.ID); pub != nil {
		t.Fatal("expired invitation still shows")
	}
	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "late@example.com", Password: "correct-horse-battery", InvitationID: inv.ID})
	authWant(t, err, domain.CodeForbidden, "Sign-up is by invitation. Ask a member for an invite link.")
	if invs, _ := f.app.ListInvitations(f.ctx, oa); len(invs) != 0 {
		t.Fatalf("expired invitation listed: %+v", invs)
	}
}

func TestAuthSignIn(t *testing.T) {
	f := authSetup(t)
	f.signUp("ci@example.com", "")

	_, err := f.app.SignIn(f.ctx, "nope", "x", authClient)
	authWant(t, err, domain.CodeInvalidInput, "Invalid email")
	for _, c := range []struct{ email, pw string }{
		{"ci@example.com", "wrong-password"},
		{"nobody@example.com", "correct-horse-battery"},
		{"ci@example.com", ""},
	} {
		_, err := f.app.SignIn(f.ctx, c.email, c.pw, authClient)
		authWant(t, err, domain.CodeNotAuthenticated, "Invalid email or password")
	}
	out, err := f.app.SignIn(f.ctx, "CI@Example.com", "correct-horse-battery", authClient)
	if err != nil {
		t.Fatal(err)
	}
	a := f.actor(out.Token)
	if a.Email != "ci@example.com" || a.OrganizationID == "" {
		t.Fatalf("signed-in actor: %+v", a)
	}
}

func TestAuthSignInLimiter(t *testing.T) {
	f := authSetup(t)
	f.signUp("ci@example.com", "")
	for i := 0; i < app.SignInAttempts; i++ {
		_, err := f.app.SignIn(f.ctx, "ci@example.com", "wrong-password", authClient)
		authWant(t, err, domain.CodeNotAuthenticated, "Invalid email or password")
		f.now += 1000
	}
	_, err := f.app.SignIn(f.ctx, "ci@example.com", "correct-horse-battery", authClient)
	var limited *domain.RateLimitError
	if !errors.As(err, &limited) {
		t.Fatalf("11th try: %v", err)
	}
	if limited.RetryAfterSeconds != 290 {
		t.Fatalf("retry after %d", limited.RetryAfterSeconds)
	}
	authWant(t, err, domain.CodeRateLimited, "Too many requests. Please try again later.")

	if _, err := f.app.SignIn(f.ctx, "ci@example.com", "correct-horse-battery", app.ClientInfo{IP: "100.64.0.10"}); err != nil {
		t.Fatalf("other IP: %v", err)
	}
	_, err = f.app.SignIn(f.ctx, "other@example.com", "x-password", authClient)
	authWant(t, err, domain.CodeNotAuthenticated, "Invalid email or password")

	f.now = authT0 + app.SignInWindow.Milliseconds()
	if _, err := f.app.SignIn(f.ctx, "ci@example.com", "correct-horse-battery", authClient); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	for i := 0; i < app.SignInAttempts; i++ {
		if _, err := f.app.SignIn(f.ctx, "ci@example.com", "wrong-password", authClient); errors.As(err, &limited) {
			t.Fatalf("limited after a success reset, try %d", i)
		}
	}
}

func TestAuthSessionLifetime(t *testing.T) {
	f := authSetup(t)
	s := f.signUp("ci@example.com", "")

	for _, tok := range []string{"", "unknown"} {
		if a := f.actor(tok); a.SignedIn() {
			t.Fatalf("token %q resolved to %+v", tok, a)
		}
	}
	a := f.actor(s.Token)
	if a.SessionRenewed || a.SessionExpiresAt != authT0+domain.SessionTTL {
		t.Fatalf("fresh session: %+v", a)
	}

	f.now = authT0 + domain.SessionUpdateAge - 1
	if a := f.actor(s.Token); a.SessionRenewed {
		t.Fatal("renewed too early")
	}
	f.now = authT0 + domain.SessionUpdateAge
	a = f.actor(s.Token)
	if !a.SessionRenewed || a.SessionExpiresAt != f.now+domain.SessionTTL {
		t.Fatalf("renewal: %+v", a)
	}
	if a := f.actor(s.Token); a.SessionRenewed {
		t.Fatal("renewed twice in a row")
	}

	f.now += domain.SessionTTL
	if a := f.actor(s.Token); a.SignedIn() {
		t.Fatalf("expired session resolved: %+v", a)
	}
	var n int
	_ = f.store.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d sessions left", n)
	}

	one, err := f.app.SignIn(f.ctx, "ci@example.com", "correct-horse-battery", authClient)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := f.app.SignIn(f.ctx, "ci@example.com", "correct-horse-battery", authClient)
	if err := f.app.SignOut(f.ctx, f.actor(one.Token)); err != nil {
		t.Fatal(err)
	}
	if f.actor(one.Token).SignedIn() || !f.actor(two.Token).SignedIn() {
		t.Fatal("sign-out deleted the wrong session")
	}
	if err := f.app.SignOut(f.ctx, domain.Actor{}); err != nil {
		t.Fatalf("signed-out sign-out: %v", err)
	}
	var stored string
	_ = f.store.DB().QueryRow(`SELECT token_hash FROM sessions`).Scan(&stored)
	if stored == two.Token || stored != domain.HashSecret(two.Token) {
		t.Fatalf("stored token %q", stored)
	}
}

func TestAuthInvitationRules(t *testing.T) {
	f := authSetup(t)
	owner := f.actor(f.signUp("owner@example.com", "").Token)
	org := owner.OrganizationID

	inv, err := f.app.CreateInvitation(f.ctx, owner, "admin@example.com", "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin := f.actor(f.signUp("admin@example.com", inv.ID).Token)
	inv, _ = f.app.CreateInvitation(f.ctx, owner, "member@example.com", "")
	member := f.actor(f.signUp("member@example.com", inv.ID).Token)
	if admin.Role != "admin" || member.Role != "member" {
		t.Fatalf("roles: %s %s", admin.Role, member.Role)
	}

	_, err = f.app.CreateInvitation(f.ctx, member, "x@example.com", "")
	authWant(t, err, domain.CodeForbidden, "You are not allowed to invite users to this organization")
	_, err = f.app.CreateInvitation(f.ctx, admin, "x@example.com", "owner")
	authWant(t, err, domain.CodeForbidden, "You are not allowed to invite a user with this role")
	_, err = f.app.CreateInvitation(f.ctx, owner, "x@example.com", "root")
	authWant(t, err, domain.CodeInvalidInput, "Role not found: root")
	_, err = f.app.CreateInvitation(f.ctx, owner, "x@", "")
	authWant(t, err, domain.CodeInvalidInput, "Invalid email")
	_, err = f.app.CreateInvitation(f.ctx, owner, "MEMBER@example.com", "")
	authWant(t, err, domain.CodeConflict, "User is already a member of this organization")
	_, err = f.app.CreateInvitation(f.ctx, domain.Actor{}, "x@example.com", "")
	authWant(t, err, domain.CodeNotAuthenticated, "Not authenticated")

	first, err := f.app.CreateInvitation(f.ctx, admin, "x@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.app.CreateInvitation(f.ctx, owner, "X@example.com", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if pub, _ := f.app.GetInvitation(f.ctx, first.ID); pub != nil {
		t.Fatal("re-invite left the first link standing")
	}
	invs, err := f.app.ListInvitations(f.ctx, member)
	if err != nil || len(invs) != 1 || invs[0].ID != second.ID || invs[0].Role != "admin" || invs[0].InviterID != owner.UserID {
		t.Fatalf("list: %+v %v", invs, err)
	}

	err = f.app.CancelInvitation(f.ctx, member, second.ID)
	authWant(t, err, domain.CodeForbidden, "You are not allowed to cancel this invitation")
	err = f.app.CancelInvitation(f.ctx, owner, "unknown")
	authWant(t, err, domain.CodeNotFound, "Invitation not found")
	f.events.take(org)
	if err := f.app.CancelInvitation(f.ctx, admin, second.ID); err != nil {
		t.Fatal(err)
	}
	if got := f.events.take(org); strings.Join(got, ",") != "/api/organization" {
		t.Fatalf("cancel published %v", got)
	}
	if invs, _ := f.app.ListInvitations(f.ctx, owner); len(invs) != 0 {
		t.Fatalf("cancelled invitation listed: %+v", invs)
	}
	if err := f.app.CancelInvitation(f.ctx, owner, second.ID); err != nil {
		t.Fatalf("cancelling twice: %v", err)
	}
	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "x@example.com", Password: "correct-horse-battery", InvitationID: second.ID})
	authWant(t, err, domain.CodeForbidden, "Sign-up is by invitation. Ask a member for an invite link.")
}

func TestAuthInvitationLimit(t *testing.T) {
	f := authSetup(t)
	owner := f.actor(f.signUp("owner@example.com", "").Token)
	for i := 0; i < domain.InvitationLimit; i++ {
		if _, err := f.app.CreateInvitation(f.ctx, owner, "u"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26))+"@example.com", ""); err != nil {
			t.Fatalf("invitation %d: %v", i, err)
		}
	}
	_, err := f.app.CreateInvitation(f.ctx, owner, "one-more@example.com", "")
	authWant(t, err, domain.CodeForbidden, "Invitation limit reached")
	if _, err := f.app.CreateInvitation(f.ctx, owner, "uaa@example.com", ""); err != nil {
		t.Fatalf("re-invite at the limit: %v", err)
	}
	f.now += domain.InvitationTTL + 1
	if _, err := f.app.CreateInvitation(f.ctx, owner, "one-more@example.com", ""); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
}

func TestAuthAcceptInvitation(t *testing.T) {
	f := authSetup(t)
	owner := f.actor(f.signUp("owner@example.com", "").Token)
	f.insertUser("guest1", "guest@example.com", f.hash("guest-password"))
	f.insertUser("guest2", "other@example.com", f.hash("guest-password"))
	signIn := func(email string) domain.Actor {
		out, err := f.app.SignIn(f.ctx, email, "guest-password", authClient)
		if err != nil {
			t.Fatal(err)
		}
		return f.actor(out.Token)
	}
	guest, other := signIn("guest@example.com"), signIn("other@example.com")

	inv, err := f.app.CreateInvitation(f.ctx, owner, "Guest@Example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.app.AcceptInvitation(f.ctx, domain.Actor{}, inv.ID)
	authWant(t, err, domain.CodeNotAuthenticated, "Not authenticated")
	_, err = f.app.AcceptInvitation(f.ctx, guest, "unknown")
	authWant(t, err, domain.CodeNotFound, "Invitation not found")
	_, err = f.app.AcceptInvitation(f.ctx, other, inv.ID)
	authWant(t, err, domain.CodeForbidden, "You are not the recipient of the invitation")
	_, err = f.app.AcceptInvitation(f.ctx, owner, inv.ID)
	authWant(t, err, domain.CodeForbidden, "You are not the recipient of the invitation")

	f.events.take(owner.OrganizationID)
	org, err := f.app.AcceptInvitation(f.ctx, guest, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if org != (app.MyOrganization{ID: owner.OrganizationID, Name: "Default", Slug: "default", Role: "member"}) {
		t.Fatalf("accepted: %+v", org)
	}
	if got := f.events.take(owner.OrganizationID); strings.Join(got, ",") != "/api/me,/api/organization" {
		t.Fatalf("accept published %v", got)
	}
	_, err = f.app.AcceptInvitation(f.ctx, guest, inv.ID)
	authWant(t, err, domain.CodeNotFound, "Invitation not found")

	again, _ := f.app.CreateInvitation(f.ctx, owner, "other@example.com", "")
	f.exec(`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org2', 'Second', 'second', 1)`)
	f.exec(`INSERT INTO members (id, organization_id, user_id, role, created_at) VALUES ('m2', 'org2', 'guest2', 'owner', 1)`)
	other = signIn("other@example.com")
	_, err = f.app.AcceptInvitation(f.ctx, other, again.ID)
	authWant(t, err, domain.CodeConflict, "You're already in an organization")
}

func TestAuthForeignOrganizationIsMissing(t *testing.T) {
	f := authSetup(t)
	owner := f.actor(f.signUp("owner@example.com", "").Token)
	inv, err := f.app.CreateInvitation(f.ctx, owner, "guest@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO organizations (id, name, slug, created_at) VALUES ('orgb', 'Other', 'other', 1)`)
	f.insertUser("ub", "b@example.com", f.hash("b-password-123"))
	f.exec(`INSERT INTO members (id, organization_id, user_id, role, created_at) VALUES ('mb', 'orgb', 'ub', 'owner', 1)`)
	out, err := f.app.SignIn(f.ctx, "b@example.com", "b-password-123", authClient)
	if err != nil {
		t.Fatal(err)
	}
	b := f.actor(out.Token)
	if b.OrganizationID != "orgb" {
		t.Fatalf("b: %+v", b)
	}

	if invs, err := f.app.ListInvitations(f.ctx, b); err != nil || len(invs) != 0 {
		t.Fatalf("b sees invitations: %+v %v", invs, err)
	}
	if ms, err := f.app.ListMembers(f.ctx, b); err != nil || len(ms) != 1 || ms[0].UserID != "ub" {
		t.Fatalf("b sees members: %+v %v", ms, err)
	}
	err = f.app.CancelInvitation(f.ctx, b, inv.ID)
	authWant(t, err, domain.CodeNotFound, "Invitation not found")
	if pub, _ := f.app.GetInvitation(f.ctx, inv.ID); pub == nil {
		t.Fatal("b's cancel touched the invitation")
	}
	me, _ := f.app.GetMe(f.ctx, b)
	if me.Organization == nil || me.Organization.ID != "orgb" {
		t.Fatalf("b's me: %+v", me.Organization)
	}

	start, err := f.app.StartDeviceLogin(f.ctx, "keel-cli", app.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.ClaimDeviceCode(f.ctx, owner, start.UserCode); err != nil {
		t.Fatal(err)
	}
	if v, _ := f.app.ClaimDeviceCode(f.ctx, b, start.UserCode); v.Status != domain.DevicePending {
		t.Fatalf("status %s", v.Status)
	}
	err = f.app.DecideDeviceLogin(f.ctx, b, start.UserCode, true)
	authWantRefusal(t, err, 403, "access_denied", "You are not authorized to approve this device authorization")

	_, err = f.app.ListMembers(f.ctx, domain.Actor{})
	authWant(t, err, domain.CodeNotAuthenticated, "Not authenticated")
	f.insertUser("loner", "loner@example.com", "")
	_, err = f.app.ListInvitations(f.ctx, domain.Actor{UserID: "loner"})
	authWant(t, err, domain.CodeNoOrganization, domain.MsgNoOrganization)
}

func TestAuthDeviceLogin(t *testing.T) {
	f := authSetup(t)
	alice := f.actor(f.signUp("alice@example.com", "").Token)
	poll := func(code string) (app.DeviceToken, error) {
		return f.app.PollDeviceLogin(f.ctx, domain.DeviceGrantType, code, "keel-cli", app.ClientInfo{UserAgent: "keel-cli/test"})
	}

	_, err := f.app.StartDeviceLogin(f.ctx, "someone-else", app.ClientInfo{})
	authWantRefusal(t, err, 400, "invalid_client", "Invalid client ID")

	start, err := f.app.StartDeviceLogin(f.ctx, "keel-cli", app.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if len(start.DeviceCode) != 40 || len(start.UserCode) != 8 || start.ExpiresIn != 1800 || start.Interval != 5 ||
		start.VerificationURI != "http://keel.test/device" ||
		start.VerificationURIComplete != "http://keel.test/device?user_code="+start.UserCode {
		t.Fatalf("start: %+v", start)
	}
	pretty := start.UserCode[:4] + "-" + start.UserCode[4:]

	_, err = f.app.PollDeviceLogin(f.ctx, "password", start.DeviceCode, "keel-cli", app.ClientInfo{})
	authWantRefusal(t, err, 400, "unsupported_grant_type", "Unsupported grant type")
	_, err = f.app.PollDeviceLogin(f.ctx, domain.DeviceGrantType, start.DeviceCode, "other", app.ClientInfo{})
	authWantRefusal(t, err, 400, "invalid_grant", "Invalid client ID")
	_, err = poll("not-a-code")
	authWantRefusal(t, err, 400, "invalid_grant", "Invalid device code")
	_, err = poll(start.DeviceCode)
	authWantRefusal(t, err, 400, "authorization_pending", "Authorization pending")
	f.now += 4_999
	_, err = poll(start.DeviceCode)
	authWantRefusal(t, err, 400, "slow_down", "Polling too frequently")
	f.now += 1
	_, err = poll(start.DeviceCode)
	authWantRefusal(t, err, 400, "authorization_pending", "Authorization pending")

	err = f.app.DecideDeviceLogin(f.ctx, domain.Actor{}, pretty, true)
	authWantRefusal(t, err, 401, "unauthorized", "Authentication required")
	err = f.app.DecideDeviceLogin(f.ctx, alice, pretty, true)
	authWantRefusal(t, err, 400, "invalid_request", domain.MsgDeviceNotClaimed)
	err = f.app.DecideDeviceLogin(f.ctx, alice, "ZZZZ-ZZZZ", true)
	authWantRefusal(t, err, 400, "invalid_request", "Invalid user code")

	v, err := f.app.ClaimDeviceCode(f.ctx, domain.Actor{}, pretty)
	if err != nil || v.UserCode != pretty || v.Status != domain.DevicePending {
		t.Fatalf("anonymous lookup: %+v %v", v, err)
	}
	err = f.app.DecideDeviceLogin(f.ctx, alice, pretty, true)
	authWantRefusal(t, err, 400, "invalid_request", domain.MsgDeviceNotClaimed)
	_, err = f.app.ClaimDeviceCode(f.ctx, alice, "nope")
	authWantRefusal(t, err, 400, "invalid_request", "Invalid user code")

	if _, err := f.app.ClaimDeviceCode(f.ctx, alice, pretty); err != nil {
		t.Fatal(err)
	}
	inv, _ := f.app.CreateInvitation(f.ctx, alice, "bob@example.com", "")
	bob := f.actor(f.signUp("bob@example.com", inv.ID).Token)
	if _, err := f.app.ClaimDeviceCode(f.ctx, bob, start.UserCode); err != nil {
		t.Fatal(err)
	}
	err = f.app.DecideDeviceLogin(f.ctx, bob, start.UserCode, false)
	authWantRefusal(t, err, 403, "access_denied", "You are not authorized to deny this device authorization")

	if err := f.app.DecideDeviceLogin(f.ctx, alice, pretty, true); err != nil {
		t.Fatal(err)
	}
	err = f.app.DecideDeviceLogin(f.ctx, alice, pretty, false)
	authWantRefusal(t, err, 400, "invalid_request", "Device code already processed")
	if v, _ := f.app.ClaimDeviceCode(f.ctx, alice, start.UserCode); v.Status != domain.DeviceApproved {
		t.Fatalf("status after approval: %s", v.Status)
	}

	f.now += 5_000
	tok, err := poll(start.DeviceCode)
	if err != nil {
		t.Fatal(err)
	}
	if tok.ExpiresIn != domain.SessionTTL/1000 {
		t.Fatalf("expires_in %d", tok.ExpiresIn)
	}
	if a := f.actor(tok.AccessToken); a.UserID != alice.UserID || a.OrganizationID != alice.OrganizationID {
		t.Fatalf("CLI session: %+v", a)
	}
	f.now += 5_000
	_, err = poll(start.DeviceCode)
	authWantRefusal(t, err, 400, "invalid_grant", "Invalid device code")
	_, err = f.app.ClaimDeviceCode(f.ctx, alice, pretty)
	authWantRefusal(t, err, 400, "invalid_request", "Invalid user code")
}

func TestAuthDeviceLoginDeniedAndExpired(t *testing.T) {
	f := authSetup(t)
	alice := f.actor(f.signUp("alice@example.com", "").Token)
	poll := func(code string) error {
		_, err := f.app.PollDeviceLogin(f.ctx, domain.DeviceGrantType, code, "keel-cli", app.ClientInfo{})
		return err
	}

	denied, _ := f.app.StartDeviceLogin(f.ctx, "keel-cli", app.ClientInfo{})
	if _, err := f.app.ClaimDeviceCode(f.ctx, alice, denied.UserCode); err != nil {
		t.Fatal(err)
	}
	if err := f.app.DecideDeviceLogin(f.ctx, alice, denied.UserCode, false); err != nil {
		t.Fatal(err)
	}
	authWantRefusal(t, poll(denied.DeviceCode), 400, "access_denied", "Access denied")
	f.now += 5_000
	authWantRefusal(t, poll(denied.DeviceCode), 400, "invalid_grant", "Invalid device code")

	expired, _ := f.app.StartDeviceLogin(f.ctx, "keel-cli", app.ClientInfo{})
	f.now += domain.DeviceCodeTTL + 1
	_, err := f.app.ClaimDeviceCode(f.ctx, alice, expired.UserCode)
	authWantRefusal(t, err, 400, "expired_token", "User code has expired")
	err = f.app.DecideDeviceLogin(f.ctx, alice, expired.UserCode, true)
	authWantRefusal(t, err, 400, "expired_token", "User code has expired")
	authWantRefusal(t, poll(expired.DeviceCode), 400, "expired_token", "Device code has expired")
	authWantRefusal(t, poll(expired.DeviceCode), 400, "invalid_grant", "Invalid device code")

	stale, _ := f.app.StartDeviceLogin(f.ctx, "keel-cli", app.ClientInfo{})
	f.now += domain.DeviceCodeTTL + 1
	if _, err := f.app.StartDeviceLogin(f.ctx, "keel-cli", app.ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	_, err = f.app.ClaimDeviceCode(f.ctx, alice, stale.UserCode)
	authWantRefusal(t, err, 400, "expired_token", "User code has expired")
	authWantRefusal(t, poll(stale.DeviceCode), 400, "expired_token", "Device code has expired")

	forgotten, _ := f.app.StartDeviceLogin(f.ctx, "keel-cli", app.ClientInfo{})
	f.now += domain.DeviceCodeTTL + domain.DeviceCodeKeep + 1
	if _, err := f.app.StartDeviceLogin(f.ctx, "keel-cli", app.ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	_, err = f.app.ClaimDeviceCode(f.ctx, alice, forgotten.UserCode)
	authWantRefusal(t, err, 400, "invalid_request", "Invalid user code")
}

func TestAuthPerIPLimits(t *testing.T) {
	f := authSetup(t)
	spray := app.ClientInfo{IP: "100.64.0.66"}
	for i := 0; i < app.AuthPerIP; i++ {
		_, err := f.app.SignIn(f.ctx, fmt.Sprintf("victim%d@example.com", i), "Summer2026!", spray)
		authWant(t, err, domain.CodeNotAuthenticated, "Invalid email or password")
	}
	_, err := f.app.SignIn(f.ctx, "victim-next@example.com", "Summer2026!", spray)
	authWant(t, err, domain.CodeRateLimited, "Too many requests. Please try again later.")
	_, err = f.app.SignUp(f.ctx, app.SignUpInput{Email: "new@example.com", Password: "correct-horse-battery", Name: "N", Client: spray})
	authWant(t, err, domain.CodeRateLimited, "Too many requests. Please try again later.")
	if _, err := f.app.SignUp(f.ctx, app.SignUpInput{Email: "new@example.com", Password: "correct-horse-battery", Name: "N", Client: app.ClientInfo{IP: "100.64.0.67"}}); err != nil {
		t.Fatalf("other address: %v", err)
	}
	f.now += app.AuthPerIPWindow.Milliseconds()
	_, err = f.app.SignIn(f.ctx, "victim-next@example.com", "Summer2026!", spray)
	authWant(t, err, domain.CodeNotAuthenticated, "Invalid email or password")

	for i := 0; i < app.DeviceStartPerIP; i++ {
		if _, err := f.app.StartDeviceLogin(f.ctx, "keel-cli", spray); err != nil {
			t.Fatalf("device code %d: %v", i, err)
		}
	}
	_, err = f.app.StartDeviceLogin(f.ctx, "keel-cli", spray)
	authWant(t, err, domain.CodeRateLimited, "Too many requests. Please try again later.")

	_, err = f.app.SignIn(f.ctx, strings.Repeat("a", 1<<20)+"@example.com", "x", app.ClientInfo{IP: "100.64.0.68"})
	authWant(t, err, domain.CodeInvalidInput, domain.MsgInvalidEmail)
}
