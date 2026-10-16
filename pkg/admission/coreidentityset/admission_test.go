/*
Copyright 2026 The kcp Authors.

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

package coreidentityset

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
)

func attr(op admission.Operation, u user.Info, gr string) admission.Attributes {
	set := &adminv1alpha1.CoreIdentitySet{ObjectMeta: metav1.ObjectMeta{Name: "root"}}
	resource := adminv1alpha1.Resource("coreidentitysets").WithVersion("v1alpha1")
	if gr == "shards" {
		resource = corev1alpha1.Resource("shards").WithVersion("v1alpha1")
	}
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(set), nil,
		adminv1alpha1.Kind("CoreIdentitySet").WithVersion("v1alpha1"),
		"", set.Name, resource, "", op, &metav1.CreateOptions{}, false, u,
	)
}

func TestValidate(t *testing.T) {
	t.Parallel()

	system := &user.DefaultInfo{Name: "system:apiserver", Groups: []string{user.SystemPrivilegedGroup}}
	admin := &user.DefaultInfo{Name: "admin", Groups: []string{"system:kcp:admin"}}

	tests := map[string]struct {
		op      admission.Operation
		user    user.Info
		cluster string
		gr      string
		wantErr bool
	}{
		"system in system:shard creates":     {op: admission.Create, user: system, cluster: "system:shard"},
		"system in system:shard updates":     {op: admission.Update, user: system, cluster: "system:shard"},
		"system in system:shard deletes":     {op: admission.Delete, user: system, cluster: "system:shard"},
		"system elsewhere is forbidden":      {op: admission.Create, user: system, cluster: "root", wantErr: true},
		"admin in system:shard is forbidden": {op: admission.Update, user: admin, cluster: "system:shard", wantErr: true},
		"admin elsewhere is forbidden":       {op: admission.Create, user: admin, cluster: "root:org", wantErr: true},
		"reads are not affected":             {op: admission.Connect, user: admin, cluster: "root:org"},
		"other resources are not affected":   {op: admission.Create, user: admin, cluster: "root:org", gr: "shards"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o := &coreIdentitySet{Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete)}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.Name(tc.cluster)})
			err := o.Validate(ctx, attr(tc.op, tc.user, tc.gr), nil)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
