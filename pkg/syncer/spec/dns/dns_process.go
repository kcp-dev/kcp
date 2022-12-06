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

package dns

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/kcp-dev/logicalcluster/v3"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubernetesinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	listersnetworkingv1 "k8s.io/client-go/listers/networking/v1"
	listersrbacv1 "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

type DNSProcessor struct {
	downstreamKubeClient kubernetes.Interface

	serviceAccountLister listerscorev1.ServiceAccountLister
	roleLister           listersrbacv1.RoleLister
	roleBindingLister    listersrbacv1.RoleBindingLister
	deploymentLister     listersappsv1.DeploymentLister
	serviceLister        listerscorev1.ServiceLister
	endpointLister       listerscorev1.EndpointsLister
	networkPolicyLister  listersnetworkingv1.NetworkPolicyLister

	syncTargetUID  types.UID
	syncTargetName string
	dnsNamespace   string // namespace containing all DNS objects
	dnsImage       string

	initialized      sync.Map
	initializationMu sync.RWMutex
}

func NewDNSProcessor(
	downstreamKubeClient kubernetes.Interface,
	syncerNamespaceInformerFactory kubernetesinformers.SharedInformerFactory,
	syncTargetName string,
	syncTargetUID types.UID,
	dnsNamespace string,
	dnsImage string) *DNSProcessor {
	return &DNSProcessor{
		downstreamKubeClient: downstreamKubeClient,
		serviceAccountLister: syncerNamespaceInformerFactory.Core().V1().ServiceAccounts().Lister(),
		roleLister:           syncerNamespaceInformerFactory.Rbac().V1().Roles().Lister(),
		roleBindingLister:    syncerNamespaceInformerFactory.Rbac().V1().RoleBindings().Lister(),
		deploymentLister:     syncerNamespaceInformerFactory.Apps().V1().Deployments().Lister(),
		serviceLister:        syncerNamespaceInformerFactory.Core().V1().Services().Lister(),
		endpointLister:       syncerNamespaceInformerFactory.Core().V1().Endpoints().Lister(),
		networkPolicyLister:  syncerNamespaceInformerFactory.Networking().V1().NetworkPolicies().Lister(),
		syncTargetName:       syncTargetName,
		syncTargetUID:        syncTargetUID,
		dnsNamespace:         dnsNamespace,
		dnsImage:             dnsImage,
	}
}

func (d *DNSProcessor) ServiceLister() listerscorev1.ServiceLister {
	return d.serviceLister
}

// EnsureDNSUpAndReady creates all DNS-related resources if necessary.
// It also checks that the DNS Deployment for this workspace
// are effectively reachable through the Service.
// It returns true if the DNS is setup and reachable, and returns an error if there was an error
// during the check or creation of the DNS-related resources.
func (d *DNSProcessor) EnsureDNSUpAndReady(ctx context.Context, tenantID string, clusterName logicalcluster.Name) (bool, error) {
	logger := klog.FromContext(ctx)
	logger = logger.WithName("dns")

	dnsID := shared.GetDNSID(clusterName, d.syncTargetUID, d.syncTargetName)
	logger = logger.WithValues("name", dnsID, "namespace", d.dnsNamespace)

	logger.V(4).Info("checking if all dns objects exist and are up-to-date")
	ctx = klog.NewContext(ctx, logger)

	// Try updating resources if not done already
	if initialized, ok := d.initialized.Load(dnsID); !ok || !initialized.(bool) {
		updated, err := d.lockMayUpdate(ctx, dnsID, tenantID)
		if updated {
			return false, err
		}
	}

	// Get the expected Endpoints resource
	endpoints, err := d.endpointLister.Endpoints(d.dnsNamespace).Get(dnsID)
	if err == nil {
		// DNS is ready if the Endpoints resource has at least one ready address
		return hasAtLeastOneReadyAddress(endpoints), nil
	}

	if !apierrors.IsNotFound(err) {
		return false, err
	}

	// No Endpoints resource was found: try to create all the DNS-related resources
	if err := d.processServiceAccount(ctx, dnsID, tenantID); err != nil {
		return false, err
	}
	if err := d.processRole(ctx, dnsID, tenantID); err != nil {
		return false, err
	}
	if err := d.processRoleBinding(ctx, dnsID, tenantID); err != nil {
		return false, err
	}
	if err := d.processDeployment(ctx, dnsID, tenantID); err != nil {
		return false, err
	}
	if err := d.processService(ctx, dnsID, tenantID); err != nil {
		return false, err
	}
	if err := d.processNetworkPolicy(ctx, dnsID, tenantID); err != nil {
		return false, err
	}

	// Since the Endpoints resource was not found, the DNS is not yet ready,
	// even though all the required resources have been created
	// (deployment still needs to start).
	return false, nil
}

