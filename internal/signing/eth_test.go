package signing

import (
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Hardhat/anvil account #0, a published key. The EIP-191 and legacy-domain
// EIP-712 vectors below are the ones nexus-exchange-rs pins against ethers
// v6, and nexus-exchange-py (eth-account) and nexus-exchange-ts pin in turn,
// so matching them means byte-identical signatures to three other
// implementations.
const (
	anvilKey  = "ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	anvilAddr = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"
)

func TestAddress(t *testing.T) {
	k, err := ParseKey("0x" + anvilKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := Address(k.PubKey()); got != anvilAddr {
		t.Fatalf("address = %s, want %s", got, anvilAddr)
	}
}

func TestParseKeyRejects(t *testing.T) {
	for _, bad := range []string{"", "zz", "0x1234", strings.Repeat("00", 32), strings.Repeat("ff", 32)} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) accepted", bad)
		} else if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("error echoes the input: %v", err)
		}
	}
}

func TestPersonalSignVector(t *testing.T) {
	k, _ := ParseKey(anvilKey)
	digest := PersonalHash("Sign in to Nexus Exchange")
	if got := hex.EncodeToString(digest); got != "99efa412eaa32f8d4ad2be2cad8835efc063776eff7834ddd3a8e34da9cd6268" {
		t.Fatalf("digest = %s", got)
	}
	sig := SignHash(k, digest)
	const want = "ff4ddf3b1af438fe00d02368ad8fa5fc5e57667e6826dbda3ddddc395a5287bb" +
		"6eab0bc97652f6e7e1f08f665b868ca143da79e18dae8021799cdafc4af670ea1b"
	if got := hex.EncodeToString(sig); got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
	if addr, err := Recover(digest, sig); err != nil || addr != anvilAddr {
		t.Fatalf("recover = %s, %v", addr, err)
	}
}

func TestRegisterAgentVectors(t *testing.T) {
	// Legacy domain (no salt), the other SDKs' pinned vector: digest and
	// signature both.
	k, _ := ParseKey(anvilKey)
	agent, _ := ParseAddress("0x1234567890abcdef1234567890abcdef12345678")
	d := RegisterAgentDigest(393, nil, agent, 1_782_000_000_000, 1)
	if got := hex.EncodeToString(d); got != "356e6f3d741f48279c78b228d4ed9217eb49ad9179d549c618215be57817bfd6" {
		t.Fatalf("legacy digest = %s", got)
	}
	const wantSig = "5df263ed6d1b619a72d436a01104f9036af6258cacf56dea973321cbe722a995" +
		"50644eea6bf75656d48e982d2ce5db9ef13c4aced4539cf3c2ff87802b0197cc1b"
	if got := hex.EncodeToString(SignHash(k, d)); got != wantSig {
		t.Fatalf("legacy signature = %s, want %s", got, wantSig)
	}

	// Salted domain, as the server verifies today: its own pinned digest
	// (agent_store::tests::eip712_register_agent_digest_pinned, alloy).
	agent, _ = ParseAddress("0xaaaaaaaaaaaaaaaaaaaabbbbbbbbbbbbbbbbbbbb")
	d = RegisterAgentDigest(20056, NetworkSalt("testnet"), agent, 1_700_000_000, 1)
	if got := hex.EncodeToString(d); got != "5a52159bdde9c9ba6c1880598078c3326e8e32ea39c93425baafc76590d2a902" {
		t.Fatalf("salted digest = %s", got)
	}
}

// The salts published in the spec's x-nexus-networks signing_domain.
func TestNetworkSalt(t *testing.T) {
	for network, want := range map[string]string{
		"testnet": "d992b760ba3914309086be769796784454b6684e49ebfe3005bb9455433b7c8e",
		"mainnet": "7beafa94c8bfb8f1c1a43104a34f72c524268aafbfe83bff17485539345c66ff",
		"local":   "98591f89798185a27bc859ebabeeae88a1ed96bfbdf2f01b32ac97474b024894",
	} {
		if got := hex.EncodeToString(NetworkSalt(network)); got != want {
			t.Errorf("%s salt = %s, want %s", network, got, want)
		}
	}
}

