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

package validatingwebhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/configuration"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/generic"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/validating"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/indexers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const (
	PluginName = "apis.kcp.io/ValidatingWebhook"
)

type Plugin struct {
	*admission.Handler
	config []byte

	// Injected/set via initializers
	kubeClusterClient               kcpkubernetesclientset.ClusterInterface
	localKubeSharedInformerFactory  kcpkubernetesinformers.SharedInformerFactory
	globalKubeSharedInformerFactory kcpkubernetesinformers.SharedInformerFactory

	getAPIBinding     func(clusterName logicalcluster.Name, gr schema.GroupResource) (*apisv1alpha1.APIBinding, error)
	getLogicalCluster func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)

	lock  sync.RWMutex
	cache map[logicalcluster.Name]map[logicalcluster.Name]clusterCache // by request and hook source cluster.
}

type clusterCache struct {
	source generic.Source
	plugin *validating.Plugin
}

var (
	_ = admission.ValidationInterface(&Plugin{})
	_ = admission.InitializationValidator(&Plugin{})
	_ = kcpinitializers.WantsKubeClusterClient(&Plugin{})
	_ = kcpinitializers.WantsKubeInformers(&Plugin{})
	_ = kcpinitializers.WantsKcpInformers(&Plugin{})
)

func NewValidatingAdmissionWebhook(configFile io.Reader) (*Plugin, error) {
	p := &Plugin{
		cache:   make(map[logicalcluster.Name]map[logicalcluster.Name]clusterCache),
		Handler: admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update),
	}
	if configFile != nil {
		config, err := io.ReadAll(configFile)
		if err != nil {
			return nil, err
		}
		p.config = config
	}

	return p, nil
}

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(configFile io.Reader) (admission.Interface, error) {
		return NewValidatingAdmissionWebhook(configFile)
	})
}

func (p *Plugin) Validate(ctx context.Context, attr admission.Attributes, o admission.ObjectInterfaces) error {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return err
	}
	clusterName := cluster.Name

	if attr.GetResource().GroupResource() == corev1alpha1.Resource("logicalclusters") {
		// logical clusters are excluded from admission webhooks
		return nil
	}

	plugin, err := p.getPlugin(clusterName, attr)
	if err != nil {
		return err
	}

	// Add cluster annotation on create
	if attr.GetOperation() == admission.Create {
		u, ok := attr.GetObject().(metav1.Object)
		if !ok {
			return fmt.Errorf("unexpected type %T", attr.GetObject())
		}
		if undo := SetClusterAnnotation(u, clusterName); undo != nil {
			defer undo()
		}
	}

	return plugin.Validate(ctx, attr, o)
}

func (p *Plugin) getPlugin(clusterName logicalcluster.Name, attr admission.Attributes) (*validating.Plugin, error) {
	var config io.Reader
	if len(p.config) > 0 {
		config = bytes.NewReader(p.config)
	}

	// get the APIBinding for the resource, or nil for local resources
	gr := attr.GetResource().GroupResource()
	binding, err := p.getAPIBinding(clusterName, gr)
	if err != nil && !kerrors.IsNotFound(err) {
		return nil, fmt.Errorf("error getting APIBinding for %q: %w", gr, err)
	}
	sourceClusterName := clusterName
	if binding != nil {
		sourceClusterName = logicalcluster.Name(binding.Status.APIExportClusterName)
	}

	// fast path
	p.lock.RLock()
	c, ok := p.cache[clusterName][sourceClusterName]
	p.lock.RUnlock()
	if ok {
		return c.plugin, nil
	}

	// slow path
	p.lock.Lock()
	defer p.lock.Unlock()
	c, ok = p.cache[clusterName][sourceClusterName]
	if ok {
		return c.plugin, nil
	}

	// double check that the logical cluster is still alive
	if _, err := p.getLogicalCluster(clusterName); err != nil {
		p.lock.Unlock()
		return nil, fmt.Errorf("error getting LogicalCluster %q: %w", clusterName, err)
	}

	// create new plugin for this logical cluster and source cluster
	source := configuration.NewValidatingWebhookConfigurationManagerForInformer(
		// TODO(sttts): fix supporting local admission webhooks for bound resources as well
		p.globalKubeSharedInformerFactory.Admissionregistration().V1().ValidatingWebhookConfigurations().Cluster(sourceClusterName),
	)
	plugin, err := validating.NewValidatingAdmissionWebhook(config)
	if err != nil {
		return nil, fmt.Errorf("error creating mutaing admission webhook: %w", err)
	}
	plugin.SetExternalKubeClientSet(p.kubeClusterClient.Cluster(clusterName.Path()))
	plugin.SetNamespaceInformer(p.localKubeSharedInformerFactory.Core().V1().Namespaces().Cluster(clusterName))
	plugin.SetHookSource(source)
	plugin.SetReadyFuncFromKCP(p.localKubeSharedInformerFactory.Core().V1().Namespaces().Cluster(clusterName))

	if err := plugin.ValidateInitialization(); err != nil {
		return nil, fmt.Errorf("error mutaing ValidatingAdmissionWebhook initialization: %w", err)
	}

	// store in cache
	c = clusterCache{
		source: source,
		plugin: plugin,
	}
	if _, ok := p.cache[clusterName]; !ok {
		p.cache[clusterName] = map[logicalcluster.Name]clusterCache{}
	}
	p.cache[clusterName][sourceClusterName] = c

	return c.plugin, nil
}

