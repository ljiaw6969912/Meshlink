package licensing

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

type TrustedKeyFile struct {
	Keys []TrustedKeyEntry `json:"keys"`
}

type TrustedKeyEntry struct {
	KeyID     string `json:"key_id"`
	PublicKey string `json:"public_key"`
}

func LoadTrustedKeysFile(path string) (TrustedKeys, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("trusted public key file is too large: %w", ErrInvalidDocument)
	}
	var config TrustedKeyFile
	if err := decodeStrict(raw, &config); err != nil {
		return nil, fmt.Errorf("trusted public key file is invalid: %w", err)
	}
	if len(config.Keys) == 0 {
		return nil, fmt.Errorf("trusted public key file is empty: %w", ErrInvalidDocument)
	}
	keys := make(TrustedKeys, len(config.Keys))
	for _, entry := range config.Keys {
		keyID := strings.TrimSpace(entry.KeyID)
		if !stableIDPattern.MatchString(keyID) || keyID != entry.KeyID {
			return nil, fmt.Errorf("trusted key id is invalid: %w", ErrInvalidDocument)
		}
		if _, exists := keys[keyID]; exists {
			return nil, fmt.Errorf("trusted key id %q is duplicated: %w", keyID, ErrInvalidDocument)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(entry.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize || entry.PublicKey != base64.RawURLEncoding.EncodeToString(decoded) {
			return nil, fmt.Errorf("trusted public key %q is invalid: %w", keyID, ErrInvalidDocument)
		}
		keys[keyID] = ed25519.PublicKey(decoded)
	}
	return keys, nil
}

func encodePublicKey(key ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(key)
}
