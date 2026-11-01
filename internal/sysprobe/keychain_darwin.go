//go:build darwin && cgo

package sysprobe

import (
	"errors"

	keychain "github.com/keybase/go-keychain"
)

// DefaultKeychain queries an item that does not exist; a readable Keychain answers "not found" without a prompt.
func DefaultKeychain() (string, string) {
	q := keychain.NewItem()
	q.SetSecClass(keychain.SecClassGenericPassword)
	q.SetService("dev.orqen.credential")
	q.SetAccount("cred_PREFLIGHT_PROBE_ABSENT")
	q.SetReturnData(true)
	q.SetMatchLimit(keychain.MatchLimitOne)
	_, err := keychain.QueryItem(q)
	if err == nil || errors.Is(err, keychain.ErrorItemNotFound) {
		return Pass, "the macOS Keychain is readable"
	}
	return Fail, "the macOS Keychain could not be queried: " + err.Error()
}
