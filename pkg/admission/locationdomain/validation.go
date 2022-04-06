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

package locationdomain

import (
	metavalidation "k8s.io/apimachinery/pkg/apis/meta/v1/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	locationdomainreconciler "github.com/kcp-dev/kcp/pkg/reconciler/scheduling/locationdomain"
)

func ValidateLocationDomain(domain *schedulingv1alpha1.LocationDomain) field.ErrorList {
	errs := field.ErrorList{}

	errs = append(errs, ValidateSpec(&domain.Spec, field.NewPath("spec"))...)

	return errs
}

func ValidateSpec(spec *schedulingv1alpha1.LocationDomainSpec, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}

	if want := locationdomainreconciler.LocationDomainTypeWorkload; spec.Type != want {
		errs = append(errs, field.NotSupported(fldPath.Child("type"), spec.Type, []string{string(want)}))
	}

	errs = append(errs, ValidateInstances(&spec.Instances, fldPath.Child("instances"))...)

	for i := range spec.Locations {
		errs = append(errs, ValidateLocation(&spec.Locations[i], fldPath.Index(i).Child("locations"))...)

	}
	return errs
}

func ValidateLocation(location *schedulingv1alpha1.LocationDomainLocationDefinition, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}

	errs = append(errs, metavalidation.ValidateLabelSelector(location.InstanceSelector, fldPath.Child("labelSelector"))...)

	return errs
}

func ValidateInstances(instances *schedulingv1alpha1.InstancesReference, fldPath *field.Path) field.ErrorList {
	errs := field.ErrorList{}

	if instances.Resource != workloadClustersGVR {
		errs = append(errs, field.Invalid(fldPath.Child("instances"), instances.Resource, "must be "+workloadClustersGVRString))
	}

	return errs
}