func (p *Plugin) ValidateInitialization() error {
	if p.kubeClusterClient == nil {
		return errors.New("missing kubeClusterClient")
	}
	if p.localKubeSharedInformerFactory == nil {
		return errors.New("missing localKubeSharedInformerFactory")
	}
	if p.globalKubeSharedInformerFactory == nil {
		return errors.New("missing globalKubeSharedInformerFactory")
	}
	return nil
}

func (p *Plugin) SetKubeClusterClient(client kcpkubernetesclientset.ClusterInterface) {
	p.kubeClusterClient = client
}

func (p *Plugin) SetKubeInformers(local, global kcpkubernetesinformers.SharedInformerFactory) {
	p.localKubeSharedInformerFactory = local
	p.globalKubeSharedInformerFactory = global
}

func (p *Plugin) SetKcpInformers(local, _ kcpinformers.SharedInformerFactory) {
	// watch APIBindings
	_ = local.Apis().V1alpha1().APIBindings().Informer().AddIndexers(cache.Indexers{
		indexers.APIBindingByBoundResources:    indexers.IndexAPIBindingByBoundResources,
		indexers.APIBindingsByAPIExportCluster: indexers.IndexAPIBindingsByAPIExportCluster,
	}) // ignore conflict
	p.getAPIBinding = func(clusterName logicalcluster.Name, gr schema.GroupResource) (*apisv1alpha1.APIBinding, error) {
		key := indexers.APIBindingBoundResourceValue(clusterName, gr.Resource, gr.Group)
		objs, err := local.Apis().V1alpha1().APIBindings().Informer().GetIndexer().ByIndex(indexers.APIBindingByBoundResources, key)
		if err != nil {
			return nil, fmt.Errorf("error getting APIBindings by bound resources: %w", err)
		}
		switch len(objs) {
		case 0:
			return nil, kerrors.NewNotFound(apisv1alpha1.Resource("APIBinding"), key)
		case 1:
			return objs[0].(*apisv1alpha1.APIBinding), nil
		default:
			// should never happen
			return nil, fmt.Errorf("found multiple APIBindings for bound resources %q", key)
		}
	}
	_, err := local.Apis().V1alpha1().APIBindings().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			p.lock.Lock()
			defer p.lock.Unlock()

			// delete if there is no other binding from the same logical cluster
			binding := obj.(*apisv1alpha1.APIBinding)
			key := indexers.APIBindingByBoundResourceValue(logicalcluster.From(binding), logicalcluster.Name(binding.Status.APIExportClusterName))
			objs, err := local.Apis().V1alpha1().APIBindings().Informer().GetIndexer().ByIndex(indexers.APIBindingsByAPIExportCluster, key)
			if err != nil {
				runtime.HandleError(fmt.Errorf("error getting APIBindings by APIExportCluster: %w", err))
				return
			}
			foundOther := false
			for _, obj := range objs {
				otherBinding := obj.(*apisv1alpha1.APIBinding)
				if otherBinding.Name != binding.Name {
					foundOther = true
					break
				}
			}
			if !foundOther {
				delete(p.cache[logicalcluster.From(binding)], logicalcluster.Name(binding.Status.APIExportClusterName))
			}
		},
	})
	if err != nil {
		runtime.HandleError(fmt.Errorf("error adding APIBinding delete event handler: %w", err))
	}

	// watch logical clusters
	p.getLogicalCluster = func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
		return local.Core().V1alpha1().LogicalClusters().Lister().Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
	}
	_, err = local.Core().V1alpha1().LogicalClusters().Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			clusterName := logicalcluster.From(obj.(*corev1alpha1.LogicalCluster))
			p.lock.Lock()
			defer p.lock.Unlock()
			delete(p.cache, clusterName)
		},
	})
	if err != nil {
		runtime.HandleError(fmt.Errorf("error adding LogicalCluster delete event handler: %w", err))
	}
}

// SetClusterAnnotation sets the cluster annotation on the given object to the given clusterName,
// returning an undo function that can be used to revert the change.
func SetClusterAnnotation(obj metav1.Object, clusterName logicalcluster.Name) (undoFn func()) {
	anns := obj.GetAnnotations()
	if anns == nil {
		obj.SetAnnotations(map[string]string{logicalcluster.AnnotationKey: clusterName.String()})
		return func() { obj.SetAnnotations(nil) }
	}

	old, ok := anns[logicalcluster.AnnotationKey]
	if old == clusterName.String() {
		return nil
	}

	anns[logicalcluster.AnnotationKey] = clusterName.String()
	obj.SetAnnotations(anns)
	if ok {
		return func() {
			anns[logicalcluster.AnnotationKey] = old
			obj.SetAnnotations(anns)
		}
	}
	return func() {
		delete(anns, logicalcluster.AnnotationKey)
		obj.SetAnnotations(anns)
	}
}
