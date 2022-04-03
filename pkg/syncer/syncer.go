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

package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	nscontroller "github.com/kcp-dev/kcp/pkg/reconciler/workload/namespace"
	"github.com/kcp-dev/kcp/pkg/syncer/mutators"
)

const (
	resyncPeriod       = 10 * time.Hour
	SyncerNamespaceKey = "SYNCER_NAMESPACE"
	syncerApplyManager = "syncer"

	// TODO(marun) Coordinate this value with the interval configured for the heartbeat controller
	heartbeatInterval = 20 * time.Second
)

// SyncDirection indicates which direction data is flowing for this particular syncer
type SyncDirection string

// SyncDown indicates a syncer watches resources on KCP and applies the spec to the target cluster
const SyncDown SyncDirection = "down"

// SyncUp indicates a syncer watches resources on the target cluster and applies the status to KCP
const SyncUp SyncDirection = "up"

// TODO(marun) Remove this function and rename startSyncer to StartSyncer once push-mode syncer management goes away.
func StartManagedSyncer(ctx context.Context, upstream, downstream *rest.Config, resources sets.String, kcpClusterName logicalcluster.LogicalCluster, pcluster string, numSyncerThreads int, externalAddress string) error {
	return startSyncer(ctx, upstream, downstream, resources, kcpClusterName, pcluster, numSyncerThreads, externalAddress)
}

func StartSyncer(ctx context.Context, upstream, downstream *rest.Config, resources sets.String, kcpClusterName logicalcluster.LogicalCluster, pcluster string, numSyncerThreads int) error {
	return startSyncer(ctx, upstream, downstream, resources, kcpClusterName, pcluster, numSyncerThreads, "")
}

func startSyncer(ctx context.Context, upstream, downstream *rest.Config, resources sets.String, kcpClusterName logicalcluster.LogicalCluster, pcluster string, numSyncerThreads int, externalAddress string) error {
	specSyncer, err := NewSpecSyncer(upstream, downstream, resources.List(), kcpClusterName, pcluster, externalAddress)
	if err != nil {
		return err
	}
	statusSyncer, err := NewStatusSyncer(downstream, upstream, resources.List(), kcpClusterName, pcluster)
	if err != nil {
		return err
	}
	go specSyncer.Start(ctx, numSyncerThreads)
	go statusSyncer.Start(ctx, numSyncerThreads)

	// TODO(marun) Report pcluster connectivity to kcp
	kcpClusterClient, err := kcpclient.NewClusterForConfig(upstream)
	if err != nil {
		return err
	}
	kcpClient := kcpClusterClient.Cluster(kcpClusterName)
	workloadClustersClient := kcpClient.WorkloadV1alpha1().WorkloadClusters()

	// Attempt to heartbeat every interval
	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		var heartbeatTime time.Time

		// TODO(marun) Figure out a strategy for backoff to avoid a thundering herd problem with lots of syncers

		// Attempt to heartbeat every second until successful. Errors are logged instead of being returned so the
		// poll error can be safely ignored.
		_ = wait.PollImmediateInfiniteWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
			patchBytes := []byte(fmt.Sprintf(`[{"op":"replace","path":"/status/lastSyncerHeartbeatTime","value":%q}]`, time.Now().Format(time.RFC3339)))
			workloadCluster, err := workloadClustersClient.Patch(ctx, pcluster, types.JSONPatchType, patchBytes, metav1.PatchOptions{}, "status")
			if err != nil {
				klog.Errorf("failed to set status.lastSyncerHeartbeatTime for WorkloadCluster %s|%s: %v", kcpClusterName, pcluster, err)
				return false, nil
			}
			heartbeatTime = workloadCluster.Status.LastSyncerHeartbeatTime.Time
			return true, nil
		})

		klog.V(5).Infof("Heartbeat set for WorkloadCluster %s|%s: %s", kcpClusterName, pcluster, heartbeatTime)

	}, heartbeatInterval)

	return nil
}

type mutatorGvrMap map[schema.GroupVersionResource]func(obj *unstructured.Unstructured) error
type UpsertFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace string, unstrob *unstructured.Unstructured) error
type DeleteFunc func(ctx context.Context, gvr schema.GroupVersionResource, namespace, name string) error
type HandlersProvider func(c *Controller, gvr schema.GroupVersionResource) cache.ResourceEventHandlerFuncs

type Controller struct {
	name  string
	queue workqueue.RateLimitingInterface

	fromInformers dynamicinformer.DynamicSharedInformerFactory
	toClient      dynamic.Interface

	upsertFn  UpsertFunc
	deleteFn  DeleteFunc
	direction SyncDirection

	upstreamClusterName logicalcluster.LogicalCluster
	syncerNamespace     string
	mutators            mutatorGvrMap
}

