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

package nsmap

import (
	"context"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/rewrite"

	"k8s.io/klog/v2"
)

func init() {
	plugin.Register("nsmap", setup)
}

func setup(c *caddy.Controller) error {
	rewriter := &namespaceRewriter{Namespaces: map[string]string{}}

	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		return LogicalToPhysicalNamespaceMapper{
			Next:         next,
			namespace:    rewriter,
			revertPolicy: rewrite.NewRevertPolicy(false, false),
		}
	})

	// Start ConfigMap watcher and notify the namespace rewriter
	ctx, cancel := context.WithCancel(context.Background())
	ctx = klog.NewContext(ctx, klog.Background())

	err := StartWatcher(ctx, rewriter.updateFromConfigmap)
	if err != nil {
		cancel()
		return err
	}

	c.OnFinalShutdown(func() error {
		cancel()
		return nil
	})

	return nil
}
