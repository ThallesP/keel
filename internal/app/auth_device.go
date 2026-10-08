package app

import (
	"context"
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

// `keel login`: RFC 8628 device authorization, exactly as better-auth's deviceAuthorization
// plugin answered it (docs/go/spec/cli-install.md §A8). Refusals are *domain.DeviceRefusal errors
// (the transport writes them as {error, error_description}, not as problems).

// DeviceStart is POST /api/auth/device/code's answer.
type DeviceStart struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int // seconds
	Interval                int // seconds
}

// DeviceToken is a successful POST /api/auth/device/token: a session for the approving account.
type DeviceToken struct {
	AccessToken string
	ExpiresIn   int64 // seconds until the session expires
}

// DeviceView is GET /api/auth/device: the code as the caller gave it and its status.
type DeviceView struct {
	UserCode string
	Status   domain.DeviceStatus
}

// StartDeviceLogin makes a login link for the keel CLI.
func (a *App) StartDeviceLogin(ctx context.Context, clientID string) (DeviceStart, error) {
	if r := domain.CheckDeviceClient(clientID); r != nil {
		return DeviceStart{}, r
	}
	deviceCode := domain.NewDeviceCode()
	var userCode string
	err := a.write(ctx, func(tx Tx, _ *Changes) error {
		now := a.Now()
		// Codes are only deleted when polled; sweep the stale ones so user codes stay free.
		if err := tx.AuthDeleteExpiredDeviceCodes(now); err != nil {
			return err
		}
		for {
			userCode = domain.NewUserCode()
			_, err := tx.AuthDeviceCodeByUserCode(userCode)
			if errors.Is(err, ErrNoRow) {
				break
			}
			if err != nil {
				return err
			}
		}
		return tx.AuthInsertDeviceCode(domain.DeviceCode{
			ID: domain.NewID(), UserCode: userCode, ClientID: clientID, Status: domain.DevicePending,
			IntervalS: domain.DeviceIntervalS, ExpiresAt: now + domain.DeviceCodeTTL, CreatedAt: now,
		}, domain.HashSecret(deviceCode))
	})
	if err != nil {
		return DeviceStart{}, err
	}
	uri := a.Config.SiteURL + "/device"
	return DeviceStart{
		DeviceCode: deviceCode, UserCode: userCode,
		VerificationURI: uri, VerificationURIComplete: uri + "?user_code=" + userCode,
		ExpiresIn: int(domain.DeviceCodeTTL / 1000), Interval: domain.DeviceIntervalS,
	}, nil
}

// PollDeviceLogin is one CLI poll. Once the code is approved it is consumed and the answer
// carries a new session token: the token is handed out once.
func (a *App) PollDeviceLogin(ctx context.Context, grantType, deviceCode, clientID string, client ClientInfo) (DeviceToken, error) {
	if grantType != domain.DeviceGrantType {
		return DeviceToken{}, &domain.DeviceRefusal{Status: 400, Code: "unsupported_grant_type", Description: domain.MsgDeviceGrantType}
	}
	var (
		out     DeviceToken
		refusal *domain.DeviceRefusal
	)
	// Refusals commit too: a poll records last_polled_at and deletes spent codes.
	err := a.write(ctx, func(tx Tx, _ *Changes) error {
		now := a.Now()
		var dc *domain.DeviceCode
		if clientID == domain.DeviceClientID {
			row, err := tx.AuthDeviceCodeByHash(domain.HashSecret(deviceCode))
			if err == nil {
				dc = &row
			} else if !errors.Is(err, ErrNoRow) {
				return err
			}
		}
		d := domain.DecidePoll(dc, clientID, now)
		refusal = d.Refusal
		switch d.Action {
		case domain.PollTouch:
			return tx.AuthSetDevicePolled(dc.ID, now)
		case domain.PollTouchDelete:
			return tx.AuthDeleteDeviceCode(dc.ID)
		case domain.PollIssue:
			consumed, err := tx.AuthConsumeApprovedDeviceCode(dc.ID)
			if err != nil {
				return err
			}
			if !consumed {
				refusal = &domain.DeviceRefusal{Status: 400, Code: "invalid_grant", Description: domain.MsgDeviceInvalidCode}
				return nil
			}
			user, err := tx.AuthUser(dc.UserID)
			if errors.Is(err, ErrNoRow) {
				refusal = &domain.DeviceRefusal{Status: 500, Code: "server_error", Description: domain.MsgDeviceUserNotFound}
				return nil
			}
			if err != nil {
				return err
			}
			s, err := issueSession(tx, user, now, client)
			if err != nil {
				return err
			}
			out = DeviceToken{AccessToken: s.Token, ExpiresIn: (s.Session.ExpiresAt - now) / 1000}
		}
		return nil
	})
	if err != nil {
		return DeviceToken{}, err
	}
	if refusal != nil {
		return DeviceToken{}, refusal
	}
	return out, nil
}

// ClaimDeviceCode is the dashboard's /device page looking a code up. While signed in it also
// binds an unclaimed pending code to the caller: only that account can approve or deny it.
func (a *App) ClaimDeviceCode(ctx context.Context, actor domain.Actor, userCode string) (DeviceView, error) {
	clean := domain.CleanUserCode(userCode)
	var out DeviceView
	look := func(tx Tx) (*domain.DeviceCode, error) {
		dc, err := tx.AuthDeviceCodeByUserCode(clean)
		if errors.Is(err, ErrNoRow) {
			return nil, domain.CheckUserCode(nil, a.Now())
		}
		if err != nil {
			return nil, err
		}
		if r := domain.CheckUserCode(&dc, a.Now()); r != nil {
			return nil, r
		}
		out = DeviceView{UserCode: userCode, Status: dc.Status}
		return &dc, nil
	}
	if !actor.SignedIn() {
		err := a.read(ctx, func(tx Tx) error { _, err := look(tx); return err })
		return out, err
	}
	err := a.write(ctx, func(tx Tx, _ *Changes) error {
		dc, err := look(tx)
		if err != nil {
			return err
		}
		if dc.ShouldBind(actor.UserID) {
			_, err = tx.AuthBindDeviceCode(dc.ID, actor.UserID)
		}
		return err
	})
	return out, err
}

// DecideDeviceLogin approves or denies a code; only the account it is bound to may.
func (a *App) DecideDeviceLogin(ctx context.Context, actor domain.Actor, userCode string, approve bool) error {
	if !actor.SignedIn() {
		return domain.DecideDevice(nil, "", approve, 0) // 401 unauthorized, before any lookup
	}
	clean := domain.CleanUserCode(userCode)
	return a.write(ctx, func(tx Tx, _ *Changes) error {
		var dc *domain.DeviceCode
		row, err := tx.AuthDeviceCodeByUserCode(clean)
		if err == nil {
			dc = &row
		} else if !errors.Is(err, ErrNoRow) {
			return err
		}
		if r := domain.DecideDevice(dc, actor.UserID, approve, a.Now()); r != nil {
			return r
		}
		status := domain.DeviceDenied
		if approve {
			status = domain.DeviceApproved
		}
		ok, err := tx.AuthDecideDeviceCode(dc.ID, status, actor.UserID)
		if err != nil {
			return err
		}
		if !ok {
			return &domain.DeviceRefusal{Status: 400, Code: "invalid_request", Description: domain.MsgDeviceProcessed}
		}
		return nil
	})
}
