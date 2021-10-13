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
	Clients map[string]*rest.Config
}

func New(delegates string, injector <-chan IdentifiedConfig) (*ClientLoader, error) {
	l := &ClientLoader{
		Clients: map[string]*rest.Config{},
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
		l.Clients[genericcontrolplane.SanitizeClusterId(context)] = contextCfg
		l.Unlock()
	}

	l.Lock()
	go func() {
		defer l.Unlock()
		local := <-injector
		local.Config.ContentType = "application/json"
		l.Clients[genericcontrolplane.SanitizeClusterId(local.Identifier)] = local.Config
	}()

	return l, nil
}
