// Package grpccodec is the protobuf codec of the gRPC API with optional
// payload encryption, the counterpart of the RSA encryption of the HTTP API.
package grpccodec

import (
	"bytes"
	"crypto/rsa"
	"errors"
	"fmt"

	payloadcrypto "github.com/rebusman/svcmetrics/internal/crypto"
	"google.golang.org/grpc/mem"
	"google.golang.org/protobuf/proto"
)

// Name is the content subtype of the codec, the same as the stock protobuf one.
const Name = "proto"

// encryptedPrefix marks an encrypted message. It starts with a tag of the
// invalid wire type 7, so it can never be the start of a valid protobuf
// message and plain messages stay distinguishable.
var encryptedPrefix = []byte{0xff, 0x00, 'E', 'N', 'C'}

// ErrEncryptionUnsupported is returned for an encrypted message received
// without a private key.
var ErrEncryptionUnsupported = errors.New("unsupported request encryption")

// Codec marshals protobuf messages, encrypting them with the public key when
// one is set, and unmarshals both plain and encrypted messages, decrypting
// with the private key when one is set. The agent sets the public key, the
// server the private one.
type Codec struct {
	Public  *rsa.PublicKey
	Private *rsa.PrivateKey
}

// Name implements encoding.CodecV2.
func (Codec) Name() string { return Name }

// Marshal implements encoding.CodecV2.
func (c Codec) Marshal(v any) (mem.BufferSlice, error) {
	m, ok := v.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("grpccodec: %T is not a protobuf message", v)
	}
	data, err := Marshal(m)
	if err != nil {
		return nil, err
	}
	if c.Public != nil {
		encrypted, err := payloadcrypto.Encrypt(c.Public, data)
		if err != nil {
			return nil, err
		}
		data = append(bytes.Clone(encryptedPrefix), encrypted...)
	}
	return mem.BufferSlice{mem.SliceBuffer(data)}, nil
}

// Unmarshal implements encoding.CodecV2.
func (c Codec) Unmarshal(data mem.BufferSlice, v any) error {
	m, ok := v.(proto.Message)
	if !ok {
		return fmt.Errorf("grpccodec: %T is not a protobuf message", v)
	}
	buf := data.Materialize()
	if rest, found := bytes.CutPrefix(buf, encryptedPrefix); found {
		if c.Private == nil {
			return ErrEncryptionUnsupported
		}
		plain, err := payloadcrypto.Decrypt(c.Private, rest)
		if err != nil {
			return fmt.Errorf("invalid encrypted payload: %w", err)
		}
		buf = plain
	}
	return proto.Unmarshal(buf, m)
}

// Marshal is the deterministic encoding the message signatures are computed
// over: the same message always gives the same bytes.
func Marshal(m proto.Message) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(m)
}
