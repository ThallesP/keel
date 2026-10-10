package app

import (
	"context"
	"errors"
	"strings"

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
	if clientID != domain.DeviceClientID {
		return DeviceStart{}, &domain.DeviceRefusal{Status: 400, Code: "invalid_client", Description: domain.MsgDeviceInvalidClient}
	}
	if err := a.limited(a.limits().deviceStart, client.IP); err != nil {
		return DeviceStart{}, err
	}
	deviceCode := domain.NewSecret(25)
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
		return DeviceToken{}, &domain.DeviceRefusal{Status: 400, Code: "unsupported_grant_type", Description: "Unsupported grant type"}
	}
	if err := a.limited(a.limits().devicePoll, client.IP); err != nil {
		return DeviceToken{}, err
	}
	if clientID != domain.DeviceClientID {
		return DeviceToken{}, &domain.DeviceRefusal{Status: 400, Code: "invalid_grant", Description: domain.MsgDeviceInvalidClient}
	}
	invalidCode := &domain.DeviceRefusal{Status: 400, Code: "invalid_grant", Description: "Invalid device code"}
	var (
		action  domain.PollAction
		refusal *domain.DeviceRefusal
		token   string
	)
	err := a.write(ctx, func(tx Tx, _ *Changes) error {
		now := a.Now()
		dc, err := tx.AuthDeviceCodeByHash(domain.HashSecret(deviceCode))
		if errors.Is(err, ErrNoRow) {
			return invalidCode
		}
		if err != nil {
			return err
		}
		action, refusal = domain.DecidePoll(dc, now)
		switch action {
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
				return invalidCode
			}
			user, err := tx.AuthUser(dc.UserID)
			if err != nil {
				return err
			}
			s, err := issueSession(tx, user, now, client)
			token = s.Token
			return err
		}
		return nil
	})
	if err != nil {
		return DeviceToken{}, err
	}
	if refusal != nil {
		return DeviceToken{}, refusal
	}
	return DeviceToken{AccessToken: token, ExpiresIn: domain.SessionTTL / 1000}, nil
}

func (a *App) ClaimDeviceCode(ctx context.Context, actor domain.Actor, userCode string) (DeviceView, error) {
	var dc domain.DeviceCode
	look := func(tx Tx) (err error) {
		dc, err = liveDeviceCode(tx, userCode, a.Now())
		return err
	}
	if !actor.SignedIn() {
		err := a.read(ctx, look)
		return DeviceView{UserCode: userCode, Status: dc.Status}, err
	}
	err := a.write(ctx, func(tx Tx, _ *Changes) error {
		if err := look(tx); err != nil {
			return err
		}
		_, err := tx.AuthBindDeviceCode(dc.ID, actor.UserID)
		return err
	})
	return DeviceView{UserCode: userCode, Status: dc.Status}, err
}

func (a *App) DecideDeviceLogin(ctx context.Context, actor domain.Actor, userCode string, approve bool) error {
	if !actor.SignedIn() {
		return &domain.DeviceRefusal{Status: 401, Code: "unauthorized", Description: "Authentication required"}
	}
	return a.write(ctx, func(tx Tx, _ *Changes) error {
		dc, err := liveDeviceCode(tx, userCode, a.Now())
		if err != nil {
			return err
		}
		if r := domain.DecideDevice(dc, actor.UserID, approve); r != nil {
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

func liveDeviceCode(tx Tx, userCode string, now int64) (domain.DeviceCode, error) {
	dc, err := tx.AuthDeviceCodeByUserCode(strings.ReplaceAll(userCode, "-", ""))
	if errors.Is(err, ErrNoRow) {
		return domain.DeviceCode{}, &domain.DeviceRefusal{Status: 400, Code: "invalid_request", Description: "Invalid user code"}
	}
	if err != nil {
		return domain.DeviceCode{}, err
	}
	if dc.Expired(now) {
		return domain.DeviceCode{}, &domain.DeviceRefusal{Status: 400, Code: "expired_token", Description: "User code has expired"}
	}
	return dc, nil
}
