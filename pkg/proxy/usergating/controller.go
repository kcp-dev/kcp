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

package usergating

import (
	"context"
	"fmt"
	"sync"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/client-go/informers"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/proxy/usergating/ruleset"
)

const (
	controllerName = "kcp-user-gating"
	resyncPeriod   = 10 * time.Minute

	UserGateNamespace = "kcp-system"
	UserGateResource  = "usergate.kcp.dev"
	RuleKey           = "ruleset.yaml"
)

type SecretGetter func(string) (*v1.Secret, error)

// Controller maintains a set of rules configured from a secret in
// root:kcp-system that determine which users can use the service.
type Controller struct {
	queue workqueue.RateLimitingInterface

	GetSecret SecretGetter

	lock  sync.RWMutex
	Rules *ruleset.RuleSet

	DefaultRules *ruleset.RuleSet
}

// NewController creates a controller that watches a secret named
// usergate.kcp.dev in root:kcp-system. The secret contains yaml under the key
// "usergate.yaml" that describes which users are allowed or denied access to
// the system.
func NewController(ctx context.Context, versionedInformers informers.SharedInformerFactory, defaultRules *ruleset.RuleSet) *Controller {
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), controllerName)
	secretInformer := versionedInformers.Core().V1().Secrets().Cluster(tenancyv1alpha1.RootCluster)

	c := &Controller{
		queue:        queue,
		GetSecret:    secretInformer.Lister().Secrets(UserGateNamespace).Get,
		DefaultRules: defaultRules,
	}

	secretInformer.Informer().AddEventHandlerWithResyncPeriod(
		cache.FilteringResourceEventHandler{
			FilterFunc: func(obj interface{}) bool {
				if secret, ok := obj.(v1.Secret); ok {
					return secret.Name == UserGateResource
				}
				return false
			},
			Handler: cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					c.enqueueSecret(ctx, obj)
				},
				UpdateFunc: func(old, obj interface{}) {
					c.enqueueSecret(ctx, obj)
				},
				DeleteFunc: func(obj interface{}) {
					c.lock.Lock()
					defer c.lock.Unlock()
					c.Rules = nil
				},
			},
		},
		resyncPeriod)
	return c
}

// GetRuleSet returns the parsed rules that determine whether a user
// is allowed through the gate.
func (c *Controller) GetRuleSet() (ruleset.RuleSet, bool) {
	c.lock.RLock()
	defer c.lock.RUnlock()
	if c.Rules != nil {
		return *c.Rules, true
	}
	if c.DefaultRules != nil {
		return *c.DefaultRules, true
	}
	return ruleset.RuleSet{}, false
}

func (c *Controller) enqueueSecret(ctx context.Context, obj interface{}) {
	key, err := kcpcache.DeletionHandlingMetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		runtime.HandleError(err)
		return
	}

	c.queue.Add(key)
}

// Start the controller.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()

	logger := klog.FromContext(ctx).WithValues("controller", controllerName)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}
func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k.(string)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		runtime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", controllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	logger.WithValues("key", key).Info("updating usergate secret")

	secret, err := c.GetSecret(key)
	if err != nil {
		if errors.IsNotFound(err) {
			c.lock.Lock()
			defer c.lock.Unlock()
			c.Rules = nil

			return nil
		}
		return err
	}

	rules, err := ruleset.FromYAML(secret.Data[RuleKey])
	if err != nil {
		return err
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	c.Rules = rules

	return nil
}
