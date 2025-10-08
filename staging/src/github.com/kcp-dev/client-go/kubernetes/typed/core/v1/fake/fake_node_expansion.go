/*
Copyright 2016 The Kubernetes Authors.
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

	v1 "k8s.io/api/core/v1"
	types "k8s.io/apimachinery/pkg/types"

	core "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
)

// TODO: Should take a PatchType as an argument probably.
func (c *nodeScopedClient) PatchStatus(_ context.Context, nodeName string, data []byte) (*v1.Node, error) {
	// TODO: Should be configurable to support additional patch strategies.
	pt := types.StrategicMergePatchType
	obj, err := c.Fake.Invokes(
		core.NewRootPatchSubresourceAction(c.Resource(), c.ClusterPath, nodeName, pt, data, "status"), &v1.Node{})
	if obj == nil {
		return nil, err
	}

	return obj.(*v1.Node), err
}
