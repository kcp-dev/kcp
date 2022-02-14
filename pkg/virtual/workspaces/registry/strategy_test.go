/*
Copyright 2021 The KCP Authors.

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

package registry

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apirequest "k8s.io/apiserver/pkg/endpoints/request"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func TestWorkspaceStrategy(t *testing.T) {
	ctx := apirequest.NewDefaultContext()
	if Strategy.NamespaceScoped() {
		t.Errorf("Workspaces should not be namespace scoped")
	}
	if Strategy.AllowCreateOnUpdate() {
		t.Errorf("Workspaces should not allow create on update")
	}
	if Strategy.AllowUnconditionalUpdate() {
		t.Errorf("Workspaces should not allow unconditional update")
	}
	project := &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "foo", ResourceVersion: "10"},
	}
	Strategy.PrepareForCreate(ctx, project)
	errs := Strategy.Validate(ctx, project)
	if len(errs) != 0 {
		t.Errorf("Unexpected error validating %v", errs)
	}
}
