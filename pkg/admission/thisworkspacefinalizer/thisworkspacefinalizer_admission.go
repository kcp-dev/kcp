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

package thisworkspacefinalizer

import (
	"io"

	"k8s.io/apiserver/pkg/admission"

	"github.com/kcp-dev/kcp/pkg/admission/finalizer"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/workspacedeletion/deletion"
)

const (
	PluginName = "tenancy.kcp.dev/ThisWorkspaceDeletionFinalizer"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &finalizer.FinalizerPlugin{
				Handler:       admission.NewHandler(admission.Create, admission.Update),
				FinalizerName: deletion.WorkspaceFinalizer,
				Resource:      tenancyv1alpha1.Resource("thisworkspaces"),
			}, nil
		})
}
