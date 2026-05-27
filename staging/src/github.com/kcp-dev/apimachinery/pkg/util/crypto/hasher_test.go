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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasherBytes(t *testing.T) {
	tests := map[string]struct {
		h    Hasher
		in   []byte
		want string
	}{
		"base36 empty":             {h: Base36Sha224, in: []byte(""), want: "2n6a5vwpk9ckyxi5x87i7768t4bpqmp64qi0556ih35b"},
		"base36 hello":             {h: Base36Sha224, in: []byte("hello"), want: "2yfg0ibsewngbzapgs5fesiord507vsncfiw4jd5c2oj"},
		"base36 leading zero byte": {h: Base36Sha224, in: []byte("input50"), want: "4xtfzmllsov5334t8xolbc31qfgtl44oza013n6uzk"},
		"base62 empty":             {h: Base62Sha224, in: []byte(""), want: "aAkHCkvjglfF66mdhRcVKb0I3FT1lepYlWZNwj"},
		"base62 hello":             {h: Base62Sha224, in: []byte("hello"), want: "bPWFAjMdldrWYheSsV0t7VkdTNtgGHBplr6qLF"},
		"base62 leading zero byte": {h: Base62Sha224, in: []byte("input50"), want: "WF7EK9IErgKvTHdcQRzkjJDEJDBOwfQSmzEQ"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.h.Bytes(tc.in))
		})
	}
}

func TestHasherBytesPad(t *testing.T) {
	tests := map[string]struct {
		h    Hasher
		in   []byte
		want string
	}{
		"base36 empty":             {h: Base36Sha224, in: []byte(""), want: "2n6a5vwpk9ckyxi5x87i7768t4bpqmp64qi0556ih35b"},
		"base36 hello":             {h: Base36Sha224, in: []byte("hello"), want: "2yfg0ibsewngbzapgs5fesiord507vsncfiw4jd5c2oj"},
		"base36 leading zero byte": {h: Base36Sha224, in: []byte("input50"), want: "04xtfzmllsov5334t8xolbc31qfgtl44oza013n6uzk"},
		"base62 empty":             {h: Base62Sha224, in: []byte(""), want: "aAkHCkvjglfF66mdhRcVKb0I3FT1lepYlWZNwj"},
		"base62 hello":             {h: Base62Sha224, in: []byte("hello"), want: "bPWFAjMdldrWYheSsV0t7VkdTNtgGHBplr6qLF"},
		"base62 leading zero byte": {h: Base62Sha224, in: []byte("input50"), want: "0WF7EK9IErgKvTHdcQRzkjJDEJDBOwfQSmzEQ"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.h.BytesPad(tc.in))
		})
	}
}

func TestHasherString(t *testing.T) {
	tests := map[string]struct {
		h    Hasher
		in   string
		want string
	}{
		"base36 empty":             {h: Base36Sha224, in: "", want: "2n6a5vwpk9ckyxi5x87i7768t4bpqmp64qi0556ih35b"},
		"base36 hello":             {h: Base36Sha224, in: "hello", want: "2yfg0ibsewngbzapgs5fesiord507vsncfiw4jd5c2oj"},
		"base36 leading zero byte": {h: Base36Sha224, in: "input50", want: "4xtfzmllsov5334t8xolbc31qfgtl44oza013n6uzk"},
		"base62 empty":             {h: Base62Sha224, in: "", want: "aAkHCkvjglfF66mdhRcVKb0I3FT1lepYlWZNwj"},
		"base62 hello":             {h: Base62Sha224, in: "hello", want: "bPWFAjMdldrWYheSsV0t7VkdTNtgGHBplr6qLF"},
		"base62 leading zero byte": {h: Base62Sha224, in: "input50", want: "WF7EK9IErgKvTHdcQRzkjJDEJDBOwfQSmzEQ"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.h.String(tc.in))
		})
	}
}

func TestHasherStringPad(t *testing.T) {
	tests := map[string]struct {
		h    Hasher
		in   string
		want string
	}{
		"base36 empty":             {h: Base36Sha224, in: "", want: "2n6a5vwpk9ckyxi5x87i7768t4bpqmp64qi0556ih35b"},
		"base36 hello":             {h: Base36Sha224, in: "hello", want: "2yfg0ibsewngbzapgs5fesiord507vsncfiw4jd5c2oj"},
		"base36 leading zero byte": {h: Base36Sha224, in: "input50", want: "04xtfzmllsov5334t8xolbc31qfgtl44oza013n6uzk"},
		"base62 empty":             {h: Base62Sha224, in: "", want: "aAkHCkvjglfF66mdhRcVKb0I3FT1lepYlWZNwj"},
		"base62 hello":             {h: Base62Sha224, in: "hello", want: "bPWFAjMdldrWYheSsV0t7VkdTNtgGHBplr6qLF"},
		"base62 leading zero byte": {h: Base62Sha224, in: "input50", want: "0WF7EK9IErgKvTHdcQRzkjJDEJDBOwfQSmzEQ"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.h.StringPad(tc.in))
		})
	}
}
