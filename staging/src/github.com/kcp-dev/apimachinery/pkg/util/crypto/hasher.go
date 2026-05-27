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

package crypto

import (
	"crypto/sha256"
)

// Hasher computes sha224(input) and encodes the digest with its embedded encoder.
type Hasher struct {
	enc Encoder
}

var (
	// Base36Sha224 hashes input with sha224 and encodes the digest as lowercase base36.
	Base36Sha224 = Hasher{enc: Base36}
	// Base62Sha224 hashes input with sha224 and encodes the digest as lowercase base62.
	Base62Sha224 = Hasher{enc: Base62}
)

// Bytes calculates the sha224 hash of b and returns its base-N encoding.
// Leading zero bytes in the digest are dropped; use [Hasher.BytesPad] to preserve them.
func (h Hasher) Bytes(b []byte) string {
	d := sha256.Sum224(b)
	return h.enc.Bytes(d[:])
}

// BytesPad calculates the sha224 hash of b and returns its base-N encoding,
// with one pad character prepended for each leading zero byte in the digest.
func (h Hasher) BytesPad(b []byte) string {
	d := sha256.Sum224(b)
	return h.enc.BytesPad(d[:])
}

// String calculates the sha224 hash of s and returns its base-N encoding.
// Leading zero bytes in the digest are dropped; use [Hasher.StringPad] to preserve them.
func (h Hasher) String(s string) string {
	d := sha256.Sum224([]byte(s))
	return h.enc.Bytes(d[:])
}

// StringPad calculates the sha224 hash of s and returns its base-N encoding,
// with one pad character prepended for each leading zero byte in the digest.
func (h Hasher) StringPad(s string) string {
	d := sha256.Sum224([]byte(s))
	return h.enc.BytesPad(d[:])
}
