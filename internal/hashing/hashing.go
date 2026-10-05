// Package hashing authenticates HTTP message bodies with HMAC-SHA256. The agent
// and the server both depend on it, so the header name and the digest format
// cannot drift apart between the two sides of the protocol.
package hashing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// Header is the HTTP header that carries the hexadecimal HMAC-SHA256 digest of
// a message body, as transmitted — after compression, when the message is
// compressed.
const Header = "HashSHA256"

// NoSignature is the placeholder some clients put in [Header] instead of
// omitting it when they have no key. It means the same as an absent header.
const NoSignature = "none"

// Sum returns the lowercase hexadecimal HMAC-SHA256 digest of body under key.
func Sum(body []byte, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Equal reports whether sig is the digest of body under key. The comparison
// happens on the decoded digests, so the case of the hexadecimal digits does
// not matter and a malformed sig simply never matches.
func Equal(sig string, body []byte, key string) bool {
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