// New returns a new syncer Controller syncing spec from "from" to "to".
func New(kcpClusterName logicalcluster.LogicalCluster, pcluster string, fromDiscovery discovery.DiscoveryInterface, fromClient, toClient dynamic.Interface, direction SyncDirection, syncedResourceTypes []string, pclusterID string, mutators mutatorGvrMap) (*Controller, error) {
	controllerName := string(direction) + "--" + kcpClusterName.String() + "--" + pcluster
	queue := workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "kcp-"+controllerName)

	c := Controller{
		name:                controllerName,
		queue:               queue,
		toClient:            toClient,
		direction:           direction,
		upstreamClusterName: kcpClusterName,
		syncerNamespace:     os.Getenv(SyncerNamespaceKey),
		mutators:            make(mutatorGvrMap),
	}

	if len(mutators) > 0 {
		c.mutators = mutators
	}

	if direction == SyncDown {
		c.upsertFn = c.applyToDownstream
		c.deleteFn = c.deleteFromDownstream
	} else {
		c.upsertFn = c.updateStatusInUpstream
	}

	fromInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(fromClient, resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
		o.LabelSelector = fmt.Sprintf("%s=%s", nscontroller.ClusterLabel, pclusterID)
	})

	// Get all types the upstream API server knows about.
	// TODO: watch this and learn about new types, or forget about old ones.
	gvrstrs, err := getAllGVRs(fromDiscovery, syncedResourceTypes...)
	if err != nil {
		return nil, err
	}
	for _, gvrstr := range gvrstrs {
		gvr, _ := schema.ParseResourceArg(gvrstr)

		fromInformers.ForResource(*gvr).Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) { c.AddToQueue(*gvr, obj) },
			UpdateFunc: func(oldObj, newObj interface{}) {
				if c.direction == SyncDown {
					if !deepEqualApartFromStatus(oldObj, newObj) {
						c.AddToQueue(*gvr, newObj)
					}
				} else {
					if !deepEqualStatus(oldObj, newObj) {
						c.AddToQueue(*gvr, newObj)
					}
				}
			},
			DeleteFunc: func(obj interface{}) { c.AddToQueue(*gvr, obj) },
		})
		klog.InfoS("Set up informer", "direction", c.direction, "clusterName", kcpClusterName, "pcluster", pcluster, "gvr", gvr)
	}

	c.fromInformers = fromInformers

	return &c, nil
}

func contains(ss []string, s string) bool {
	for _, n := range ss {
		if n == s {
			return true
		}
	}
	return false
}

func getAllGVRs(discoveryClient discovery.DiscoveryInterface, resourcesToSync ...string) ([]string, error) {
	toSyncSet := sets.NewString(resourcesToSync...)
	willBeSyncedSet := sets.NewString()
	rs, err := discoveryClient.ServerPreferredResources()
	if err != nil {
		if strings.Contains(err.Error(), "unable to retrieve the complete list of server APIs") {
			// This error may occur when some API resources added from CRDs are not completely ready.
			// We should just retry without a limit on the number of retries in such a case.
			//
			// In fact this might be related to a bug in the changes made on the feature-logical-cluster
			// Kubernetes branch to support legacy schema resources added as CRDs.
			// If this is confirmed, this test will be removed when the CRD bug is fixed.
			return nil, err
		} else {
			return nil, err
		}
	}
	// TODO(jmprusi): Added ServiceAccounts, Configmaps and Secrets to the default syncing, but we should figure out
	//                a way to avoid doing that: https://github.com/kcp-dev/kcp/issues/727
	gvrstrs := []string{"namespaces.v1.", "serviceaccounts.v1.", "configmaps.v1.", "secrets.v1."} // A syncer should always watch namespaces, serviceaccounts, secrets and configmaps.
	for _, r := range rs {
		// v1 -> v1.
		// apps/v1 -> v1.apps
		// tekton.dev/v1beta1 -> v1beta1.tekton.dev
		groupVersion, err := schema.ParseGroupVersion(r.GroupVersion)
		if err != nil {
			klog.Warningf("Unable to parse GroupVersion %s : %v", r.GroupVersion, err)
			continue
		}
		vr := groupVersion.Version + "." + groupVersion.Group
		for _, ai := range r.APIResources {
			var willBeSynced string
			groupResource := schema.GroupResource{
				Group:    groupVersion.Group,
				Resource: ai.Name,
			}

			if toSyncSet.Has(groupResource.String()) {
				willBeSynced = groupResource.String()
			} else if toSyncSet.Has(ai.Name) {
				willBeSynced = ai.Name
			} else {
				// We're not interested in this resource type
				continue
			}
			if strings.Contains(ai.Name, "/") {
				// foo/status, pods/exec, namespace/finalize, etc.
				continue
			}
			if !ai.Namespaced {
				// Ignore cluster-scoped things.
				continue
			}
			if !contains(ai.Verbs, "watch") {
				klog.Infof("resource %s %s is not watchable: %v", vr, ai.Name, ai.Verbs)
				continue
			}
			gvrstrs = append(gvrstrs, fmt.Sprintf("%s.%s", ai.Name, vr))
			willBeSyncedSet.Insert(willBeSynced)
		}
	}

	notFoundResourceTypes := toSyncSet.Difference(willBeSyncedSet)
	if notFoundResourceTypes.Len() != 0 {
		// Some of the API resources expected to be there are still not published by KCP.
		// We should just retry without a limit on the number of retries in such a case,
		// until the corresponding resources are added inside KCP as CRDs and published as API resources.
		return nil, fmt.Errorf("The following resource types were requested to be synced, but were not found in the KCP logical cluster: %v", notFoundResourceTypes.List())
	}
	return gvrstrs, nil
}

