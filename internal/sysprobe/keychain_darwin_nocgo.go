//go:build darwin && !cgo

package sysprobe

func DefaultKeychain() (string, string) {
	return Fail, "this build has no Keychain support (credential_store_unavailable)"
}
