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
)

type namespaceRewriter struct {
	Namespaces map[string]string
}

func (m *namespaceRewriter) updateFromConfigmap(cm *corev1.ConfigMap) {
	klog.Infof("reloading nsmap config map : +%v", cm.Data)
	m.Namespaces = cm.Data
}

// Rewrite rewrites the current request.
func (m *namespaceRewriter) Rewrite(ctx context.Context, state request.Request) rewrite.ResponseRules {
	name := state.Name()

	parts := strings.SplitN(name, ".", 3)
	if len(parts) < 2 {
		// No dots: fallthrough
		return nil
	}

	if len(parts) == 3 && !strings.HasPrefix(parts[2], "svc") { // TODO(LV): custom prefix
		// not a cluster local name: fallthrough
		return nil
	}

	ns := parts[1]
	if targetNs, ok := m.Namespaces[ns]; ok {
		// Do the rewriting
		// TODO(LV): check the response resolves. If not, try again without rewriting
		// For instance, bit.ly can either refer to a local service (name: bit, ns: ly) or an external service.
		// The ly namespace might not contain a bit service, and will fail the be properly resolved.
		replacement := parts[0] + "." + targetNs + "." + parts[2]

		if klog.V(4).Enabled() {
			klog.Infof("rewriting dns name from %s to %s", name, replacement)
		}

		state.Req.Question[0].Name = replacement

		rewriter := newRemapStringRewriter(state.Req.Question[0].Name, state.Name())
		return rewrite.ResponseRules{
			&nameRewriterResponseRule{rewriter},
			&valueRewriterResponseRule{rewriter},
		}
	}

	return nil
}
