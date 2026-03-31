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

package helpers

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TolerateErr returns true if the error is tolerated.
type TolerateErr func(error) bool

// TolerateOrFail verifies that the supplied error is not nil, and is
// tolerated by at least one of the TolerateErr functions. In that case,
// true is returned. False is returned if the error is nil.
//
// TolerateOrFail fails the test if err is not nil, and none of the
// TolerateErr functions tolerate the error.
func TolerateOrFail(t *testing.T, err error, tolerateErrFuncs ...TolerateErr) bool {
	if err == nil {
		return false
	}

	t.Helper()
	for _, tolerateErr := range tolerateErrFuncs {
		if tolerateErr(err) {
			return true
		}
	}

	require.NoError(t, err)
	return false
}
