//go:build !darwin

package sysprobe

func DefaultKeychain() (string, string) {
	return Skipped, "the macOS Keychain is not available on this platform"
}
