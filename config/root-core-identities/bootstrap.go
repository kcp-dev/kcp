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

package root

import (
	"context"
	"embed"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	confighelpers "github.com/kcp-dev/kcp/config/helpers"
)

//go:embed *.yaml
var fs embed.FS

type coreIdentitiesFile struct {
	Identities []struct {
		Export string `json:"export"`
		Key    string `json:"key"`
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
	identitiesFilename string,
) error {
	var coreIdentities coreIdentitiesFile
	bs, err := os.ReadFile(identitiesFilename)
	if err != nil {
		return fmt.Errorf("failed to read core identities file %s: %v", identitiesFilename, err)
	}
	if err = yaml.UnmarshalStrict(bs, &coreIdentities); err != nil {
		return fmt.Errorf("failed to parse YAML file %s: %v", identitiesFilename, err)
	}

	for _, identity := range coreIdentities.Identities {
		err = confighelpers.Bootstrap(ctx, kubeClient.Discovery(), rootDynamicClient, nil, fs, confighelpers.ReplaceOption(
			"IDENTITY_APIEXPORT", identity.Export,
			"IDENTITY_KEY", identity.Key,
		))
		if err != nil {
			return fmt.Errorf("failed to bootstrap identity secret for %s: %v", identity.Export, err)
		}
	}

	return nil
}