type holder struct {
	gvr         schema.GroupVersionResource
	clusterName logicalcluster.LogicalCluster
	namespace   string
	name        string
}

func (c *Controller) AddToQueue(gvr schema.GroupVersionResource, obj interface{}) {
	objToCheck := obj

	tombstone, ok := objToCheck.(cache.DeletedFinalStateUnknown)
	if ok {
		objToCheck = tombstone.Obj
	}

	metaObj, err := meta.Accessor(objToCheck)
	if err != nil {
		klog.Errorf("%s: error getting meta for %T", c.name, obj)
		return
	}

	qualifiedName := metaObj.GetName()
	if len(metaObj.GetNamespace()) > 0 {
		qualifiedName = metaObj.GetNamespace() + "/" + qualifiedName
	}
	if c.direction == SyncDown {
		qualifiedName = metaObj.GetClusterName() + "|" + qualifiedName
	}
	klog.Infof("Syncer %s: adding %s %s to queue", c.name, gvr, qualifiedName)

	c.queue.Add(
		holder{
			gvr:         gvr,
			clusterName: logicalcluster.From(metaObj),
			namespace:   metaObj.GetNamespace(),
			name:        metaObj.GetName(),
		},
	)
}

// Start starts N worker processes processing work items.
func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer runtime.HandleCrash()
	defer c.queue.ShutDown()

	c.fromInformers.Start(ctx.Done())
	c.fromInformers.WaitForCacheSync(ctx.Done())

	klog.InfoS("Starting syncer workers", "controller", c.name)
	defer klog.InfoS("Stopping syncer workers", "controller", c.name)
	for i := 0; i < numThreads; i++ {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

// startWorker processes work items until stopCh is closed.
func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	h := key.(holder)

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, h); err != nil {
		runtime.HandleError(fmt.Errorf("syncer %q failed to sync %q, err: %w", c.name, key, err))
		c.queue.AddRateLimited(key)
		return true
	}

	c.queue.Forget(key)

	return true
}

// NamespaceLocator stores a logical cluster and namespace and is used
// as the source for the mapped namespace name in a physical cluster.
type NamespaceLocator struct {
	LogicalCluster logicalcluster.LogicalCluster `json:"logical-cluster"`
	Namespace      string                        `json:"namespace"`
}

// PhysicalClusterNamespaceName encodes the NamespaceLocator to a new
// namespace name for use on a physical cluster. The encoding is repeatable.
func PhysicalClusterNamespaceName(l NamespaceLocator) (string, error) {
	b, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum224(b)
	return fmt.Sprintf("kcp%x", hash), nil
}