func (d *DNSProcessor) processServiceAccount(ctx context.Context, name, tenantID string) error {
	logger := klog.FromContext(ctx)

	_, err := d.serviceAccountLister.ServiceAccounts(d.dnsNamespace).Get(name)
	if apierrors.IsNotFound(err) {
		expected := MakeServiceAccount(name, d.dnsNamespace, tenantID)
		_, err = d.downstreamKubeClient.CoreV1().ServiceAccounts(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
		if err == nil {
			logger.Info("ServiceAccount created")
		}
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.Error(err, "failed to get ServiceAccount (retrying)")
		return err
	}

	return nil
}

func (d *DNSProcessor) processRole(ctx context.Context, name, tenantID string) error {
	logger := klog.FromContext(ctx)

	_, err := d.roleLister.Roles(d.dnsNamespace).Get(name)
	if apierrors.IsNotFound(err) {
		expected := MakeRole(name, d.dnsNamespace, tenantID)
		_, err = d.downstreamKubeClient.RbacV1().Roles(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
		if err == nil {
			logger.Info("Role created")
		}
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.Error(err, "failed to get Role (retrying)")
		return err
	}

	return nil
}

func (d *DNSProcessor) processRoleBinding(ctx context.Context, name, tenantID string) error {
	logger := klog.FromContext(ctx)

	_, err := d.roleBindingLister.RoleBindings(d.dnsNamespace).Get(name)
	if apierrors.IsNotFound(err) {
		expected := MakeRoleBinding(name, d.dnsNamespace, tenantID)
		_, err = d.downstreamKubeClient.RbacV1().RoleBindings(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
		if err == nil {
			logger.Info("RoleBinding created")
		}
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.Error(err, "failed to get RoleBinding (retrying)")
		return err
	}

	return nil
}

func (d *DNSProcessor) processDeployment(ctx context.Context, name, tenantID string) error {
	logger := klog.FromContext(ctx)

	_, err := d.deploymentLister.Deployments(d.dnsNamespace).Get(name)
	if apierrors.IsNotFound(err) {
		expected := MakeDeployment(name, d.dnsNamespace, tenantID, d.dnsImage)
		_, err = d.downstreamKubeClient.AppsV1().Deployments(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
		if err == nil {
			logger.Info("Deployment created")
		}
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.Error(err, "failed to get Deployment (retrying)")
		return err
	}

	return nil
}

func (d *DNSProcessor) processService(ctx context.Context, name, tenantID string) error {
	logger := klog.FromContext(ctx)

	_, err := d.serviceLister.Services(d.dnsNamespace).Get(name)
	if apierrors.IsNotFound(err) {
		expected := MakeService(name, d.dnsNamespace, tenantID)
		_, err = d.downstreamKubeClient.CoreV1().Services(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
		if err == nil {
			logger.Info("Service created")
		}
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.Error(err, "failed to get Service (retrying)")
		return err
	}

	return nil
}

func (d *DNSProcessor) processNetworkPolicy(ctx context.Context, name, tenantID string) error {
	logger := klog.FromContext(ctx)

	var kubeEndpoints *corev1.Endpoints
	_, err := d.networkPolicyLister.NetworkPolicies(d.dnsNamespace).Get(name)
	if apierrors.IsNotFound(err) {
		kubeEndpoints, err = d.downstreamKubeClient.CoreV1().Endpoints("default").Get(ctx, "kubernetes", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if len(kubeEndpoints.Subsets) == 0 || len(kubeEndpoints.Subsets[0].Addresses) == 0 {
			return errors.New("missing kubernetes API endpoints")
		}

		expected := MakeNetworkPolicy(name, d.dnsNamespace, tenantID, &kubeEndpoints.Subsets[0])
		_, err = d.downstreamKubeClient.NetworkingV1().NetworkPolicies(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
		if err == nil {
			logger.Info("NetworkPolicy created")
		}
	}
	if err != nil && !apierrors.IsAlreadyExists(err) {
		logger.Error(err, "failed to get NetworkPolicy (retrying)")
		return err
	}

	return nil
}

func hasAtLeastOneReadyAddress(endpoints *corev1.Endpoints) bool {
	for _, s := range endpoints.Subsets {
		if len(s.Addresses) > 0 && s.Addresses[0].IP != "" {
			return true
		}
	}
	return false
}

// lockMayUpdate guarantees mayUpdate is run in a critical section.
// It returns true when the DNS deployment has been updated.
func (d *DNSProcessor) lockMayUpdate(ctx context.Context, dnsID, tenantID string) (bool, error) {
	d.initializationMu.Lock()
	defer d.initializationMu.Unlock()

	// initialized may have been modified outside the critical section so checking again here
	if initialized, ok := d.initialized.Load(dnsID); !ok || !initialized.(bool) {
		updated, err := d.mayUpdate(ctx, dnsID, tenantID)

		if err != nil {
			return true, err
		}

		d.initialized.Store(dnsID, true)

		if updated {
			// The endpoint might temporarily be without ready addresses, depending on the
			// deployment strategy. Anyhow, gives some time for the system to stabilize

			return true, nil
		}
	}
	return false, nil
}

func (d *DNSProcessor) mayUpdate(ctx context.Context, name, tenantID string) (bool, error) {
	deployment, err := d.deploymentLister.Deployments(d.dnsNamespace).Get(name)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	deployment = deployment.DeepCopy()
	needsUpdate := false
	c := findContainer(deployment, "kcp-dns")

	if c == nil {
		// corrupted deployment. Trying to recover
		expected := MakeDeployment(name, d.dnsNamespace, tenantID, d.dnsImage)
		deployment.Spec = expected.Spec
		needsUpdate = true
	} else if c.Image != d.dnsImage {
		c.Image = d.dnsImage
		needsUpdate = true
	}

	if !needsUpdate {
		return false, nil
	}

	logger := klog.FromContext(ctx)

	_, err = d.downstreamKubeClient.AppsV1().Deployments(d.dnsNamespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		logger.Error(err, "failed to update Deployment (retrying)")
		return false, err
	}

	logger.Info("Deployment updated")
	return true, nil
}

func findContainer(deployment *appsv1.Deployment, name string) *corev1.Container {
	containers := deployment.Spec.Template.Spec.Containers

	for i := 0; i < len(containers); i++ {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

func (d *DNSProcessor) CleanupTenant(ctx context.Context, tenantID string) error {
	logger := klog.FromContext(ctx)
	logger.WithName("dns")
	logger.WithValues("tenantID", tenantID)

	logger.V(2).Info("Cleaning DNS-related resources for KCP tenant")
	if services, err := d.downstreamKubeClient.CoreV1().Services(d.dnsNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", shared.TenantIDLabel, tenantID),
	}); err != nil {
		return err
	} else {
		for _, service := range services.Items {
			if err := d.downstreamKubeClient.CoreV1().Services(d.dnsNamespace).Delete(ctx, service.Name, metav1.DeleteOptions{}); err != nil {
				return err
			}
		}
	}
	if err := d.downstreamKubeClient.AppsV1().Deployments(d.dnsNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", shared.TenantIDLabel, tenantID),
	}); err != nil {
		return err
	}
	if err := d.downstreamKubeClient.CoreV1().ServiceAccounts(d.dnsNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", shared.TenantIDLabel, tenantID),
	}); err != nil {
		return err
	}
	if err := d.downstreamKubeClient.RbacV1().Roles(d.dnsNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", shared.TenantIDLabel, tenantID),
	}); err != nil {
		return err
	}
	return d.downstreamKubeClient.RbacV1().RoleBindings(d.dnsNamespace).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", shared.TenantIDLabel, tenantID),
	})
}
