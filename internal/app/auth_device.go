package app

import (
	"context"
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

type DeviceStart struct {
	DeviceCode              string
	UserCode                string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresIn               int
	Interval                int
}

type DeviceToken struct {
	AccessToken string
	ExpiresIn   int64
}

type DeviceView struct {
	UserCode string
	Status   domain.DeviceStatus
}

func (a *App) StartDeviceLogin(ctx context.Context, clientID string, client ClientInfo) (DeviceStart, error) {
	if r := domain.CheckDeviceClient(clientID); r != nil {
		return DeviceStart{}, r
	}
	if err := a.limited(a.limits().deviceStart, client.IP); err != nil {
		return DeviceStart{}, err
	}
	deviceCode := domain.NewDeviceCode()
	var userCode string
	err := a.write(ctx, func(tx Tx, _ *Changes) error {
		now := a.Now()
		if err := tx.AuthDeleteExpiredDeviceCodes(now - domain.DeviceCodeKeep); err != nil {
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

func (a *App) PollDeviceLogin(ctx context.Context, grantType, deviceCode, clientID string, client ClientInfo) (DeviceToken, error) {
	if grantType != domain.DeviceGrantType {
		return DeviceToken{}, &domain.DeviceRefusal{Status: 400, Code: "unsupported_grant_type", Description: domain.MsgDeviceGrantType}
	}
	if err := a.limited(a.limits().devicePoll, client.IP); err != nil {
		return DeviceToken{}, err
	}
	var (
		out     DeviceToken
		refusal *domain.DeviceRefusal
	)
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

func (a *App) DecideDeviceLogin(ctx context.Context, actor domain.Actor, userCode string, approve bool) error {
	if !actor.SignedIn() {
		return domain.DecideDevice(nil, "", approve, 0)
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
