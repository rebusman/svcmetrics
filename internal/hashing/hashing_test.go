package hashing

import (
	"strings"
	"testing"
)

func TestSumIsStableAndKeyed(t *testing.T) {
	body := []byte("body")

	if got := Sum(body, "key"); got != Sum(body, "key") {
		t.Error("Sum is not deterministic")
	}
	if Sum(body, "key") == Sum(body, "other key") {
		t.Error("Sum does not depend on the key")
	}
	if Sum(body, "key") == Sum([]byte("other body"), "key") {
		t.Error("Sum does not depend on the body")
	}
	if got := Sum(body, "key"); got != strings.ToLower(got) {
		t.Errorf("Sum = %q, want lowercase hexadecimal", got)
	}
}

func TestEqual(t *testing.T) {
	body := []byte("body")
	sig := Sum(body, "key")

	tests := []struct {
		name string
		sig  string
		body []byte
		key  string
		want bool
	}{
		{name: "match", sig: sig, body: body, key: "key", want: true},
		{name: "uppercase match", sig: strings.ToUpper(sig), body: body, key: "key", want: true},
		{name: "wrong key", sig: sig, body: body, key: "other key"},
		{name: "wrong body", sig: sig, body: []byte("other body"), key: "key"},
		{name: "malformed hex", sig: "not hex", body: body, key: "key"},
		{name: "odd length hex", sig: "abc", body: body, key: "key"},
		{name: "empty signature", sig: "", body: body, key: "key"},
		{name: "truncated digest", sig: sig[:len(sig)-2], body: body, key: "key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Equal(tt.sig, tt.body, tt.key); got != tt.want {
				t.Errorf("Equal = %v, want %v", got, tt.want)
			}
		})
	}
}
