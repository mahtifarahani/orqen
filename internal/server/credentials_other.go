//go:build !darwin

package server

import (
	"context"
	"errors"
	"github.com/zalando/go-keyring"
)

type KeychainStore struct{}

func (KeychainStore) Put(_ context.Context, ref, secret string) error {
	if err := keyring.Set(credentialService, ref, secret); err != nil {
		return problem("credential_store_unavailable")
	}
	return nil
}
func (KeychainStore) Get(_ context.Context, ref string) (string, bool, error) {
	v, err := keyring.Get(credentialService, ref)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, problem("credential_store_unavailable")
	}
	return v, true, nil
}
func (KeychainStore) Delete(_ context.Context, ref string) error {
	err := keyring.Delete(credentialService, ref)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return problem("credential_store_unavailable")
	}
	return nil
}
