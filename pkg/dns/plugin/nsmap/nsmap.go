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

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/plugin/rewrite"
	"github.com/coredns/coredns/request"
	"github.com/miekg/dns"
)

// LogicalToPhysicalNamespaceMapper is a CoreDNS plugin to map logical namespaces to physical namespaces.
type LogicalToPhysicalNamespaceMapper struct {
	Next         plugin.Handler
	namespace    *namespaceRewriter
	revertPolicy rewrite.RevertPolicy
}

// ServeDNS implements the plugin.Handler interface.
func (nm LogicalToPhysicalNamespaceMapper) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	wr := rewrite.NewResponseReverter(w, r, nm.revertPolicy)
	state := request.Request{W: w, Req: r}

	// Only one rewrite rule to apply, namespace rewriter
	respRules := nm.namespace.Rewrite(ctx, state)

	if respRules != nil {
		wr.ResponseRules = append(wr.ResponseRules, respRules...)
		return plugin.NextOrFailure(nm.Name(), nm.Next, ctx, wr, r)
	}

	return plugin.NextOrFailure(nm.Name(), nm.Next, ctx, w, r)
}

// Name implements the Handler interface.
func (nm LogicalToPhysicalNamespaceMapper) Name() string {
	return "nsmap"
}
