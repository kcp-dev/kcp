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

	"github.com/kcp-dev/logicalcluster/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
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

	syncTargetName string
	syncTargetUID  types.UID
	dnsNamespace   string // namespace containing all DNS objects
	dnsImage       string
}

func NewDNSProcessor(
	downstreamKubeClient kubernetes.Interface,
	serviceAccountLister listerscorev1.ServiceAccountLister,
	roleLister listersrbacv1.RoleLister,
	roleBindingLister listersrbacv1.RoleBindingLister,
	deploymentLister listersappsv1.DeploymentLister,
	serviceLister listerscorev1.ServiceLister,
	endpointLister listerscorev1.EndpointsLister,
	syncTargetName string,
	syncTargetUID types.UID,
	dnsNamespace string,
	dnsImage string) *DNSProcessor {

	return &DNSProcessor{
		downstreamKubeClient: downstreamKubeClient,
		serviceAccountLister: serviceAccountLister,
		roleLister:           roleLister,
		roleBindingLister:    roleBindingLister,
		deploymentLister:     deploymentLister,
		serviceLister:        serviceLister,
		endpointLister:       endpointLister,
		syncTargetName:       syncTargetName,
		syncTargetUID:        syncTargetUID,
		dnsNamespace:         dnsNamespace,
		dnsImage:             dnsImage,
	}
}

// Process reconciles all DNS objects: it checks they exist and are up-to-date.
func (d *DNSProcessor) Process(ctx context.Context, workspace logicalcluster.Name) error {
	logger := klog.FromContext(ctx)
	logger.WithName("dns")

	dnsID := shared.GetDNSID(workspace, d.syncTargetUID, d.syncTargetName)
	logger.WithValues("name", dnsID, "namespace", d.dnsNamespace)

	logger.Info("checking if all dns objects exist and are up-to-date")
	ctx = klog.NewContext(ctx, logger)

	if err := d.processServiceAccount(ctx, dnsID); err != nil {
		return err
	}
	if err := d.processRole(ctx, dnsID); err != nil {
		return err
	}
	if err := d.processRoleBinding(ctx, dnsID); err != nil {
		return err
	}
	if err := d.processDeployment(ctx, dnsID); err != nil {
		return err
	}
	if err := d.processService(ctx, dnsID); err != nil {
		return err
	}

	// TODO: check endpoints. It's disabled until we figure out how to update e2e tests.
	//endpoints, err := d.endpointLister.Endpoints(d.dnsNamespace).Get(dnsID)
	//if err != nil {
	//	return err
	//}
	//
	//if hasAtLeastOneReadyAddress(endpoints) {
	//	return nil
	//}

	return nil
}

func (d *DNSProcessor) processServiceAccount(ctx context.Context, name string) error {
	logger := klog.FromContext(ctx)

	expected := MakeServiceAccount(name, d.dnsNamespace)
	_, err := d.serviceAccountLister.ServiceAccounts(d.dnsNamespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, err := d.downstreamKubeClient.CoreV1().ServiceAccounts(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
			if err != nil {
				logger.Error(err, "failed to create ServiceAccount (retrying)")
				return err // retry
			}
			logger.Info("ServiceAccount created")
		}
		logger.Error(err, "failed to get ServiceAccount (retrying)")
		return err
	}

	// TODO: check object has the expected content (eg. after an upgrade)

	return nil
}

func (d *DNSProcessor) processRole(ctx context.Context, name string) error {
	logger := klog.FromContext(ctx)

	expected := MakeRole(name, d.dnsNamespace)
	_, err := d.roleLister.Roles(d.dnsNamespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, err := d.downstreamKubeClient.RbacV1().Roles(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
			if err != nil {
				logger.Error(err, "failed to create Role (retrying)")
				return err // retry
			}
			logger.Info("Role created")
		}
		logger.Error(err, "failed to get Role (retrying)")
		return err
	}

	// TODO: check object has the expected content (eg. after an upgrade)

	return nil
}

func (d *DNSProcessor) processRoleBinding(ctx context.Context, name string) error {
	logger := klog.FromContext(ctx)

	expected := MakeRoleBinding(name, d.dnsNamespace)
	_, err := d.roleBindingLister.RoleBindings(d.dnsNamespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, err := d.downstreamKubeClient.RbacV1().RoleBindings(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
			if err != nil {
				logger.Error(err, "failed to create RoleBinding (retrying)")
				return err // retry
			}
			logger.Info("RoleBinding created")
		}
		logger.Error(err, "failed to get RoleBinding (retrying)")
		return err
	}

	// TODO: check object has the expected content (eg. after an upgrade)

	return nil
}

func (d *DNSProcessor) processDeployment(ctx context.Context, name string) error {
	logger := klog.FromContext(ctx)

	expected := MakeDeployment(name, d.dnsNamespace, d.dnsImage)
	_, err := d.deploymentLister.Deployments(d.dnsNamespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, err := d.downstreamKubeClient.AppsV1().Deployments(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
			if err != nil {
				logger.Error(err, "failed to create Deployment (retrying)")
				return err // retry
			}
			logger.Info("Deployment created")
		}
		logger.Error(err, "failed to get Deployment (retrying)")
		return err
	}

	// TODO: check object has the expected content (eg. after an upgrade)

	return nil
}

func (d *DNSProcessor) processService(ctx context.Context, name string) error {
	logger := klog.FromContext(ctx)

	expected := MakeService(name, d.dnsNamespace)
	_, err := d.serviceLister.Services(d.dnsNamespace).Get(name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, err := d.downstreamKubeClient.CoreV1().Services(d.dnsNamespace).Create(ctx, expected, metav1.CreateOptions{})
			if err != nil {
				logger.Error(err, "failed to create Service (retrying)")
				return err // retry
			}
			logger.Info("Service created")
		}
		logger.Error(err, "failed to get Service (retrying)")
		return err
	}

	// TODO: check object has the expected content (eg. after an upgrade)

	return nil
}

//
//func hasAtLeastOneReadyAddress(endpoints *corev1.Endpoints) bool {
//	for _, s := range endpoints.Subsets {
//		if len(s.Addresses) > 0 {
//			return true
//		}
//	}
//	return false
//}
