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
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
)

// Suite should be called at the very beginning of a test case, to ensure that a test is only
// run when the suite containing it is selected by the user running tests.
func Suite(t *testing.T, suite string) {
	t.Helper()
	if !sets.New[string](TestConfig.Suites()...).Has(suite) {
		t.Skipf("suite %s disabled", suite)
	}
}
