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
	"context"
	"fmt"
	"io"
	"sort"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

const (
	QuickstartLabel       = "kcp.io/quickstart"
	QuickstartPrefixLabel = "kcp.io/quickstart-prefix"

	OrgSuffix      = "-org"
	ProviderSuffix = "-provider"
	ConsumerSuffix = "-consumer"

	StateKeyOrgPath      = "org-path"
	StateKeyProviderPath = "provider-path"
	StateKeyConsumerPath = "consumer-path"
	StateKeyWithSamples  = "with-samples"
)

type Step struct {
	Description string

	// CleanupDescription is shown in progress output during --cleanup runs.
	// Falls back to Description when empty.
	CleanupDescription string

	Execute func(ctx context.Context, execCtx ExecutionContext) error
	Cleanup func(ctx context.Context, execCtx ExecutionContext) error
}

type Scenario interface {
	Name() string
	Steps(prefix string) []Step
	Samples(prefix string) []Step
	PrintSummary(out io.Writer, prefix string, state map[string]string) error
}

type ExecutionContext struct {
	KCPClusterClient kcpclientset.ClusterInterface
	DynamicClient    kcpdynamic.ClusterInterface
	Out              io.Writer
	ErrOut           io.Writer

	// mutable state passed between steps
	// e.g. "org-path" -> "root:quickstart-org".
	State map[string]string
}

var registry = map[string]func() Scenario{
	"api-provider": func() Scenario { return &apiProviderScenario{} },
}

func Get(name string) (Scenario, error) {
	factory, ok := registry[name]
	if !ok {
		names := make([]string, 0, len(registry))
		for k := range registry {
			names = append(names, k)
		}
		sort.Strings(names)

		return nil, fmt.Errorf("unknown scenario %q, available: %v", name, names)
	}

	return factory(), nil
}
