package nexus

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/nexus-xyz/nexus-exchange-go/internal/signing"
)

// PrivateKey is a secp256k1 key: an owner wallet, or an agent key a wallet
// delegates trading to. Build one with [NewPrivateKey] or
// [GeneratePrivateKey]. What a key may do depends on how it is used: as
// [WithWallet] it is the account owner, as an [Agent] it trades and nothing
// more (R2.18).
//
// Like [APISecret] it cannot be printed: every fmt verb and JSON encoding
// shows only the public address.
type PrivateKey struct {
	// A func for the same reason as APISecret.key: fmt cannot reflect into it.
	key  func() *ecdsa.PrivateKey
	addr string
}

// NewPrivateKey decodes privateKeyHex, 32 bytes of hex with or without 0x.
// The error never echoes the input.
func NewPrivateKey(privateKeyHex string) (*PrivateKey, error) {
	k, err := signing.ParseKey(privateKeyHex)
	if err != nil {
		return nil, err
	}
	return newPrivateKey(k), nil
}

// GeneratePrivateKey returns a new random key from crypto/rand, for a fresh
// agent. The SDK keeps it only in memory: save [PrivateKey.Hex] somewhere
// safe if the agent must outlive the process.
func GeneratePrivateKey() (*PrivateKey, error) {
	k, err := crypto.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("nexus: generate key: %w", err)
	}
	return newPrivateKey(k), nil
}

func newPrivateKey(k *ecdsa.PrivateKey) *PrivateKey {
	return &PrivateKey{key: func() *ecdsa.PrivateKey { return k }, addr: signing.Address(&k.PublicKey)}
}

// Address is the key's Ethereum address, lower-case hex with 0x.
func (k *PrivateKey) Address() string { return k.addr }

// Hex returns the private key as 0x-prefixed hex, the form [NewPrivateKey]
// reads back. It is the one way to get the secret out; call it only to store
// the key, never to log it.
func (k *PrivateKey) Hex() string { return "0x" + hex.EncodeToString(crypto.FromECDSA(k.key())) }

func (k *PrivateKey) String() string   { return "nexus.PrivateKey(" + k.addr + ")" }
func (k *PrivateKey) GoString() string { return k.String() }

// Format shows only the address, under every verb.
func (k *PrivateKey) Format(f fmt.State, _ rune) { fmt.Fprint(f, k.String()) }

// MarshalJSON emits the address, never the key.
func (k *PrivateKey) MarshalJSON() ([]byte, error) { return []byte(`"` + k.String() + `"`), nil }
