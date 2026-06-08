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
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEncoderBytes(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		enc  Encoder
		in   []byte
		want string
	}{
		"base36 empty":     {enc: Base36, in: []byte{}, want: "0"},
		"base36 zero byte": {enc: Base36, in: []byte{0x00}, want: "0"},
		"base36 one":       {enc: Base36, in: []byte{0x01}, want: "1"},
		"base36 0xff":      {enc: Base36, in: []byte{0xff}, want: "73"},
		"base36 0x0100":    {enc: Base36, in: []byte{0x01, 0x00}, want: "74"},
		"base62 empty":     {enc: Base62, in: []byte{}, want: "0"},
		"base62 zero byte": {enc: Base62, in: []byte{0x00}, want: "0"},
		"base62 one":       {enc: Base62, in: []byte{0x01}, want: "1"},
		"base62 0xff":      {enc: Base62, in: []byte{0xff}, want: "47"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.enc.Bytes(tc.in))
		})
	}
}

func TestEncoderBytesPad(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		enc  Encoder
		in   []byte
		want string
	}{
		"base36 no leading zero":    {enc: Base36, in: []byte{0x01}, want: "1"},
		"base36 0xff":               {enc: Base36, in: []byte{0xff}, want: "73"},
		"base36 one leading zero":   {enc: Base36, in: []byte{0x00, 0x01}, want: "01"},
		"base36 two leading zeros":  {enc: Base36, in: []byte{0x00, 0x00, 0x01}, want: "001"},
		"base36 leading zero multi": {enc: Base36, in: []byte{0x00, 0xc6, 0x50, 0xb1, 0xad, 0x5a, 0x99, 0xc3}, want: "0f9mt7goyif7"},
		"base36 all zeros":          {enc: Base36, in: []byte{0x00, 0x00}, want: "00"},
		"base36 empty":              {enc: Base36, in: []byte{}, want: ""},
		"base62 no leading zero":    {enc: Base62, in: []byte{0x01}, want: "1"},
		"base62 one leading zero":   {enc: Base62, in: []byte{0x00, 0x01}, want: "01"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.enc.BytesPad(tc.in))
		})
	}
}

func TestEncoderInt(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		enc  Encoder
		in   uint64
		want string
	}{
		"base36 zero":       {enc: Base36, in: 0, want: "0"},
		"base36 one":        {enc: Base36, in: 1, want: "1"},
		"base36 thirty six": {enc: Base36, in: 36, want: "10"},
		"base36 max":        {enc: Base36, in: math.MaxUint64, want: "3w5e11264sgsf"},
		"base62 zero":       {enc: Base62, in: 0, want: "0"},
		"base62 one":        {enc: Base62, in: 1, want: "1"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.enc.Int(tc.in))
		})
	}
}

func TestEncoderIntPad(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		enc   Encoder
		in    uint64
		width int
		want  string
	}{
		"base36 zero":       {enc: Base36, in: 0, width: Base36Uint64Width, want: "0000000000000"},
		"base36 one":        {enc: Base36, in: 1, width: Base36Uint64Width, want: "0000000000001"},
		"base36 thirty six": {enc: Base36, in: 36, width: Base36Uint64Width, want: "0000000000010"},
		"base36 max":        {enc: Base36, in: math.MaxUint64, width: Base36Uint64Width, want: "3w5e11264sgsf"},
		"base62 zero":       {enc: Base62, in: 0, width: Base62Uint64Width, want: "00000000000"},
		"base62 one":        {enc: Base62, in: 1, width: Base62Uint64Width, want: "00000000001"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := tc.enc.IntPad(tc.in, tc.width)
			assert.Equal(t, tc.want, got)
			assert.Len(t, got, tc.width)
		})
	}
}
