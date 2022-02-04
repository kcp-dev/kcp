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
)

type ClientLoader struct {
	sync.RWMutex
	clients map[string]*rest.Config
}

func NewClientLoader() *ClientLoader {
	return &ClientLoader{
		clients: make(map[string]*rest.Config),
	}
}

func (c *ClientLoader) Add(name string, config *rest.Config) {
	c.Lock()
	defer c.Unlock()
	c.clients[name] = config
}

func (c *ClientLoader) AddKubeConfigContexts(path string) error {
	loader := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	cfg, err := loader.Load()
	if err != nil {
		return fmt.Errorf("failed to load: %w", err)
	}

	c.Lock()
	defer c.Unlock()

	for context := range cfg.Contexts {
		contextCfg, err := clientcmd.NewNonInteractiveClientConfig(*cfg, context, &clientcmd.ConfigOverrides{}, loader).ClientConfig()
		if err != nil {
			return fmt.Errorf("create %s client: %w", context, err)
		}
		contextCfg.ContentType = "application/json"
		c.clients[context] = contextCfg
		c.Unlock()
	}

	return nil
}

func (c *ClientLoader) Clients() map[string]*rest.Config {
	c.Lock()
	defer c.Unlock()

	out := make(map[string]*rest.Config, len(c.clients))
	for key, value := range c.clients {
		out[key] = rest.CopyConfig(value)
	}

	return out
}
