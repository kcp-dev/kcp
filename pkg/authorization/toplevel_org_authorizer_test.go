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

package authorization

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v2"
)

func TestTopLevelOrg(t *testing.T) {
	tests := []struct {
		cluster string
		want    string
		wantOk  bool
	}{
		{"", "", false},
		{"root", "", false},

		{"root:org", "org", true},
		{"root:org:team", "org", true},
		{"root:org:team:universal", "org", true},

		{"foo:org", "", false},
		{"foo:org:team", "", false},
		{"foo:org:team:universal", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.cluster, func(t *testing.T) {
			got, gotOk := topLevelOrg(logicalcluster.New(tt.cluster))
			if got != tt.want {
				t.Errorf("rootOrg() got = %v, want %v", got, tt.want)
			}
			if gotOk != tt.wantOk {
				t.Errorf("rootOrg() ok = %v, want %v", gotOk, tt.wantOk)
			}
		})
	}
}
