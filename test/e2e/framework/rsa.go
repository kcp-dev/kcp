/*
Copyright 2022 The KCP Authors.

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

package framework

import (
	"crypto/rand"
	"crypto/rsa"

	"k8s.io/client-go/util/keyutil"
)

// GenerateRSAPrivateKey creates a new RSA keypair and returns the
// private key as PEM-encoded bytes.
func GenerateRSAPrivateKey() ([]byte, error) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return nil, err
	}
	privKeyPEM, err := keyutil.MarshalPrivateKeyToPEM(rsaKey)
	if err != nil {
		return nil, err
	}
	return privKeyPEM, nil
}
