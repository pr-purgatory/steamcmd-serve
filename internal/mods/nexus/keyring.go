package nexus

import (
	"context"
	"encoding/json"
	"errors"

	"steamcmd-serve/internal/secrets"
	"steamcmd-serve/internal/store"
)

const (
	settingKey     = "nexus.api_key"
	settingAccount = "nexus.account"
)

// Settings is the part of *store.Store the Keyring uses.
type Settings interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
	DeleteSetting(ctx context.Context, key string) error
}

var _ Settings = (*store.Store)(nil)

// Keyring stores the NexusMods API key sealed in app_settings.
type Keyring struct {
	store  Settings
	box    *secrets.Box
	client *Client
}

// NewKeyring returns a Keyring. client may be nil (production defaults).
func NewKeyring(st Settings, box *secrets.Box, client *Client) *Keyring {
	if client == nil {
		client = &Client{}
	}
	return &Keyring{store: st, box: box, client: client}
}

// Set validates key with Nexus first and stores it only if accepted.
func (k *Keyring) Set(ctx context.Context, key string) (Account, error) {
	acct, err := k.client.Validate(ctx, key)
	if err != nil {
		return Account{}, err
	}
	if err := k.store.SetSetting(ctx, settingKey, k.box.Seal(key)); err != nil {
		return Account{}, err
	}
	k.cache(ctx, acct)
	return acct, nil
}

// Key returns the stored key (ErrNoKey when unset).
func (k *Keyring) Key(ctx context.Context) (string, error) {
	sealed, err := k.store.GetSetting(ctx, settingKey)
	if errors.Is(err, store.ErrNotFound) {
		return "", ErrNoKey
	}
	if err != nil {
		return "", err
	}
	key, err := k.box.Open(sealed)
	if err != nil {
		return "", err
	}
	if key == "" {
		return "", ErrNoKey
	}
	return key, nil
}

// Status reports whether a key is stored and the account tier. The stored key is
// re-validated; if Nexus cannot be reached (or rejects the stored key) the cached
// account from the last successful validation is returned.
func (k *Keyring) Status(ctx context.Context) (Account, error) {
	key, err := k.Key(ctx)
	if errors.Is(err, ErrNoKey) {
		return Account{}, nil
	}
	if err != nil {
		return Account{}, err
	}
	if acct, err := k.client.Validate(ctx, key); err == nil {
		k.cache(ctx, acct)
		return acct, nil
	}
	acct := Account{HasValue: true}
	if raw, err := k.store.GetSetting(ctx, settingAccount); err == nil {
		var c Account
		if json.Unmarshal([]byte(raw), &c) == nil {
			acct.Premium, acct.Username = c.Premium, c.Username
		}
	}
	return acct, nil
}

// Delete removes the key and the cached account.
func (k *Keyring) Delete(ctx context.Context) error {
	if err := k.store.DeleteSetting(ctx, settingAccount); err != nil {
		return err
	}
	return k.store.DeleteSetting(ctx, settingKey)
}

// cache is best effort: failing to write it must not fail the request.
func (k *Keyring) cache(ctx context.Context, a Account) {
	b, err := json.Marshal(Account{HasValue: true, Premium: a.Premium, Username: a.Username})
	if err == nil {
		_ = k.store.SetSetting(ctx, settingAccount, string(b))
	}
}
