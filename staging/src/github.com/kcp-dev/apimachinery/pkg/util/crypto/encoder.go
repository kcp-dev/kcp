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
	"encoding/binary"
	"math/big"
	"strings"
)

const (
	// padChar is the character used to left-pad short encodings.
	padChar = "0"

	// Base36Uint64Width is the maximum number of base36 characters needed to represent any uint64:
	// ceil(64 / log2(36)) = 13.
	Base36Uint64Width = 13

	// Base62Uint64Width is the maximum number of base62 characters needed to represent any uint64:
	// ceil(64 / log2(62)) = 11.
	Base62Uint64Width = 11
)

// Encoder produces base-N encodings of byte slices and uint64s.
type Encoder struct {
	base int
}

// NewEncoder returns an Encoder for the given base.
func NewEncoder(base int) Encoder {
	return Encoder{base: base}
}

var (
	// Base36 encodes input as base36.
	Base36 = NewEncoder(36)
	// Base62 encodes input as base62.
	Base62 = NewEncoder(62)
)

// Bytes returns the base-N encoding of b.
// Leading zero bytes in b are dropped; use [Encoder.BytesPad] to preserve them.
func (e Encoder) Bytes(b []byte) string {
	return new(big.Int).SetBytes(b).Text(e.base)
}

// BytesPad returns the base-N encoding of b, with one pad character
// prepended for each leading zero byte in b. An empty or all-zero b is encoded
// as a string of len(b) pad characters.
func (e Encoder) BytesPad(b []byte) string {
	leadingZeros := 0
	for _, x := range b {
		if x != 0 {
			break
		}
		leadingZeros++
	}
	if leadingZeros == len(b) {
		return strings.Repeat(padChar, leadingZeros)
	}
	return strings.Repeat(padChar, leadingZeros) + e.Bytes(b)
}

// Int returns the base-N encoding of v's big-endian byte representation.
// The output is variable-length, use [Encoder.IntPad] for fixed-length.
func (e Encoder) Int(v uint64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return e.Bytes(b[:])
}

// IntPad returns the base-N encoding of v's big-endian byte representation,
// left-zero-padded to width characters.
func (e Encoder) IntPad(v uint64, width int) string {
	return pad(e.Int(v), width)
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat(padChar, width-len(s)) + s
}
