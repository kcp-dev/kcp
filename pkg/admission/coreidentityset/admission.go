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
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"

	configshard "github.com/kcp-dev/kcp/config/shard"
)

const (
	PluginName = "admin.kcp.io/CoreIdentitySet"

	hint = "CoreIdentitySet objects are written by the shards themselves, one per shard in the shard-local system:shard logical cluster. " +
		"View them via the Admin workspace (kubectl ws use :admin)"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &coreIdentitySet{
				Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete),
			}, nil
		})
}

// coreIdentitySet protects CoreIdentitySet objects: only system components
// write them, and only in the system:shard logical cluster.
type coreIdentitySet struct {
	*admission.Handler
}

var _ = admission.ValidationInterface(&coreIdentitySet{})

func (o *coreIdentitySet) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != adminv1alpha1.Resource("coreidentitysets") {
		return nil
	}

	switch a.GetOperation() {
	case admission.Create, admission.Update, admission.Delete:
	default:
		return nil
	}

	if !sets.New(a.GetUserInfo().GetGroups()...).Has(user.SystemPrivilegedGroup) {
		return admission.NewForbidden(a, fmt.Errorf("%s", hint))
	}

	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return admission.NewForbidden(a, err)
	}
	if cluster.Name != configshard.SystemShardCluster {
		return admission.NewForbidden(a, fmt.Errorf("CoreIdentitySet objects only exist in the %s logical cluster; %s", configshard.SystemShardCluster, hint))
	}
	return nil
}
