/*
Copyright 2022 The Kube Bind Authors.

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

package serviceexportrequest

import (
	"context"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	kubebindv1alpha1 "github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha1"
	"github.com/kube-bind/kube-bind/sdk/apis/kubebind/v1alpha1/helpers"
	conditionsapi "github.com/kube-bind/kube-bind/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kube-bind/kube-bind/sdk/apis/third_party/conditions/util/conditions"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
)

type reconciler struct {
	informerScope kubebindv1alpha1.Scope

	getCRD              func(cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	getServiceExport    func(cluster logicalcluster.Name, ns, name string) (*kubebindv1alpha1.APIServiceExport, error)
	createServiceExport func(ctx context.Context, cluster logicalcluster.Name, resource *kubebindv1alpha1.APIServiceExport) (*kubebindv1alpha1.APIServiceExport, error)

	deleteServiceExportRequest func(ctx context.Context, cluster logicalcluster.Name, namespace, name string) error
}

func (r *reconciler) reconcile(ctx context.Context, clusterName logicalcluster.Name, req *kubebindv1alpha1.APIServiceExportRequest) error {
	var errs []error

	if err := r.ensureExports(ctx, clusterName, req); err != nil {
		errs = append(errs, err)
	}

	conditions.SetSummary(req)

	return utilerrors.NewAggregate(errs)
}

func (r *reconciler) ensureExports(ctx context.Context, clusterName logicalcluster.Name, req *kubebindv1alpha1.APIServiceExportRequest) error {
	logger := klog.FromContext(ctx)

	if req.Status.Phase == kubebindv1alpha1.APIServiceExportRequestPhasePending {
		failure := false
		for _, res := range req.Spec.Resources {
			name := res.Resource + "." + res.Group
			crd, err := r.getCRD(clusterName, name)
			if err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if apierrors.IsNotFound(err) {
				conditions.MarkFalse(
					req,
					kubebindv1alpha1.APIServiceExportRequestConditionExportsReady,
					"CRDNotFound",
					conditionsapi.ConditionSeverityError,
					"CustomResourceDefinition %s in the service provider cluster not found",
					name,
				)
				failure = true
				break
			}

			if _, err := r.getServiceExport(clusterName, req.Namespace, name); err != nil && !apierrors.IsNotFound(err) {
				return err
			} else if err == nil {
				continue
			}

			exportSpec, err := helpers.CRDToServiceExport(crd)
			if err != nil {
				conditions.MarkFalse(
					req,
					kubebindv1alpha1.APIServiceExportRequestConditionExportsReady,
					"CRDInvalid",
					conditionsapi.ConditionSeverityError,
					"CustomResourceDefinition %s cannot be converted to a APIServiceExport: %v",
					name,
					err,
				)
				failure = true
				break
			}
			hash := helpers.APIServiceExportCRDSpecHash(exportSpec)
			export := &kubebindv1alpha1.APIServiceExport{
				ObjectMeta: metav1.ObjectMeta{
					Name:      crd.Name,
					Namespace: req.Namespace,
					Annotations: map[string]string{
						kubebindv1alpha1.SourceSpecHashAnnotationKey: hash,
					},
				},
				Spec: kubebindv1alpha1.APIServiceExportSpec{
					APIServiceExportCRDSpec: *exportSpec,
					InformerScope:           r.informerScope,
				},
			}

			logger.V(1).Info("Creating APIServiceExport", "name", export.Name, "namespace", export.Namespace)
			if _, err = r.createServiceExport(ctx, clusterName, export); err != nil {
				return err
			}
		}

		if !failure {
			conditions.MarkTrue(req, kubebindv1alpha1.APIServiceExportRequestConditionExportsReady)
			req.Status.Phase = kubebindv1alpha1.APIServiceExportRequestPhaseSucceeded
			return nil
		}

		if time.Since(req.CreationTimestamp.Time) > time.Minute {
			req.Status.Phase = kubebindv1alpha1.APIServiceExportRequestPhaseFailed
			req.Status.TerminalMessage = conditions.GetMessage(req, kubebindv1alpha1.APIServiceExportRequestConditionExportsReady)
		}

		return nil
	}

	if time.Since(req.CreationTimestamp.Time) > 10*time.Minute {
		logger.Info("Deleting service binding request %s/%s", req.Namespace, req.Name, "reason", "timeout", "age", time.Since(req.CreationTimestamp.Time))
		return r.deleteServiceExportRequest(ctx, clusterName, req.Namespace, req.Name)
	}

	return nil
}
