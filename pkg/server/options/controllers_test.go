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

package options

import (
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestControllersWorkerFlags(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		args        []string
		wantGC      int
		wantQuota   int
		wantInvalid int
	}{
		"defaults": {
			wantGC:    4,
			wantQuota: 4,
		},
		"overridden": {
			args:      []string{"--concurrent-gc-syncs=10", "--concurrent-resource-quota-syncs=2"},
			wantGC:    10,
			wantQuota: 2,
		},
		"zero gc workers": {
			args:        []string{"--concurrent-gc-syncs=0"},
			wantGC:      0,
			wantQuota:   4,
			wantInvalid: 1,
		},
		"negative workers": {
			args:        []string{"--concurrent-gc-syncs=-1", "--concurrent-resource-quota-syncs=0"},
			wantGC:      -1,
			wantQuota:   0,
			wantInvalid: 2,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := NewControllers()
			fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
			c.AddFlags(fs)
			require.NoError(t, fs.Parse(tc.args))

			require.Equal(t, tc.wantGC, c.ConcurrentGarbageCollectorSyncs)
			require.Equal(t, tc.wantQuota, c.ConcurrentResourceQuotaSyncs)
			require.Len(t, c.Validate(), tc.wantInvalid)
		})
	}
}
