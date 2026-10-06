package icloud

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"math/big"
	"testing"
)

// srpServer is the server side of RFC 5054 SRP-6a with Apple's conventions,
// written from the RFC rather than from the client: it holds the verifier
// v = g^x and answers with B = k*v + g^b.
type srpServer struct {
	salt []byte
	v, b *big.Int
	B    []byte
}

func newSRPServer(t *testing.T, derivedKey []byte) *srpServer {
	t.Helper()
	salt := make([]byte, 16)
	secret := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	inner := sha256.Sum256(append([]byte(":"), derivedKey...))
	x := new(big.Int).SetBytes(hash(salt, inner[:]))
	s := &srpServer{salt: salt, v: new(big.Int).Exp(srpG, x, srpN), b: new(big.Int).SetBytes(secret)}
	B := new(big.Int).Add(new(big.Int).Mul(srpK, s.v), new(big.Int).Exp(srpG, s.b, srpN))
	B.Mod(B, srpN)
	s.B = B.Bytes() // as Apple sends it: no padding
	return s
}

// key computes the server's session key from the client's A.
func (s *srpServer) key(clientA []byte) []byte {
	a := new(big.Int).SetBytes(clientA)
	u := new(big.Int).SetBytes(hash(pad(a), pad(new(big.Int).SetBytes(s.B))))
	// S = (A * v^u) ^ b mod N
	S := new(big.Int).Mul(a, new(big.Int).Exp(s.v, u, srpN))
	S.Exp(S.Mod(S, srpN), s.b, srpN)
	return hash(pad(S))
}

func TestSRPAgreesWithAnRFC5054Server(t *testing.T) {
	for _, protocol := range []string{protocolS2K, protocolS2KFO} {
		key, err := derivePassword("correct horse", []byte("salt"), 1000, protocol)
		if err != nil {
			t.Fatal(err)
		}
		server := newSRPServer(t, key)
		client, err := newSRPClient()
		if err != nil {
			t.Fatal(err)
		}
		if err := client.prove("Owner@Example.com", key, server.salt, server.B); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(client.K, server.key(client.publicValue())) {
			t.Fatalf("%s: client and server derived different session keys", protocol)
		}

		// A wrong password yields a different key, so the server rejects M1.
		wrong, _ := derivePassword("wrong", []byte("salt"), 1000, protocol)
		other := newSRPClientWithSecret(client.a)
		if err := other.prove("Owner@Example.com", wrong, server.salt, server.B); err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(other.K, server.key(other.publicValue())) {
			t.Fatalf("%s: a wrong password produced the server's session key", protocol)
		}
	}
}

func TestSRPPadsAAndHashesTheLowercaseAccountName(t *testing.T) {
	// a = 1 gives A = 2, a value with 255 leading zero bytes.
	c := newSRPClientWithSecret(big.NewInt(1))
	if got := c.publicValue(); len(got) != srpLen || got[srpLen-1] != 2 {
		t.Fatalf("A is not padded to %d bytes: %x", srpLen, got[:8])
	}
	key := bytes.Repeat([]byte{7}, derivedKeyLen)
	server := newSRPServer(t, key)
	mixed, lower := newSRPClientWithSecret(big.NewInt(42)), newSRPClientWithSecret(big.NewInt(42))
	if err := mixed.prove("Owner@iCloud.com", key, server.salt, server.B); err != nil {
		t.Fatal(err)
	}
	if err := lower.prove("owner@icloud.com", key, server.salt, server.B); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mixed.M1, lower.M1) {
		t.Fatal("M1 depends on the account name's case")
	}
	// The escrow proof uses an empty account name: same key, different M1.
	escrow := newSRPClientWithSecret(big.NewInt(42))
	if err := escrow.prove("", key, server.salt, server.B); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(escrow.M1, lower.M1) || !bytes.Equal(escrow.K, lower.K) {
		t.Fatal("the escrow proof must differ only in the account name")
	}
}

func TestSRPRejectsAnInvalidServerValue(t *testing.T) {
	c := newSRPClientWithSecret(big.NewInt(42))
	for _, B := range [][]byte{{0}, srpN.Bytes(), new(big.Int).Add(srpN, big.NewInt(1)).Bytes()} {
		if err := c.prove("a", []byte("k"), []byte("s"), B); err == nil {
			t.Fatalf("B = %x… accepted", B[:1])
		}
	}
}

func TestDerivePassword(t *testing.T) {
	s2k, err := derivePassword("p", []byte("salt"), 10, protocolS2K)
	if err != nil {
		t.Fatal(err)
	}
	fo, err := derivePassword("p", []byte("salt"), 10, protocolS2KFO)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2k) != derivedKeyLen || bytes.Equal(s2k, fo) {
		t.Fatalf("s2k and s2k_fo keys must differ and be %d bytes", derivedKeyLen)
	}
	if _, err := derivePassword("p", []byte("salt"), 10, "plain"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
}
