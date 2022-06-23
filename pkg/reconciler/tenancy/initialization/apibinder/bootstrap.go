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

package apibinder

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/equality"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	initializationutil "github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpscheme "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/scheme"
	kcpinformer "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	initializationbootstrap "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/apis/initialization"
	initializationclient "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/client/clientset/versioned"
	initializationinformer "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/client/informers/externalversions"
)

// Bootstrap installs the apis that drive the apibinder initializer and starts the
// controller once they're ready.
func Bootstrap(ctx context.Context, cfg *rest.Config, installInto logicalcluster.Name) error {
	klog.Infof("Beginning APISet Initializer bootstrapping in cluster %q", installInto)
	gr := metav1.GroupResource{
		Group:    initialization.GroupName,
		Resource: "apisets",
	}
	resourceSchema, err := initializationbootstrap.APIResourceSchema(gr)
	if err != nil {
		return fmt.Errorf("could not determine APIResourceSchema: %w", err)
	}

	scopedCfg := rest.AddUserAgent(rest.CopyConfig(cfg), "kcp-apibinder-initializer-bootstrapper")
	kcpClusterClient, err := kcpclient.NewClusterForConfig(scopedCfg)
	if err != nil {
		return fmt.Errorf("could not create KCP cluster client: %w", err)
	}

	kcpClient := kcpClusterClient.Cluster(installInto)
	apisClient := kcpClient.ApisV1alpha1()
	resourceSchema, err = apisClient.APIResourceSchemas().Create(ctx, resourceSchema, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("could not create APIResourceSchema: %w", err)
	}
	klog.Infof("Created APIResourceSchema %q|%q", installInto.String(), resourceSchema.Name)

	export, err := apisClient.APIExports().Create(ctx, &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: gr.String(),
		},
		Spec: apisv1alpha1.APIExportSpec{
			LatestResourceSchemas: []string{resourceSchema.Name},
		},
	}, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("could not create APIExport: %w", err)
	}
	klog.Infof("Created APIExport %q|%q", installInto.String(), export.Name)

	if err := wait.ExponentialBackoffWithContext(ctx, wait.Backoff{
		Duration: 10 * time.Millisecond,
		Factor:   2,
		Steps:    10,
	}, func() (done bool, err error) {
		export, err = apisClient.APIExports().Get(ctx, export.Name, metav1.GetOptions{})
		if err != nil {
			klog.Infof("Error waiting for APIExport %q|%q virtual workspace URLs to be ready: %v", installInto.String(), export.Name, err)
			return false, err
		}
		return checkConditionTrueWhileLogging(export, apisv1alpha1.APIExportVirtualWorkspaceURLsReady, fmt.Sprintf("Not done waiting for APIExport %q|%q virtual workspace URLs to be ready", installInto.String(), export.Name)), nil
	}); err != nil {
		return fmt.Errorf("could not wait for virtual workspace URLs to be ready on APIExport: %w", err)
	}
	klog.Infof("APIExport %q|%q virtual workspace URLs are ready", installInto.String(), export.Name)

	binding, err := apisClient.APIBindings().Create(ctx, &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: gr.String(),
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.ExportReference{
				Workspace: &apisv1alpha1.WorkspaceExportReference{
					Path:       logicalcluster.From(export).String(),
					ExportName: export.Name,
				},
			},
		},
	}, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("could not create APIBinding: %w", err)
	}
	klog.Infof("Created APIBinding %q|%q", installInto.String(), binding.Name)

	if err := wait.ExponentialBackoffWithContext(ctx, wait.Backoff{
		Duration: 10 * time.Millisecond,
		Factor:   2,
		Steps:    10,
	}, func() (done bool, err error) {
		binding, err = apisClient.APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		if err != nil {
			klog.Infof("Error waiting for APIBinding %q|%q to be bound: %v", installInto.String(), binding.Name, err)
			return false, err
		}
		return checkConditionTrueWhileLogging(binding, apisv1alpha1.InitialBindingCompleted, fmt.Sprintf("Not done waiting for APIBinding %q|%q to be bound", installInto.String(), binding.Name)), nil
	}); err != nil {
		return fmt.Errorf("could not wait for APIBinding to be bound: %w", err)
	}
	klog.Infof("APIBinding %q|%q is bound", installInto.String(), binding.Name)

	cwt, err := clusterWorkspaceType()
	if err != nil {
		return fmt.Errorf("could not resolve ClusterWorkspaceType: %w", err)
	}

	tenancyClient := kcpClient.TenancyV1alpha1()
	cwt, err = tenancyClient.ClusterWorkspaceTypes().Create(ctx, cwt, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("could not create ClusterWorkspaceType: %w", err)
	}
	klog.Infof("Created ClusterWorkspaceType %q|%q", installInto.String(), cwt.Name)

	if err := wait.ExponentialBackoffWithContext(ctx, wait.Backoff{
		Duration: 10 * time.Millisecond,
		Factor:   2,
		Steps:    10,
	}, func() (done bool, err error) {
		cwt, err = tenancyClient.ClusterWorkspaceTypes().Get(ctx, cwt.Name, metav1.GetOptions{})
		if err != nil {
			klog.Infof("Error waiting for ClusterWorkspaceType %q|%q virtual workspace URLs to be ready: %v", installInto.String(), cwt.Name, err)
			return false, err
		}
		return checkConditionTrueWhileLogging(cwt, tenancyv1alpha1.ClusterWorkspaceTypeVirtualWorkspaceURLsReady, fmt.Sprintf("Not done waiting for ClusterWorkspaceType %q|%q virtual workspace URLs to be ready", installInto.String(), cwt.Name)), nil
	}); err != nil {
		return fmt.Errorf("could not wait for virtual workspace URLs to be ready on ClusterWorkspaceType: %w", err)
	}
	klog.Infof("ClusterWorkspaceType %q|%q virtual workspace URLs are ready", installInto.String(), cwt.Name)

	initializerControllerCfg := rest.AddUserAgent(rest.CopyConfig(cfg), "kcp-apibinder-initializer-bootstrapper-controller")

	initializingWorkspacesCfg := rest.CopyConfig(initializerControllerCfg)
	initializingWorkspacesCfg.Host = cwt.Status.VirtualWorkspaces[0].URL
	initializingWorkspacesKcpClusterClient, err := kcpclient.NewClusterForConfig(initializingWorkspacesCfg)
	if err != nil {
		return err
	}
	initializingWorkspacesKcpInformers := kcpinformer.NewSharedInformerFactory(initializingWorkspacesKcpClusterClient.Cluster(logicalcluster.Wildcard), 10*time.Hour)

	apiExportCfg := rest.CopyConfig(initializerControllerCfg)
	apiExportCfg.Host = export.Status.VirtualWorkspaces[0].URL
	apiExportInitializationClusterClient, err := initializationclient.NewClusterForConfig(apiExportCfg)
	if err != nil {
		return err
	}
	apiExportInitializationInformers := initializationinformer.NewSharedInformerFactory(apiExportInitializationClusterClient.Cluster(logicalcluster.Wildcard), 10*time.Hour)

	initializerController, err := NewAPIBinder(
		initializationutil.InitializerForType(cwt),
		initializingWorkspacesKcpClusterClient,
		initializingWorkspacesKcpInformers.Tenancy().V1alpha1().ClusterWorkspaces(),
		apiExportInitializationInformers.Initialization().V1alpha1().APISets(),
	)
	if err != nil {
		return err
	}

	kcpInformers := kcpinformer.NewSharedInformerFactory(kcpClusterClient.Cluster(logicalcluster.Wildcard), 10*time.Hour)

	apiSetController, err := NewAPISetValidator(
		apiExportInitializationClusterClient,
		apiExportInitializationInformers.Initialization().V1alpha1().APISets(),
		kcpInformers.Apis().V1alpha1().APIExports(),
	)
	if err != nil {
		return err
	}

	stopCh := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(stopCh)
	}()
	initializingWorkspacesKcpInformers.Start(stopCh)
	apiExportInitializationInformers.Start(stopCh)
	kcpInformers.Start(stopCh)

	for name, informer := range map[string]cache.SharedIndexInformer{
		"clusterworkspaces": initializingWorkspacesKcpInformers.Tenancy().V1alpha1().ClusterWorkspaces().Informer(),
		"apisets":           apiExportInitializationInformers.Initialization().V1alpha1().APISets().Informer(),
		"apiexports":        kcpInformers.Apis().V1alpha1().APIExports().Informer(),
	} {
		if !cache.WaitForNamedCacheSync(name, stopCh, informer.HasSynced) {
			return errors.New("informer not synced")
		}
	}

	go initializerController.Start(ctx, 10)
	go apiSetController.Start(ctx, 2)

	return nil
}

