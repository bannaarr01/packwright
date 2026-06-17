package keys

import "github.com/99designs/keyring"

// openKeyring opens the OS-native credential store under ServiceName.
//
// Backends are restricted to OS-native stores per ADR-0038: the
// FileBackend, which encrypts a JSON blob with a passphrase that we
// would have to prompt for, is intentionally excluded from production
// — its existence in 99designs/keyring is purely a convenience for
// platforms without a real credential store, and we ship the env-var
// fallback for that case instead.
//
// openKeyring is a package-level var so keys_test.go can swap in a
// file-backed Keyring under t.TempDir() without depending on the host's
// real OS keychain.
var openKeyring = func() (keyring.Keyring, error) {
	return keyring.Open(keyring.Config{
		ServiceName:              ServiceName,
		KeychainName:             ServiceName,
		KeychainTrustApplication: true,
		LibSecretCollectionName:  "packwright",
		KWalletAppID:             "packwright",
		KWalletFolder:            "Packwright",
		WinCredPrefix:            "Packwright:",
		AllowedBackends: []keyring.BackendType{
			keyring.KeychainBackend,
			keyring.SecretServiceBackend,
			keyring.KWalletBackend,
			keyring.WinCredBackend,
		},
	})
}