func (c *Controller) process(ctx context.Context, h holder) error {
	klog.V(2).InfoS("Processing", "gvr", h.gvr, "clusterName", h.clusterName, "namespace", h.namespace, "name", h.name)

	if h.namespace == "" {
		// skipping cluster-level objects and those in syncer namespace
		// TODO: why do we watch cluster level objects?!
		return nil
	}

	var (
		// No matter which direction we're going, when we're trying to retrieve a (potentially existing) object,
		// always use its namespace, without any mapping. This is the namespace that came from the event from
		// the shared informer.
		fromNamespace = h.namespace

		// We do need to map the namespace in which we are creating or updating an object
		toNamespace string
	)

	// Determine toNamespace
	if c.direction == SyncDown {
		// Convert the clusterName and namespace to a single string for toNamespace
		l := NamespaceLocator{
			LogicalCluster: h.clusterName,
			Namespace:      h.namespace,
		}

		var err error
		toNamespace, err = PhysicalClusterNamespaceName(l)
		if err != nil {
			klog.Errorf("%s: error hashing namespace: %v", c.name, err)
			return nil
		}
	} else {
		if h.namespace == c.syncerNamespace {
			// skip syncer namespace
			return nil
		}

		nsInformer := c.fromInformers.ForResource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"})

		nsKey := fromNamespace
		if !h.clusterName.Empty() {
			// If our "physical" cluster is a kcp instance (e.g. for testing purposes), it will return resources
			// with metadata.clusterName set, which means their keys are cluster-aware, so we need to do the same here.
			nsKey = clusters.ToClusterAwareKey(h.clusterName, nsKey)
		}

		nsObj, err := nsInformer.Lister().Get(nsKey)
		if err != nil {
			klog.Errorf("%s: error retrieving namespace %q from physical cluster lister: %v", c.name, nsKey, err)
			return err
		}

		nsMeta, ok := nsObj.(metav1.Object)
		if !ok {
			klog.Errorf("%s: namespace %q: expected metav1.Object, got %T", c.name, nsKey, nsObj)
			return nil
		}

		if namespaceLocationAnnotation := nsMeta.GetAnnotations()[namespaceLocatorAnnotation]; namespaceLocationAnnotation != "" {
			var l NamespaceLocator
			if err := json.Unmarshal([]byte(namespaceLocationAnnotation), &l); err != nil {
				klog.Errorf("%s: namespace %q: error decoding annotation: %v", c.name, nsKey, err)
				return nil
			}
			toNamespace = l.Namespace
		} else {
			// this is not our namespace, silently skipping
			return nil
		}
	}

	key := h.name

	if !h.clusterName.Empty() {
		key = clusters.ToClusterAwareKey(h.clusterName, key)
	}

	key = fromNamespace + "/" + key

	obj, exists, err := c.fromInformers.ForResource(h.gvr).Informer().GetIndexer().GetByKey(key)
	if err != nil {
		return err
	}

	if !exists {
		klog.InfoS("Object doesn't exist:", "direction", c.direction, "clusterName", h.clusterName, "namespace", fromNamespace, "name", h.name)
		if c.deleteFn != nil {
			return c.deleteFn(ctx, h.gvr, toNamespace, h.name)
		}
		return nil
	}

	unstrob, isUnstructured := obj.(*unstructured.Unstructured)
	if !isUnstructured {
		return fmt.Errorf("%s: object to synchronize is expected to be Unstructured, but is %T", c.name, obj)
	}

	if c.upsertFn != nil {
		return c.upsertFn(ctx, h.gvr, toNamespace, unstrob)
	}

	return err
}

// transformName changes the object name into the desired one based on the Direction:
// - if the object is a configmap it handles the "kube-root-ca.crt" name mapping
// - if the object is a serviceaccount it handles the "default" name mapping
func transformName(syncedObject *unstructured.Unstructured, direction SyncDirection) {
	configMapGVR := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}
	serviceAccountGVR := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ServiceAccount"}
	secretGVR := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Secret"}

	switch direction {
	case SyncDown:
		if syncedObject.GroupVersionKind() == configMapGVR && syncedObject.GetName() == "kube-root-ca.crt" {
			syncedObject.SetName("kcp-root-ca.crt")
		}
		if syncedObject.GroupVersionKind() == serviceAccountGVR && syncedObject.GetName() == "default" {
			syncedObject.SetName("kcp-default")
		}
		// TODO(jmprusi): We are rewriting the name of the object into a non random one so we can reference it from the deployment transformer
		//                but this means that means than more than one default-token-XXXX object will overwrite the same "kcp-default-token"
		//				  object. This must be fixed.
		if syncedObject.GroupVersionKind() == secretGVR && strings.Contains(syncedObject.GetName(), "default-token-") {
			syncedObject.SetName("kcp-default-token")
		}
	case SyncUp:
		if syncedObject.GroupVersionKind() == configMapGVR && syncedObject.GetName() == "kcp-root-ca.crt" {
			syncedObject.SetName("kube-root-ca.crt")
		}
		if syncedObject.GroupVersionKind() == serviceAccountGVR && syncedObject.GetName() == "kcp-default" {
			syncedObject.SetName("default")
		}
	}
}

func getDefaultMutators(externalURL *url.URL) mutatorGvrMap {
	mutatorsMap := make(mutatorGvrMap)

	deploymentMutator := mutators.NewDeploymentMutator(externalURL)
	secretMutator := mutators.NewSecretMutator()

	mutatorsMap[deploymentMutator.GVR()] = deploymentMutator.Mutate
	mutatorsMap[secretMutator.GVR()] = secretMutator.Mutate
	return mutatorsMap
}
