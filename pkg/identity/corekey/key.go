/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package corekey derives the identities of kcp's core APIExports from one
// private key that is shared by all shards (--core-identities-key-file).
//
// Every shard computes the same identities from the same key, so no shard
// has to ask the root shard for them. The key is only used to fill in
// identities that do not exist yet; identities that are already in use are
// never replaced.
package corekey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// Derivation names the algorithm and version used to derive identity keys
// from the core identities key. It is recorded next to every derived
// identity (CoreIdentity.derivation) so a future derivation can be told
// apart from this one.
const Derivation = "hkdf-sha256/v1"

// salt binds the derivation to its purpose and version. Changing it is a
// new derivation and must come with a new Derivation name.
var salt = []byte("kcp.io/core-identities/" + Derivation)

// minRSABits is the smallest RSA key size accepted.
const minRSABits = 2048

// Key is a loaded core identities key.
type Key struct {
	// id is the SHA-256 hex digest of the public key (SubjectPublicKeyInfo
	// DER). It is public and identifies the key without revealing it.
	id string
	// secret is the private key in canonical PKCS#8 DER form. The file
	// format the key was loaded from (PKCS#1, PKCS#8, SEC1) does not
	// matter: the same key always has the same secret.
	secret []byte
}

// Load reads and parses a PEM-encoded private key file.
func Load(path string) (*Key, error) {
	bs, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read core identities key file %q: %w", path, err)
	}
	k, err := Parse(bs)
	if err != nil {
		return nil, fmt.Errorf("invalid core identities key file %q: %w", path, err)
	}
	return k, nil
}

// Parse parses a PEM-encoded private key. PKCS#8 ("PRIVATE KEY"), PKCS#1
// ("RSA PRIVATE KEY") and SEC1 ("EC PRIVATE KEY") blocks are accepted; the
// first private key block is used. ECDSA, Ed25519 and RSA keys of at least
// 2048 bits are supported.
func Parse(pemBytes []byte) (*Key, error) {
	var priv crypto.PrivateKey
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		var err error
		switch block.Type {
		case "PRIVATE KEY":
			priv, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		case "RSA PRIVATE KEY":
			priv, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		case "EC PRIVATE KEY":
			priv, err = x509.ParseECPrivateKey(block.Bytes)
		default:
			// e.g. a CERTIFICATE block in the same file
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s block: %w", block.Type, err)
		}
		break
	}
	if priv == nil {
		return nil, errors.New("no PEM-encoded private key found")
	}
	return FromPrivateKey(priv)
}

// FromPrivateKey builds a Key from an already parsed private key.
func FromPrivateKey(priv crypto.PrivateKey) (*Key, error) {
	var pub crypto.PublicKey
	switch k := priv.(type) {
	case *rsa.PrivateKey:
		if k.N.BitLen() < minRSABits {
			return nil, fmt.Errorf("RSA key is %d bits, need at least %d", k.N.BitLen(), minRSABits)
		}
		pub = &k.PublicKey
	case *ecdsa.PrivateKey:
		pub = &k.PublicKey
	case ed25519.PrivateKey:
		pub = k.Public()
	default:
		return nil, fmt.Errorf("unsupported private key type %T, need RSA, ECDSA or Ed25519", priv)
	}

	secret, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key: %w", err)
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal public key: %w", err)
	}
	sum := sha256.Sum256(spki)

	return &Key{
		id:     hex.EncodeToString(sum[:]),
		secret: secret,
	}, nil
}

// ID returns the public key ID: the SHA-256 hex digest of the public key.
// It tells whether a key changed without revealing it.
func (k *Key) ID() string {
	return k.id
}

// IdentityKey derives the identity key (the secret stored in the APIExport's
// identity Secret) for the named core APIExport. It has the same form as a
// randomly generated identity key: 256 bits, base64url-encoded without
// padding.
func (k *Key) IdentityKey(exportName string) (string, error) {
	if exportName == "" {
		return "", errors.New("export name must not be empty")
	}
	raw, err := hkdf.Key(sha256.New, k.secret, salt, exportName, 32)
	if err != nil {
		return "", fmt.Errorf("failed to derive identity key for %s: %w", exportName, err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// IdentityHash derives the identity hash for the named core APIExport: the
// SHA-256 hex digest of the identity key, as it appears in request URLs and
// storage keys.
func (k *Key) IdentityHash(exportName string) (string, error) {
	identityKey, err := k.IdentityKey(exportName)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(identityKey))
	return hex.EncodeToString(sum[:]), nil
}
