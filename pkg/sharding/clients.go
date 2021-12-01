/*
Copyright 2021 The KCP Authors.

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

package sharding

import (
	"fmt"
	"sync"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
)

type IdentifiedConfig struct {
	Identifier string
	Config     *rest.Config
}

type ClientLoader struct {
	*sync.RWMutex
	clients map[string]*rest.Config
}

func New(delegates string, injector <-chan IdentifiedConfig) (*ClientLoader, error) {
	l := &ClientLoader{
		clients: map[string]*rest.Config{},
		RWMutex: &sync.RWMutex{},
	}

	loader := &clientcmd.ClientConfigLoadingRules{ExplicitPath: delegates}
	cfg, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("failed to load: %w", err)
	}
	for context := range cfg.Contexts {
		contextCfg, err := clientcmd.NewNonInteractiveClientConfig(*cfg, context, &clientcmd.ConfigOverrides{}, loader).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("create %s client: %w", context, err)
		}
		contextCfg.ContentType = "application/json"
		l.Lock()
		l.clients[genericcontrolplane.SanitizeClusterId(context)] = contextCfg
		l.Unlock()
	}

	l.Lock()
	go func() {
		defer l.Unlock()
		local := <-injector
		local.Config.ContentType = "application/json"
		l.clients[genericcontrolplane.SanitizeClusterId(local.Identifier)] = local.Config
	}()

	return l, nil
}

func (c *ClientLoader) Clients() map[string]*rest.Config {
	c.Lock()
	out := map[string]*rest.Config{}
	for key, value := range c.clients {
		out[key] = rest.CopyConfig(value)
	}
	c.Unlock()
	return out
}