// The agentAuth scheme's x-nexus-test-vectors, verbatim from the spec. They
// cover both values of v.
func TestAgentVectors(t *testing.T) {
	for _, tc := range []struct {
		key, method, path, query, body, agent, canonical, sig string
		ts                                                    int64
		nonce                                                 uint64
	}{
		{
			key: "0x0101010101010101010101010101010101010101010101010101010101010101", method: "GET", path: "/account/summary",
			ts: 1776033900000, nonce: 1776033900000, agent: "0x1a642f0e3c3af545e7acbd38b07251b3990914f1",
			canonical: "GET\n/account/summary\n\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n1776033900000\n1776033900000",
			sig:       "0xd94b40dff9c3d0e0a649178b6eb3159d9a3522a1ccc66390b42a7a444b8e52c85f2d2a9d66e8a48ac5040d8e96f526d352b3b7a686634280b9192eb6c12a61b61b",
		},
		{
			key: "0x4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318", method: "post", path: "/orders",
			body: `{"market_id":"BTC-USDX-PERP","side":"Buy","order_type":"Limit","quantity":"0.1","price":"50000"}`,
			ts:   1776033900123, nonce: 42, agent: "0x2c7536e3605d9c16a7a3d7b1898e529396a65c23",
			canonical: "POST\n/orders\n\n59ddf9017ca0eb5f8802afd1a2d1584b2d93b0260a0fcd62a1b94b3ff92b332c\n1776033900123\n42",
			sig:       "0xeef09105834062da10d208be38311995d04cbc5e05043b89e7cbe6d4497d11b51f61729639551a8cfb6ea3e4fe0765afeba7bf3b5f5e09f63bc095c89005b61d1c",
		},
		{
			key: "0x4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318", method: "DELETE", path: "/orders",
			query: "market_id=BTC-USDX-PERP", ts: 1776033900456, nonce: 43, agent: "0x2c7536e3605d9c16a7a3d7b1898e529396a65c23",
			canonical: "DELETE\n/orders\nmarket_id=BTC-USDX-PERP\ne3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n1776033900456\n43",
			sig:       "0x002554d4216daa303818dfcc5a15eb86940691e11ad6ab25a270916ec5a43a1a5eb4c18dd898aa3ebd099c1683337e3c545417c2b67a0d384f1d0355ffa807f91c",
		},
	} {
		k, _ := ParseKey(tc.key)
		a := NewAgent(k)
		if a.Address() != tc.agent {
			t.Errorf("agent = %s, want %s", a.Address(), tc.agent)
		}
		c := AgentCanonical(tc.method, tc.path, tc.query, []byte(tc.body), tc.ts, tc.nonce)
		if c != tc.canonical {
			t.Errorf("canonical = %q, want %q", c, tc.canonical)
		}
		if got := SignAgent(k, c); got != tc.sig {
			t.Errorf("%s %s signature = %s, want %s", tc.method, tc.path, got, tc.sig)
		}
	}
}

func TestAgentSignNonceAndRefusal(t *testing.T) {
	k, _ := ParseKey(anvilKey)
	a := NewAgent(k)
	a.Now = func() time.Time { return time.UnixMilli(1000) }
	var nonces []string
	for range 3 {
		h := http.Header{}
		if err := a.Sign(t.Context(), h, "POST", "/orders", "", nil); err != nil {
			t.Fatal(err)
		}
		nonces = append(nonces, h.Get("X-Nonce"))
	}
	// A frozen clock still yields strictly increasing nonces.
	if strings.Join(nonces, ",") != "1000,1001,1002" {
		t.Fatalf("nonces = %v", nonces)
	}
	for _, r := range []struct {
		method, path string
		refused      bool
	}{
		{"POST", "/withdrawals", true},
		{"POST", "/api/v1/bridge/withdrawals", true},
		{"POST", "/transfers", true},
		{"GET", "/transfers/abc", true},
		{"GET", "/withdrawals", false},
		{"GET", "/api/v1/bridge/withdrawals/1", false},
		{"POST", "/orders", false},
	} {
		if got := MovesFunds(r.method, r.path); got != r.refused {
			t.Errorf("MovesFunds(%s %s) = %v, want %v", r.method, r.path, got, r.refused)
		}
	}
}
