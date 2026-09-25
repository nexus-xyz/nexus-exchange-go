package signing

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

// Keccak256 is the original Keccak-256 Ethereum hashes with, which is not
// the standardised SHA3-256 (the padding differs).
func Keccak256(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// ParseKey decodes a 32-byte hex secp256k1 private key. A leading "0x" is
// accepted. A value outside [1, n-1] is refused rather than reduced, so a
// mistyped key can never silently become a different one.
func ParseKey(s string) (*secp256k1.PrivateKey, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 32 {
		// The input is not echoed: it may be a real key, mistyped.
		return nil, errors.New("nexus: private key must be 32 bytes of hex")
	}
	var n secp256k1.ModNScalar
	if overflow := n.SetByteSlice(b); overflow || n.IsZero() {
		return nil, errors.New("nexus: private key is not a valid secp256k1 key")
	}
	return secp256k1.NewPrivateKey(&n), nil
}

// Address is the Ethereum address of pub, lower-case hex with 0x:
// the last 20 bytes of keccak256 of the uncompressed point without its 0x04
// prefix.
func Address(pub *secp256k1.PublicKey) string {
	return "0x" + hex.EncodeToString(Keccak256(pub.SerializeUncompressed()[1:])[12:])
}

// SignHash signs a 32-byte digest and returns the 65 bytes r ‖ s ‖ v with v
// in {27, 28}. The nonce is RFC 6979 and s is low (EIP-2), so for a given key
// and digest the result is byte-identical to eth-account, ethers and viem.
func SignHash(key *secp256k1.PrivateKey, digest []byte) []byte {
	c := ecdsa.SignCompact(key, digest, false) // v ‖ r ‖ s, v = 27 + recovery id
	return append(c[1:], c[0])
}

// Recover returns the address that produced sig (r ‖ s ‖ v, v in {27, 28})
// over digest. The SDK signs and never verifies; this exists so tests can
// check signatures the way the server does.
func Recover(digest, sig []byte) (string, error) {
	if len(sig) != 65 || (sig[64] != 27 && sig[64] != 28) {
		return "", errors.New("nexus: signature must be 65 bytes r || s || v with v in {27, 28}")
	}
	pub, _, err := ecdsa.RecoverCompact(append([]byte{sig[64]}, sig[:64]...), digest)
	if err != nil {
		return "", err
	}
	return Address(pub), nil
}

// PersonalHash is the EIP-191 personal_sign digest of msg:
// keccak256("\x19Ethereum Signed Message:\n" + len(msg) + msg).
func PersonalHash(msg string) []byte {
	return Keccak256([]byte("\x19Ethereum Signed Message:\n" + strconv.Itoa(len(msg)) + msg))
}

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

// EIP-712 for the one fixed struct the SDK signs. Hand-encoded rather than
// through a general typed-data encoder: every field is a static type, so each
// encodes to one 32-byte word, and the byte layout is pinned by tests against
// the server's own digest (docs/adr/0002-wallet-signing.md).
var (
	domainTypeHash         = Keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId)"))
	domainWithSaltTypeHash = Keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId,bytes32 salt)"))
	registerAgentTypeHash  = Keccak256([]byte("RegisterAgent(address agent,uint64 expiresAt,uint64 nonce)"))
	domainName             = Keccak256([]byte("Nexus Exchange"))
	domainVersion          = Keccak256([]byte("1"))
)

// NetworkSalt is the RegisterAgent domain salt for a network: keccak256 of
// its lower-case wire name ("testnet", "mainnet", "local"), as the server
// computes it (ENG-11924).
func NetworkSalt(network string) []byte { return Keccak256([]byte(network)) }

// RegisterAgentDigest is the EIP-712 digest of RegisterAgent{agent,
// expiresAt, nonce} under the domain {name: "Nexus Exchange", version: "1",
// chainId, salt}. A nil salt leaves the field out of the domain, which is the
// shape the server used before ENG-11924 and the one the other SDKs' pinned
// signatures were made under.
func RegisterAgentDigest(chainID uint64, salt []byte, agent [20]byte, expiresAt, nonce uint64) []byte {
	var domain []byte
	if salt == nil {
		domain = Keccak256(domainTypeHash, domainName, domainVersion, word(chainID))
	} else {
		domain = Keccak256(domainWithSaltTypeHash, domainName, domainVersion, word(chainID), salt)
	}
	var addr [32]byte
	copy(addr[12:], agent[:])
	msg := Keccak256(registerAgentTypeHash, addr[:], word(expiresAt), word(nonce))
	return Keccak256([]byte{0x19, 0x01}, domain, msg)
}

// word ABI-encodes an unsigned integer as one big-endian 32-byte word.
func word(v uint64) []byte {
	var w [32]byte
	binary.BigEndian.PutUint64(w[24:], v)
	return w[:]
}
