package signing

import (
	"crypto/ecdsa"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

// Keccak256 is the original Keccak-256 Ethereum hashes with, which is not
// the standardised SHA3-256 (the padding differs).
func Keccak256(parts ...[]byte) []byte { return crypto.Keccak256(parts...) }

// ParseKey decodes a 32-byte hex secp256k1 private key. A leading "0x" is
// accepted. A value outside [1, n-1] is refused rather than reduced, so a
// mistyped key can never silently become a different one.
func ParseKey(s string) (*ecdsa.PrivateKey, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 32 {
		// The input is not echoed: it may be a real key, mistyped.
		return nil, errors.New("nexus: private key must be 32 bytes of hex")
	}
	k, err := crypto.ToECDSA(b)
	if err != nil {
		return nil, errors.New("nexus: private key is not a valid secp256k1 key")
	}
	return k, nil
}

// Address is the Ethereum address of pub, lower-case hex with 0x.
func Address(pub *ecdsa.PublicKey) string {
	return strings.ToLower(crypto.PubkeyToAddress(*pub).Hex())
}

// SignHash signs a 32-byte digest and returns the 65 bytes r ‖ s ‖ v with v
// in {27, 28}. The nonce is RFC 6979 and s is low (EIP-2), so for a given key
// and digest the result is byte-identical to eth-account, ethers and viem.
func SignHash(key *ecdsa.PrivateKey, digest []byte) []byte {
	sig, err := crypto.Sign(digest, key)
	if err != nil {
		// Only a digest that is not 32 bytes fails, and every caller passes a
		// Keccak-256 output.
		panic("nexus: sign: " + err.Error())
	}
	sig[64] += 27
	return sig
}

// Recover returns the address that produced sig (r ‖ s ‖ v, v in {27, 28})
// over digest. The SDK signs and never verifies; this exists so tests can
// check signatures the way the server does.
func Recover(digest, sig []byte) (string, error) {
	if len(sig) != 65 || (sig[64] != 27 && sig[64] != 28) {
		return "", errors.New("nexus: signature must be 65 bytes r || s || v with v in {27, 28}")
	}
	s := append([]byte{}, sig...)
	s[64] -= 27
	pub, err := crypto.SigToPub(digest, s)
	if err != nil {
		return "", err
	}
	return Address(pub), nil
}

// PersonalHash is the EIP-191 personal_sign digest of msg:
// keccak256("\x19Ethereum Signed Message:\n" + len(msg) + msg).
func PersonalHash(msg string) []byte { return accounts.TextHash([]byte(msg)) }

// ParseAddress decodes a 0x-prefixed 20-byte hex address.
func ParseAddress(s string) ([20]byte, error) {
	var a [20]byte
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if !strings.HasPrefix(s, "0x") || err != nil || len(b) != 20 {
		return a, errors.New("nexus: address must be 0x followed by 40 hex digits")
	}
	copy(a[:], b)
	return a, nil
}

// NetworkSalt is the RegisterAgent domain salt for a network: keccak256 of
// its lower-case wire name ("testnet", "mainnet", "local"), as the server
// computes it (ENG-11924).
func NetworkSalt(network string) []byte { return Keccak256([]byte(network)) }

// RegisterAgentDigest is the EIP-712 digest of RegisterAgent{agent,
// expiresAt, nonce} under the domain {name: "Nexus Exchange", version: "1",
// chainId, salt}. A nil salt leaves the field out of the domain, which is the
// shape the server used before ENG-11924 and the one the other SDKs' pinned
// signatures were made under.
func RegisterAgentDigest(chainID uint64, salt []byte, agent [20]byte, expiresAt, nonce uint64) ([]byte, error) {
	domainType := []apitypes.Type{
		{Name: "name", Type: "string"},
		{Name: "version", Type: "string"},
		{Name: "chainId", Type: "uint256"},
	}
	domain := apitypes.TypedDataDomain{
		Name:    "Nexus Exchange",
		Version: "1",
		ChainId: math.NewHexOrDecimal256(int64(chainID)),
	}
	if salt != nil {
		domainType = append(domainType, apitypes.Type{Name: "salt", Type: "bytes32"})
		domain.Salt = hexutil.Encode(salt)
	}
	d, _, err := apitypes.TypedDataAndHash(apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": domainType,
			"RegisterAgent": {
				{Name: "agent", Type: "address"},
				{Name: "expiresAt", Type: "uint64"},
				{Name: "nonce", Type: "uint64"},
			},
		},
		PrimaryType: "RegisterAgent",
		Domain:      domain,
		Message: apitypes.TypedDataMessage{
			"agent":     common.Address(agent).Hex(),
			"expiresAt": strconv.FormatUint(expiresAt, 10),
			"nonce":     strconv.FormatUint(nonce, 10),
		},
	})
	return d, err
}
