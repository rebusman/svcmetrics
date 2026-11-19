// Package crypto encrypts agent request bodies with RSA-OAEP and AES-256-GCM.
//
// [Encrypt] generates a fresh AES key and nonce for each message and wraps the
// AES key with the server's public RSA key. [Decrypt] uses the corresponding
// private key to recover and authenticate the payload. Keys can be loaded from
// PEM files with [LoadPublicKey] and [LoadPrivateKey].
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// Header identifies encrypted request bodies; gzip describes the inner payload.
const Header = "X-Payload-Encryption"

// Algorithm is the value of [Header] for the format used by [Encrypt].
const Algorithm = "RSA-OAEP-AES-256-GCM"

// LoadPublicKey reads a PEM-encoded RSA public key from path.
// It accepts PKIX and PKCS#1 keys and returns an error if the file cannot be
// read, the key cannot be parsed, or the RSA modulus is smaller than 2048 bits.
func LoadPublicKey(path string) (*rsa.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read public key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("public key is not PEM encoded")
	}
	var key *rsa.PublicKey
	if block.Type == "RSA PUBLIC KEY" {
		key, err = x509.ParsePKCS1PublicKey(block.Bytes)
	} else {
		var parsed any
		parsed, err = x509.ParsePKIXPublicKey(block.Bytes)
		if err == nil {
			var ok bool
			key, ok = parsed.(*rsa.PublicKey)
			if !ok {
				err = errors.New("public key must be RSA")
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	if key.N.BitLen() < 2048 {
		return nil, errors.New("RSA key must be at least 2048 bits")
	}
	return key, nil
}

// LoadPrivateKey reads an unencrypted PEM-encoded RSA private key from path.
// It accepts PKCS#8 and PKCS#1 keys and returns an error if the file cannot be
// read or the key is malformed, invalid, or smaller than 2048 bits.
func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("private key is not PEM encoded")
	}
	var key *rsa.PrivateKey
	if block.Type == "RSA PRIVATE KEY" {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	} else {
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			key, ok = parsed.(*rsa.PrivateKey)
			if !ok {
				err = errors.New("private key must be RSA")
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	if key.N.BitLen() < 2048 {
		return nil, errors.New("RSA key must be at least 2048 bits")
	}
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("validate private key: %w", err)
	}
	return key, nil
}

// Encrypt seals data with AES-256-GCM using a fresh key and nonce, then wraps
// the AES key with RSA-OAEP/SHA-256 using key. The key must be non-nil.
// It returns an error if key wrapping or cipher initialization fails.
//
// The returned payload concatenates the following fields:
//   - Wrapped AES key, occupying key.Size() bytes.
//   - GCM nonce, occupying 12 bytes.
//   - GCM ciphertext, including its 16-byte authentication tag.
//
// [Algorithm] is authenticated as additional data. Use [Decrypt] with the
// corresponding private key to recover the plaintext.
func Encrypt(key *rsa.PublicKey, data []byte) ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key, secret, nil)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(secret)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	body := append(wrapped, nonce...)
	return gcm.Seal(body, nonce, data, []byte(Algorithm)), nil
}

// Decrypt authenticates and opens a payload produced by [Encrypt].
// The key must be non-nil and correspond to the public key used for encryption.
// It returns an error for truncated, damaged, or incompatible payloads, including
// messages encrypted with a different key.
func Decrypt(key *rsa.PrivateKey, data []byte) ([]byte, error) {
	size := key.Size()
	if len(data) < size {
		return nil, errors.New("truncated encrypted payload")
	}
	secret, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, data[:size], nil)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(secret)
	if err != nil {
		return nil, err
	}
	if len(data) < size+gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("truncated encrypted payload")
	}
	return gcm.Open(nil, data[size:size+gcm.NonceSize()], data[size+gcm.NonceSize():], []byte(Algorithm))
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
