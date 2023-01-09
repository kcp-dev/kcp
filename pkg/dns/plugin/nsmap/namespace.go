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
	"strings"

	"github.com/coredns/coredns/plugin/rewrite"
	"github.com/coredns/coredns/request"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/third_party/coredns"
)

type namespaceRewriter struct {
	Namespaces map[string]string
}

func (m *namespaceRewriter) updateFromConfigmap(ctx context.Context, cm *corev1.ConfigMap) {
	logger := klog.FromContext(ctx)
	logger.Info("reloading nsmap ConfigMap", "data", cm.Data)
	m.Namespaces = cm.Data
}

// Rewrite the current request by replacing logical namespace to physical namespace (when applicable).
func (m *namespaceRewriter) Rewrite(ctx context.Context, state request.Request) rewrite.ResponseRules {
	name := state.Name()

	parts := strings.SplitN(name, ".", 3)
	if len(parts) < 2 {
		// No dots: fallthrough
		return nil
	}

	if len(parts) == 3 && !strings.HasPrefix(parts[2], "svc") {
		// not a cluster local name: fallthrough
		return nil
	}

	ns := parts[1]
	targetNs := m.Namespaces[ns]
	if targetNs == "" {
		return nil
	}

	// TODO(LV): check the response resolves. If not, try again without rewriting
	// For instance, bit.ly can either refer to a local service (name: bit, ns: ly) or an external service.
	// The ly namespace might not contain a bit service, and will fail to be properly resolved.
	replacement := parts[0] + "." + targetNs + "." + parts[2]

	klog.V(4).Info("rewriting dns name", "before", name, "after", replacement)

	state.Req.Question[0].Name = replacement

	rewriter := coredns.NewRemapStringRewriter(state.Req.Question[0].Name, state.Name())
	return rewrite.ResponseRules{
		&coredns.NameRewriterResponseRule{RemapStringRewriter: rewriter},
		&coredns.ValueRewriterResponseRule{RemapStringRewriter: rewriter},
	}
}
