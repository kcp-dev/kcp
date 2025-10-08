/*
Copyright 2021 The Kubernetes Authors.
Modifications Copyright 2022 The KCP Authors.

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

package fake

import (
	"context"

	policy "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	core "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
)

func (c *evictionScopedClient) Evict(ctx context.Context, eviction *policy.Eviction) error {
	action := core.CreateActionImpl{}
	action.Verb = "create"
	action.Namespace = c.Namespace()
	action.ClusterPath = c.ClusterPath
	action.Resource = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	action.Subresource = "eviction"
	action.Object = eviction

	_, err := c.Fake.Invokes(action, eviction)
	return err
}
