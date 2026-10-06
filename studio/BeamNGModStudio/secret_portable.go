//go:build !windows

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func protectSecret(value, dataDir string) (string, error) {
	if value == "" {
		return "", nil
	}
	key, err := loadSecretKey(dataDir, true)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func unprotectSecret(value, dataDir string) (string, error) {
	if value == "" {
		return "", nil
	}
	key, err := loadSecretKey(dataDir, false)
	if err != nil {
		return "", err
	}
	sealed, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("encrypted secret is truncated")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func loadSecretKey(dataDir string, create bool) ([]byte, error) {
	filename := filepath.Join(dataDir, ".credential-key")
	key, err := os.ReadFile(filename)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("credential key has an invalid length")
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filename, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}
