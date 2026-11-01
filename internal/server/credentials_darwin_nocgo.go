//go:build darwin && !cgo

package server

import "context"

// Builds without cgo can still use the memory store. Keychain needs Apple's
// Security framework; fail explicitly instead of changing secret encodings.
type KeychainStore struct{}

func (KeychainStore) Put(context.Context, string, string) error {
	return problem("credential_store_unavailable")
}
func (KeychainStore) Get(context.Context, string) (string, bool, error) {
	return "", false, problem("credential_store_unavailable")
}
func (KeychainStore) Delete(context.Context, string) error {
	return problem("credential_store_unavailable")
}
