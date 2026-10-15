package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"

	"github.com/Purestreams/xmesh/internal/auth"
	"github.com/Purestreams/xmesh/internal/model"
)

// ProtectSubscriptions migrates legacy tokens without rotating existing URLs.
// Runtime snapshots retain tokens for the authenticated management UI; disk
// stores a lookup hash and an authenticated ciphertext bound to the user ID.
func (s *Store) ProtectSubscriptions(secret []byte) error {
	if len(secret) < 32 {
		return errors.New("subscription encryption requires a 32-byte session secret")
	}
	key := sha256.Sum256(append([]byte("xmesh subscription tokens v1\x00"), secret...))
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.state.Clone()
	if len(s.subscriptionKey) > 0 && !bytes.Equal(s.subscriptionKey, key[:]) {
		for id, user := range next.Users {
			user.SubscriptionTokenCipher = ""
			next.Users[id] = user
		}
	}
	persisted, err := protectSubscriptionTokens(&next, key[:])
	if err != nil {
		return err
	}
	// Preserve the Store's no-file behavior for a fresh, empty installation.
	if len(next.Users) > 0 {
		if err := writeAtomic(s.path, persisted); err != nil {
			return err
		}
	}
	s.subscriptionKey, s.state = key[:], next
	return nil
}

func protectSubscriptionTokens(state *model.State, key []byte) (model.State, error) {
	persisted := *state
	persisted.Users = maps.Clone(state.Users)
	if len(key) == 0 {
		// Encrypted state cannot be updated without its encryption key.
		for _, user := range state.Users {
			if user.SubscriptionTokenCipher != "" {
				return model.State{}, errors.New("configure subscription encryption before updating protected state")
			}
		}
		return persisted, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return model.State{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return model.State{}, err
	}
	for id, user := range state.Users {
		if user.SubscriptionToken == "" && user.SubscriptionTokenCipher != "" {
			data, err := base64.RawURLEncoding.DecodeString(user.SubscriptionTokenCipher)
			if err != nil || len(data) < aead.NonceSize() {
				return model.State{}, fmt.Errorf("invalid subscription ciphertext for user %s", id)
			}
			plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(id))
			if err != nil {
				return model.State{}, fmt.Errorf("decrypt subscription for user %s: %w", id, err)
			}
			user.SubscriptionToken = string(plain)
			if !auth.EqualSecretHash(user.SubscriptionTokenHash, user.SubscriptionToken) {
				return model.State{}, fmt.Errorf("subscription hash mismatch for user %s", id)
			}
		}
		if user.SubscriptionToken != "" && (user.SubscriptionTokenCipher == "" || !auth.EqualSecretHash(user.SubscriptionTokenHash, user.SubscriptionToken)) {
			nonce := make([]byte, aead.NonceSize())
			if _, err := rand.Read(nonce); err != nil {
				return model.State{}, fmt.Errorf("generate subscription nonce: %w", err)
			}
			user.SubscriptionTokenCipher = base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(user.SubscriptionToken), []byte(id)))
			user.SubscriptionTokenHash = auth.SecretHash(user.SubscriptionToken)
		}
		state.Users[id] = user
		user.SubscriptionToken = ""
		persisted.Users[id] = user
	}
	return persisted, nil
}
