/*
Copyright 2022 The kcp Authors.

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

package workspacetype

import (
	"context"
	"errors"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	apibindingadmission "github.com/kcp-dev/kcp/pkg/admission/apibinding"
	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

// Validate WorkspaceTypes creation and updates for
//   - the "root" WorkspaceType can only be created in the root cluster.
//   - .spec.defaultChildWorkspaceType.path, .spec.limitAllowedChildren.types[*].path,
//     and .spec.limitAllowedParents.types[*].path must be set when their parent
//     fields are present.
//   - the user has the "bind" verb on every APIExport listed in
//     spec.defaultAPIBindings (newly added entries on update). This prevents
//     a privilege escalation where unprivileged users would otherwise cause
//     the default-apibinding-controller to create APIBindings on their behalf
//     to APIExports they cannot bind directly.

const (
	PluginName = "tenancy.kcp.io/WorkspaceType"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			p := &workspacetype{
				Handler:          admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: delegated.NewDelegatedAuthorizer,
			}
			p.getLogicalCluster = func(path logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return indexers.ByPathAndNameWithFallback[*corev1alpha1.LogicalCluster](corev1alpha1.Resource("logicalclusters"), p.logicalClusterIndexer, p.cacheLogicalClusterIndexer, path, corev1alpha1.LogicalClusterName)
			}
			return p, nil
		})
}

type workspacetype struct {
	*admission.Handler

	getLogicalCluster func(path logicalcluster.Path) (*corev1alpha1.LogicalCluster, error)

	logicalClusterIndexer      cache.Indexer
	cacheLogicalClusterIndexer cache.Indexer

	deepSARClient    kcpkubernetesclientset.ClusterInterface
	createAuthorizer delegated.DelegatedAuthorizerFactory
}

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.ValidationInterface(&workspacetype{})
	_ = admission.InitializationValidator(&workspacetype{})
	_ = kcpinitializers.WantsKcpInformers(&workspacetype{})
	_ = kcpinitializers.WantsDeepSARClient(&workspacetype{})
)

func (o *workspacetype) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("workspacetypes") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	wt := &tenancyv1alpha1.WorkspaceType{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, wt); err != nil {
		return fmt.Errorf("failed to convert unstructured to WorkspaceType: %w", err)
	}

	if wt.Name == "root" && clusterName != core.RootCluster {
		return admission.NewForbidden(a, fmt.Errorf("root workspace type can only be created in root cluster"))
	}

	if wt.Spec.DefaultChildWorkspaceType != nil && wt.Spec.DefaultChildWorkspaceType.Path == "" {
		return admission.NewForbidden(a, fmt.Errorf(".spec.defaultChildWorkspaceType.path must be set"))
	}

	if wt.Spec.LimitAllowedChildren != nil {
		for i, t := range wt.Spec.LimitAllowedChildren.Types {
			if t.Path == "" {
				return admission.NewForbidden(a, fmt.Errorf(".spec.limitAllowedChildren.types[%d].path must be set", i))
			}
		}
	}

	if wt.Spec.LimitAllowedParents != nil {
		for i, t := range wt.Spec.LimitAllowedParents.Types {
			if t.Path == "" {
				return admission.NewForbidden(a, fmt.Errorf(".spec.limitAllowedParents.types[%d].path must be set", i))
			}
		}
	}

	return o.checkDefaultAPIBindingsPermissions(ctx, a, clusterName, wt)
}

// checkDefaultAPIBindingsPermissions ensures the user creating or updating the
// WorkspaceType has the "bind" verb on every APIExport newly added to
// spec.defaultAPIBindings. The default-apibinding-controller later creates
// APIBindings to these exports using its own (system) credentials, so without
// this check unprivileged users could indirectly bind APIExports they have no
// "bind" permission on.
func (o *workspacetype) checkDefaultAPIBindingsPermissions(ctx context.Context, a admission.Attributes, clusterName logicalcluster.Name, wt *tenancyv1alpha1.WorkspaceType) error {
	if len(wt.Spec.DefaultAPIBindings) == 0 {
		return nil
	}

	if !o.WaitForReady() {
		return admission.NewForbidden(a, errors.New("not yet ready to handle request"))
	}

	// `grandfathered` holds the entries that should be skipped by the permission
	// check below.
	//
	//   * On Create: stays nil. Reads from a nil map return (zero, false), so
	//     nothing is skipped and *every* binding in wt.Spec.DefaultAPIBindings
	//     is checked. This is the primary entry point for the permission gate.
	//
	//   * On Update: populated from the old object's bindings. Entries present
	//     in the old object are "grandfathered" — they were already validated
	//     when the WorkspaceType was created (or, for objects predating this
	//     check, predate it entirely). Only newly added bindings are checked,
	//     so a user without bind permission on an unrelated existing entry can
	//     still perform legitimate updates.
	var grandfathered map[tenancyv1alpha1.APIExportReference]struct{}
	if a.GetOperation() == admission.Update {
		oldU, ok := a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		oldWT := &tenancyv1alpha1.WorkspaceType{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(oldU.Object, oldWT); err != nil {
			return fmt.Errorf("failed to convert old unstructured to WorkspaceType: %w", err)
		}
		grandfathered = make(map[tenancyv1alpha1.APIExportReference]struct{}, len(oldWT.Spec.DefaultAPIBindings))
		for _, ref := range oldWT.Spec.DefaultAPIBindings {
			grandfathered[ref] = struct{}{}
		}
	}

	for _, ref := range wt.Spec.DefaultAPIBindings {
		// Skip grandfathered entries on Update; on Create the map is nil so this
		// is always false and every entry falls through to the permission check.
		if _, ok := grandfathered[ref]; ok {
			continue
		}

		exportPath := ref.Path
		exportName := ref.Export

		// Single forbidden response for both "cannot resolve the workspace"
		// and "user lacks bind on the export". Distinguishing the two would
		// let a caller with WorkspaceType create/update permission probe
		// for the existence of arbitrary workspaces by reading the error.
		forbidden := admission.NewForbidden(a, fmt.Errorf("unable to create or update WorkspaceType: no permission to bind to export %s",
			logicalcluster.NewPath(exportPath).Join(exportName).String()))

		// Resolve the APIExport's cluster. An empty path means the same cluster
		// as the WorkspaceType being admitted (matching the reconciler's
		// behavior). Otherwise resolve via LogicalCluster lookup — not via
		// the APIExport object itself, so that forward references to
		// not-yet-existing APIExports are accepted as long as the user has
		// pre-granted bind permission on the named resource (RBAC's
		// resourceNames matches names that do not yet exist). See #4145.
		var exportClusterName logicalcluster.Name
		switch {
		case exportPath == "":
			exportClusterName = clusterName
		case exportPath == core.RootCluster.String():
			exportClusterName = core.RootCluster
		default:
			path := logicalcluster.NewPath(exportPath)
			lc, err := o.getLogicalCluster(path)
			if err != nil {
				return forbidden
			}
			exportClusterName = logicalcluster.From(lc)
		}

		if err := o.checkAPIExportAccess(ctx, a, exportClusterName, exportName); err != nil {
			return forbidden
		}
	}

	return nil
}

func (o *workspacetype) checkAPIExportAccess(ctx context.Context, a admission.Attributes, apiExportClusterName logicalcluster.Name, apiExportName string) error {
	logger := klog.FromContext(ctx)
	authz, err := o.createAuthorizer(apiExportClusterName, o.deepSARClient, delegated.Options{})
	if err != nil {
		logger.Error(err, "error creating authorizer from delegating authorizer config")
		return errors.New("unable to authorize request")
	}
	return apibindingadmission.CheckAPIExportAccess(ctx, a.GetUserInfo(), apiExportName, authz)
}

func (o *workspacetype) ValidateInitialization() error {
	if o.deepSARClient == nil {
		return fmt.Errorf(PluginName + " plugin needs a deepSARClient")
	}
	if o.logicalClusterIndexer == nil {
		return fmt.Errorf(PluginName + " plugin needs a LogicalCluster indexer")
	}
	if o.cacheLogicalClusterIndexer == nil {
		return fmt.Errorf(PluginName + " plugin needs a cache LogicalCluster indexer")
	}
	return nil
}

func (o *workspacetype) SetDeepSARClient(client kcpkubernetesclientset.ClusterInterface) {
	o.deepSARClient = client
}

func (o *workspacetype) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	logicalClustersReady := local.Core().V1alpha1().LogicalClusters().Informer().HasSynced
	cacheLogicalClustersReady := global.Core().V1alpha1().LogicalClusters().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return logicalClustersReady() && cacheLogicalClustersReady()
	})
	o.logicalClusterIndexer = local.Core().V1alpha1().LogicalClusters().Informer().GetIndexer()
	o.cacheLogicalClusterIndexer = global.Core().V1alpha1().LogicalClusters().Informer().GetIndexer()

	indexers.AddIfNotPresentOrDie(local.Core().V1alpha1().LogicalClusters().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	indexers.AddIfNotPresentOrDie(global.Core().V1alpha1().LogicalClusters().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
}
