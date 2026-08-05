package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"
)

// A virtual authenticator: enough of a security key to exercise the real thing.
//
// WebAuthn cannot be tested by mocking the library — the library IS the check. So
// this builds what a real authenticator sends: a COSE ES256 public key, an
// authenticator data blob with the RP id hash and flags, a client data JSON bound
// to the challenge and origin, and an ECDSA signature over the two. Everything the
// server rejects, it rejects because one of those does not add up, which is what
// makes these tests worth having: they fail for the same reasons a real attack
// would.
//
// It is deliberately dumb about state: the sign counter is a field the test moves
// by hand, because moving it backwards is exactly how a cloned key behaves.
type virtualAuthenticator struct {
	key       *ecdsa.PrivateKey
	id        []byte
	signCount uint32
	// userVerified controls the UV flag — a fingerprint or PIN, as opposed to mere
	// possession.
	userVerified bool
}

func newVirtualAuthenticator(t *testing.T) *virtualAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return &virtualAuthenticator{key: key, id: id, signCount: 1, userVerified: true}
}

// coseKey encodes the public key the way an authenticator does: a COSE_Key map,
// which is what the server stores and later verifies signatures against.
func (a *virtualAuthenticator) coseKey(t *testing.T) []byte {
	t.Helper()
	x := make([]byte, 32)
	y := make([]byte, 32)
	a.key.PublicKey.X.FillBytes(x)
	a.key.PublicKey.Y.FillBytes(y)
	// Keys are the COSE labels: 1=kty(2=EC2), 3=alg(-7=ES256), -1=crv(1=P-256).
	key := map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y}
	blob, err := cbor.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

// authData builds the authenticator data: the RP id hash, the flags, the counter,
// and (at registration) the attested credential itself.
func (a *virtualAuthenticator) authData(t *testing.T, rpID string, includeCredential bool) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte(rpID))
	out := append([]byte{}, sum[:]...)

	var flags byte = 0x01 // user present
	if a.userVerified {
		flags |= 0x04
	}
	if includeCredential {
		flags |= 0x40 // attested credential data included
	}
	out = append(out, flags)

	counter := make([]byte, 4)
	binary.BigEndian.PutUint32(counter, a.signCount)
	out = append(out, counter...)

	if includeCredential {
		out = append(out, make([]byte, 16)...) // AAGUID: all zeroes, as "none" attestation permits
		idLen := make([]byte, 2)
		binary.BigEndian.PutUint16(idLen, uint16(len(a.id)))
		out = append(out, idLen...)
		out = append(out, a.id...)
		out = append(out, a.coseKey(t)...)
	}
	return out
}

func clientDataJSON(t *testing.T, ceremony, challenge, origin string) []byte {
	t.Helper()
	blob, err := json.Marshal(map[string]any{
		"type": challengeType(ceremony), "challenge": challenge, "origin": origin, "crossOrigin": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	return blob
}

func challengeType(ceremony string) string {
	if ceremony == "create" {
		return "webauthn.create"
	}
	return "webauthn.get"
}

// register produces the JSON a browser posts after navigator.credentials.create.
func (a *virtualAuthenticator) register(t *testing.T, rpID, origin, challenge string) string {
	t.Helper()
	authData := a.authData(t, rpID, true)
	// "none" attestation: no attestation statement to sign, which is what a passkey
	// on a phone typically produces and all this server asks for.
	attestation, err := cbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": authData,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.id),
		"rawId": base64.RawURLEncoding.EncodeToString(a.id),
		"type":  "public-key",
		"response": map[string]any{
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestation),
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientDataJSON(t, "create", challenge, origin)),
		},
	}
	return mustJSON(t, body)
}

// assert produces the JSON a browser posts after navigator.credentials.get.
//
// The counter moves first, because a real authenticator increments it on every
// assertion — that movement is the whole clone-detection signal, and a harness
// that left it still would make an ordinary login look like a cloned key.
func (a *virtualAuthenticator) assert(t *testing.T, rpID, origin, challenge string) string {
	t.Helper()
	a.signCount++
	authData := a.authData(t, rpID, false)
	clientData := clientDataJSON(t, "get", challenge, origin)
	clientHash := sha256.Sum256(clientData)

	signed := append(append([]byte{}, authData...), clientHash[:]...)
	digest := sha256.Sum256(signed)
	r, s, err := ecdsa.Sign(rand.Reader, a.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"id":    base64.RawURLEncoding.EncodeToString(a.id),
		"rawId": base64.RawURLEncoding.EncodeToString(a.id),
		"type":  "public-key",
		"response": map[string]any{
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"signature":         base64.RawURLEncoding.EncodeToString(derSignature(t, r, s)),
			"userHandle":        "",
		},
	}
	return mustJSON(t, body)
}

// derSignature encodes (r,s) as the ASN.1 DER sequence WebAuthn expects.
func derSignature(t *testing.T, r, s *big.Int) []byte {
	t.Helper()
	encodeInt := func(n *big.Int) []byte {
		b := n.Bytes()
		if len(b) > 0 && b[0]&0x80 != 0 {
			b = append([]byte{0}, b...)
		}
		return append([]byte{0x02, byte(len(b))}, b...)
	}
	body := append(encodeInt(r), encodeInt(s)...)
	return append([]byte{0x30, byte(len(body))}, body...)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	blob, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(blob)
}

// ceremonyRequest wraps a credential JSON body as the handlers receive it.
func ceremonyRequest(body string) *http.Request {
	r, _ := http.NewRequest("POST", "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}
