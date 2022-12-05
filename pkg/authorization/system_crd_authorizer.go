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

package authorization

import (
	"context"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

// SystemCRDAuthorizer protects the system CRDs from users who are admins in their workspaces.
type SystemCRDAuthorizer struct {
	delegate authorizer.Authorizer
}

func NewSystemCRDAuthorizer(delegate authorizer.Authorizer) authorizer.Authorizer {
	return &SystemCRDAuthorizer{delegate: delegate}
}

func (a *SystemCRDAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "empty cluster name", nil
	}

	switch {
	case attr.GetAPIGroup() == apisv1alpha1.SchemeGroupVersion.Group:
		switch {
		case attr.GetResource() == "apibindings" && attr.GetSubresource() == "status":
			return authorizer.DecisionDeny, "apibinding status updates not permitted", nil
		case attr.GetResource() == "apiexports" && attr.GetSubresource() == "status":
			return authorizer.DecisionDeny, "apiexport status updates not permitted", nil
		}
	}

	return a.delegate.Authorize(ctx, attr)
}
