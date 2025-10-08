/*
Copyright 2017 The Kubernetes Authors.
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

	certificates "k8s.io/api/certificates/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	core "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
)

func (c *certificateSigningRequestScopedClient) UpdateApproval(ctx context.Context, certificateSigningRequest *certificates.CertificateSigningRequest, opts metav1.UpdateOptions) (result *certificates.CertificateSigningRequest, err error) {
	obj, err := c.Fake.
		Invokes(core.NewRootUpdateSubresourceAction(c.Resource(), c.ClusterPath, "approval", certificateSigningRequest), &certificates.CertificateSigningRequest{})
	if obj == nil {
		return nil, err
	}
	return obj.(*certificates.CertificateSigningRequest), err
}
