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

package scenarios

import (
	"strings"
	"testing"
)

func TestScenariosGet(t *testing.T) {
	tests := []struct {
		name     string
		scenario string
		wantErr  string
	}{
		{
			name:     "unknown scenario",
			scenario: "does-not-exist",
			wantErr:  "unknown scenario",
		},
		{
			name:     "valid scenario",
			scenario: "api-provider",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Get(tt.scenario)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Get() error = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Get() unexpected error: %v", err)
			}
			if s == nil {
				t.Error("Get() returned nil scenario")
			}
		})
	}
}
