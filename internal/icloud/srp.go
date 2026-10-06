package icloud

// SRP-6a as Apple's sign-in service (GSA) speaks it, mirroring the iCloud web
// client: the 2048-bit group of RFC 5054 with SHA-256, a password key derived
// with PBKDF2 ("s2k"/"s2k_fo"), no account name in x, the account name
// lowercased in M1, A and S padded to the group size and B used as received.
//
// Adapted from rclone's backend/iclouddrive/api/srp.go (MIT License,
// Copyright (C) 2012 by Nick Craig-Wood http://www.craig-wood.com/nick/),
// cross-checked against icloud-photos-sync's implementation of Apple's
// webSRPClientWorker.js.

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// srpN is the RFC 5054 Appendix A 2048-bit prime; the generator is 2.
var (
	srpN = mustHex("AC6BDB41324A9A9BF166DE5E1389582FAF72B6651987EE07FC3192943DB56050" +
		"A37329CBB4A099ED8193E0757767A13DD52312AB4B03310DCD7F48A9DA04FD50" +
		"E8083969EDB767B0CF6095179A163AB3661A05FBD5FAAAE82918A9962F0B93B8" +
		"55F97993EC975EEAA80D740ADBF4FF747359D041D5C33EA71D281E446B14773B" +
		"CA97B43A23FB801676BD207A436C6481F1D2B9078717461A5B9D32E688F87748" +
		"544523B524B0D57D5EA77A2775D2ECFA032CFBDBF52FB37861602790" +
		"04E57AE6AF874E7303CE53299CCC041C7BC308D82A5698F3A8D0C38271AE35F8" +
		"E9DBFBB694B5C803D89F7AE435DE236D525F54759B65E372FCD68EF20FA7111F" +
		"9E4AFF73")
	srpG = big.NewInt(2)
	// srpK is the SRP-6a multiplier H(N | pad(g)).
	srpK = new(big.Int).SetBytes(hash(srpN.Bytes(), pad(srpG)))
)

const (
	// srpLen is the byte length of N; A, g and S are padded to it.
	srpLen = 2048 / 8
	// secretLen is the length of the client's private value a.
	secretLen = 32
	// derivedKeyLen is the PBKDF2 output length Apple uses.
	derivedKeyLen = 32
)

// The password protocols Apple's sign-in may ask for.
const (
	protocolS2K   = "s2k"
	protocolS2KFO = "s2k_fo"
)

var errBadServerValue = errors.New("srp: invalid server public value")

func mustHex(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("srp: invalid group constant")
	}
	return n
}

func hash(parts ...[]byte) []byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// pad returns n big-endian, left-padded with zeros to the length of N.
func pad(n *big.Int) []byte {
	return n.FillBytes(make([]byte, srpLen))
}

// srpClient is one SRP exchange: its private value and, once the server's
// challenge was processed, the proofs and the shared session key.
type srpClient struct {
	a, A   *big.Int
	M1, M2 []byte
	// K is the shared session key; the escrow step sends it back to Apple.
	K []byte
}

func newSRPClient() (*srpClient, error) {
	secret := make([]byte, secretLen)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("srp: random secret: %w", err)
	}
	return newSRPClientWithSecret(new(big.Int).SetBytes(secret)), nil
}

func newSRPClientWithSecret(a *big.Int) *srpClient {
	return &srpClient{a: a, A: new(big.Int).Exp(srpG, a, srpN)}
}

// publicValue is A as sent to the server.
func (c *srpClient) publicValue() []byte { return pad(c.A) }

// prove computes the session key and the proofs from the server's salt and
// public value B, for accountName (empty in the escrow step) and the password
// key from derivePassword.
func (c *srpClient) prove(accountName string, derivedKey, salt, serverB []byte) error {
	B := new(big.Int).SetBytes(serverB)
	if B.Sign() <= 0 || B.Cmp(srpN) >= 0 {
		return errBadServerValue
	}
	A := pad(c.A)
	u := new(big.Int).SetBytes(hash(A, pad(B)))
	if u.Sign() == 0 {
		return errBadServerValue
	}
	x := new(big.Int).SetBytes(hash(salt, hash([]byte(":"), derivedKey)))

	// S = (B - k*g^x) ^ (a + u*x) mod N
	base := new(big.Int).Sub(B, new(big.Int).Mul(srpK, new(big.Int).Exp(srpG, x, srpN)))
	base.Mod(base, srpN)
	exp := new(big.Int).Add(c.a, new(big.Int).Mul(u, x))
	S := new(big.Int).Exp(base, exp, srpN)
	c.K = hash(pad(S))

	hN, hG := hash(srpN.Bytes()), hash(pad(srpG))
	xor := make([]byte, len(hN))
	for i := range hN {
		xor[i] = hN[i] ^ hG[i]
	}
	c.M1 = hash(xor, hash([]byte(strings.ToLower(accountName))), salt, A, serverB, c.K)
	c.M2 = hash(A, c.M1, c.K)
	return nil
}

// derivePassword derives the SRP password key: PBKDF2-SHA256 over
// SHA256(password) for "s2k", over its lowercase hex for "s2k_fo".
func derivePassword(password string, salt []byte, iterations int, protocol string) ([]byte, error) {
	sum := sha256.Sum256([]byte(password))
	var input string
	switch protocol {
	case protocolS2K:
		input = string(sum[:])
	case protocolS2KFO:
		input = hex.EncodeToString(sum[:])
	default:
		return nil, fmt.Errorf("srp: unsupported password protocol %q", protocol)
	}
	key, err := pbkdf2.Key(sha256.New, input, salt, iterations, derivedKeyLen)
	if err != nil {
		return nil, fmt.Errorf("srp: derive password key: %w", err)
	}
	return key, nil
}
