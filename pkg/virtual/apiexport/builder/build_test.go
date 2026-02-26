/*
Copyright 2025 The KCP Authors.

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

package builder

import (
	"sync"
	"testing"

	"k8s.io/apiserver/pkg/authentication/user"
)

// TestNewImpersonationConfigConcurrency verifies that newImpersonationConfig
// can be called concurrently with a shared user.Info without causing a race
// on the Extra map. This is a regression test for
// https://github.com/kcp-dev/kcp/issues/3855.
func TestNewImpersonationConfigConcurrency(t *testing.T) {
	sharedUser := &user.DefaultInfo{
		Name:   "testuser",
		Groups: []string{"group1", "group2"},
		Extra: map[string][]string{
			"existing-key": {"val1", "val2"},
		},
	}

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			// Call the production helper that deep-copies Extra and appends warrants.
			cfg := newImpersonationConfig(sharedUser, `{"user":"system:serviceaccount:default:rest"}`)

			// Verify the returned config has the warrant.
			if len(cfg.Extra["authorization.kcp.io/warrant"]) != 1 {
				t.Errorf("expected 1 warrant, got %d", len(cfg.Extra["authorization.kcp.io/warrant"]))
			}
		}()
	}

	wg.Wait()

	// Verify the original user's Extra map was not mutated.
	if _, found := sharedUser.Extra["authorization.kcp.io/warrant"]; found {
		t.Fatal("original user Extra map was mutated: warrant key should not exist")
	}
	if got := sharedUser.Extra["existing-key"]; len(got) != 2 || got[0] != "val1" || got[1] != "val2" {
		t.Fatalf("original user Extra values were mutated: got %v", got)
	}
}
