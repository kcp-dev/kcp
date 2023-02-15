/*
Copyright 2023 The KCP Authors.

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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/component-base/cli/flag"
)

func TestNamedFlagSetOrder(t *testing.T) {
	fss := flag.NamedFlagSets{}
	NewOptions(".kcp").AddFlags(&fss)

	names := make([]string, 0, len(fss.FlagSets))
	for name, fs := range fss.FlagSets {
		if !fs.HasFlags() {
			continue
		}
		fmt.Printf("%q,\n", name)
		names = append(names, name)
	}

	require.Subset(t, names, namedFlagSetOrder, "namedFlagSetOrder has extra entries")
	require.Subset(t, namedFlagSetOrder, names, "namedFlagSetOrder in incomplete")
}
