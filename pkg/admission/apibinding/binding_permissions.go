/*
Copyright 2023 The kcp Authors.

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

package apibinding

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// CheckDefaultAPIBindingsAccess verifies that 'u' has the 'bind' verb on every APIExport
// referenced by a WorkspaceType's defaultAPIBindings.
func CheckDefaultAPIBindingsAccess(
	ctx context.Context,
	u user.Info,
	localCluster logicalcluster.Name,
	bindings []tenancyv1alpha1.APIExportReference,
	getLogicalCluster func(path logicalcluster.Path) (*corev1alpha1.LogicalCluster, error),
	newAuthorizer func(clusterName logicalcluster.Name) (authorizer.Authorizer, error),
	showExportPathInErrors bool,
) error {
	for _, ref := range bindings {
		notPermitted := errors.New("no permission to bind one or more of the default API bindings")
		if showExportPathInErrors {
			notPermitted = fmt.Errorf("no permission to bind to export %s",
				logicalcluster.NewPath(ref.Path).Join(ref.Export).String())
		}

		var exportClusterName logicalcluster.Name
		switch {
		case ref.Path == "":
			exportClusterName = localCluster
		case ref.Path == core.RootCluster.String():
			exportClusterName = core.RootCluster
		default:
			lc, err := getLogicalCluster(logicalcluster.NewPath(ref.Path))
			if err != nil {
				return notPermitted
			}
			exportClusterName = logicalcluster.From(lc)
		}

		authz, err := newAuthorizer(exportClusterName)
		if err != nil {
			return notPermitted
		}
		if err := CheckAPIExportAccess(ctx, u, ref.Export, authz); err != nil {
			return notPermitted
		}
	}

	return nil
}

func CheckAPIExportAccess(ctx context.Context, user user.Info, apiExportName string, authz authorizer.Authorizer) error {
	bindAttr := authorizer.AttributesRecord{
		User:            user,
		Verb:            "bind",
		APIGroup:        apisv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      apisv1alpha1.SchemeGroupVersion.Version,
		Resource:        "apiexports",
		Name:            apiExportName,
		ResourceRequest: true,
	}

	if decision, _, err := authz.Authorize(ctx, bindAttr); err != nil {
		return fmt.Errorf("unable to determine access to apiexports: %w", err)
	} else if decision != authorizer.DecisionAllow {
		return fmt.Errorf("no permission to bind to export %q", apiExportName)
	}

	return nil
}
