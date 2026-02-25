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

package rootidentities

import (
	"context"
	"embed"
	"fmt"
	"sync"

	"sigs.k8s.io/yaml"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
)

//go:embed *.yaml
var fs embed.FS

// bootstrapIdentities is the structure we expect to receive
// from --root-identities-file in YAML format.
type bootstrapIdentities struct {
	Identities []struct {
		Export   string `json:"export"`
		Identity string `json:"identity"`
	} `json:"identities"`
}

// Bootstrap creates resources in this package by continuously retrying the list.
// This is blocking, i.e. it only returns (with error) when the context is closed or with nil when
// the bootstrapping is successfully completed.
func Bootstrap(
	ctx context.Context,
	rootDiscoveryClient discovery.DiscoveryInterface,
	rootDynamicClient dynamic.Interface,
	kubeClient kubernetes.Interface,
	bootstrapIdentitiesBytes []byte,
) error {
	var identities bootstrapIdentities
	if err := yaml.UnmarshalStrict(bootstrapIdentitiesBytes, &identities); err != nil {
		return fmt.Errorf("failed to parse bootstrap identities: %v", err)
	}

	errsChan := make(chan error, len(identities.Identities))
	wg := &sync.WaitGroup{}
	for _, identity := range identities.Identities {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := confighelpers.Bootstrap(ctx, kubeClient.Discovery(), rootDynamicClient, nil, fs, confighelpers.ReplaceOption(
				"IDENTITY_APIEXPORT", identity.Export,
				"IDENTITY_KEY", identity.Identity,
			))
			if err != nil {
				errsChan <- fmt.Errorf("failed to bootstrap identity secret for %s: %v", identity.Export, err)
			}
		}()
	}

	wg.Wait()
	close(errsChan)

	errs := make([]error, 0, len(identities.Identities))
	for err := range errsChan {
		errs = append(errs, err)
	}

	return utilerrors.NewAggregate(errs)
}
