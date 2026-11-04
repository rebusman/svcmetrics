package crypto

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{{"empty", nil}, {"large batch", bytes.Repeat([]byte("metric"), 10000)}} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := Encrypt(&key.PublicKey, tc.data)
			if err != nil {
				t.Fatal(err)
			}
			plain, err := Decrypt(key, body)
			if err != nil || !bytes.Equal(plain, tc.data) {
				t.Fatalf("round trip: %v", err)
			}
			if _, err := Decrypt(other, body); err == nil {
				t.Fatal("wrong key accepted")
			}
			body[len(body)-1] ^= 1
			if _, err := Decrypt(key, body); err == nil {
				t.Fatal("tampering accepted")
			}
			for _, n := range []int{0, 1, key.Size(), len(body) - 1} {
				if _, err := Decrypt(key, body[:n]); err == nil {
					t.Fatalf("truncation %d accepted", n)
				}
			}
		})
	}
}

func TestLoadKeys(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, kind string
		data       []byte
		private    bool
	}{
		{"PKIX", "PUBLIC KEY", public, false},
		{"PKCS1 public", "RSA PUBLIC KEY", x509.MarshalPKCS1PublicKey(&key.PublicKey), false},
		{"PKCS8", "PRIVATE KEY", private, true},
		{"PKCS1 private", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(key), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key.pem")
			if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: tc.kind, Bytes: tc.data}), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.private {
				loaded, err := LoadPrivateKey(path)
				if err != nil || !loaded.Equal(key) {
					t.Fatalf("load private: %v", err)
				}
			} else {
				loaded, err := LoadPublicKey(path)
				if err != nil || !loaded.Equal(&key.PublicKey) {
					t.Fatalf("load public: %v", err)
				}
			}
		})
	}
	path := filepath.Join(t.TempDir(), "invalid.pem")
	if _, err := LoadPublicKey(path); err == nil {
		t.Fatal("missing public key accepted")
	}
	if _, err := LoadPrivateKey(path); err == nil {
		t.Fatal("missing private key accepted")
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPublicKey(path); err == nil {
		t.Fatal("invalid public key accepted")
	}
	if _, err := LoadPrivateKey(path); err == nil {
		t.Fatal("invalid private key accepted")
	}
}
