//go:build darwin && cgo

package server

import (
	"context"
	"errors"
	keychain "github.com/keybase/go-keychain"
)

// Use the native Keychain API to retain raw Rust-era passwords, including
// Unicode and secrets up to 8192 bytes, without passing secrets as argv.
type KeychainStore struct{}

func keychainQuery(ref string) keychain.Item {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetService(credentialService)
	q.SetAccount(ref)
	return q
}
func (KeychainStore) Put(ctx context.Context, ref, secret string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	item := keychain.NewGenericPassword(credentialService, ref, "", []byte(secret), "")
	err := keychain.AddItem(item)
	if errors.Is(err, keychain.ErrorDuplicateItem) {
		update := keychain.NewItem()
		update.SetData([]byte(secret))
		err = keychain.UpdateItem(keychainQuery(ref), update)
	}
	if err != nil {
		return problem("credential_store_unavailable")
	}
	return nil
}
func (KeychainStore) Get(ctx context.Context, ref string) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	q := keychainQuery(ref)
	q.SetReturnData(true)
	q.SetMatchLimit(keychain.MatchLimitOne)
	items, err := keychain.QueryItem(q)
	if errors.Is(err, keychain.ErrorItemNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, problem("credential_store_unavailable")
	}
	if len(items) == 0 {
		return "", false, nil
	}
	return string(items[0].Data), true, nil
}
func (KeychainStore) Delete(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := keychain.DeleteItem(keychainQuery(ref))
	if err != nil && !errors.Is(err, keychain.ErrorItemNotFound) {
		return problem("credential_store_unavailable")
	}
	return nil
}