//go:embed clusterworkspacetype.yaml
var raw []byte

func clusterWorkspaceType() (*tenancyv1alpha1.ClusterWorkspaceType, error) {
	expectedGvk := &schema.GroupVersionKind{Group: tenancy.GroupName, Version: "v1alpha1", Kind: "ClusterWorkspaceType"}
	obj, gvk, err := kcpscheme.Codecs.UniversalDeserializer().Decode(raw, expectedGvk, &tenancyv1alpha1.ClusterWorkspaceType{})
	if err != nil {
		return nil, fmt.Errorf("could not decode raw ClusterWorkspaceType: %w", err)
	}

	if !equality.Semantic.DeepEqual(gvk, expectedGvk) {
		return nil, fmt.Errorf("decoded ClusterWorkspaceType into incorrect GroupVersionKind, got %#v, wanted %#v", gvk, expectedGvk)
	}

	cwt, ok := obj.(*tenancyv1alpha1.ClusterWorkspaceType)
	if !ok {
		return nil, fmt.Errorf("decoded ClusterWorkspaceType into incorrect type, got %T, wanted %T", cwt, &tenancyv1alpha1.ClusterWorkspaceType{})
	}

	return cwt, nil
}

func checkConditionTrueWhileLogging(from conditions.Getter, conditionType conditionsv1alpha1.ConditionType, logPreamble string) bool {
	done := conditions.IsTrue(from, conditionType)
	if !done {
		condition := conditions.Get(from, conditionType)
		if condition != nil {
			klog.Infof("%s: %s: %s", logPreamble, condition.Reason, condition.Message)
		} else {
			klog.Infof("%s: no condition present", logPreamble)
		}
	}
	return done
}
