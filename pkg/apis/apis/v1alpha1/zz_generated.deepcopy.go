//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright The KCP Authors.

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

// Code generated by deepcopy-gen. DO NOT EDIT.

package v1alpha1

import (
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"

	exportsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/exports/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
)

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIBinding) DeepCopyInto(out *APIBinding) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIBinding.
func (in *APIBinding) DeepCopy() *APIBinding {
	if in == nil {
		return nil
	}
	out := new(APIBinding)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *APIBinding) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIBindingList) DeepCopyInto(out *APIBindingList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]APIBinding, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIBindingList.
func (in *APIBindingList) DeepCopy() *APIBindingList {
	if in == nil {
		return nil
	}
	out := new(APIBindingList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *APIBindingList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIBindingSpec) DeepCopyInto(out *APIBindingSpec) {
	*out = *in
	in.Reference.DeepCopyInto(&out.Reference)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIBindingSpec.
func (in *APIBindingSpec) DeepCopy() *APIBindingSpec {
	if in == nil {
		return nil
	}
	out := new(APIBindingSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIBindingStatus) DeepCopyInto(out *APIBindingStatus) {
	*out = *in
	if in.BoundAPIExport != nil {
		in, out := &in.BoundAPIExport, &out.BoundAPIExport
		*out = new(ExportReference)
		(*in).DeepCopyInto(*out)
	}
	if in.BoundResources != nil {
		in, out := &in.BoundResources, &out.BoundResources
		*out = make([]BoundAPIResource, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.Initializers != nil {
		in, out := &in.Initializers, &out.Initializers
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make(conditionsv1alpha1.Conditions, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIBindingStatus.
func (in *APIBindingStatus) DeepCopy() *APIBindingStatus {
	if in == nil {
		return nil
	}
	out := new(APIBindingStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIExport) DeepCopyInto(out *APIExport) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIExport.
func (in *APIExport) DeepCopy() *APIExport {
	if in == nil {
		return nil
	}
	out := new(APIExport)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *APIExport) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIExportList) DeepCopyInto(out *APIExportList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]APIExport, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIExportList.
func (in *APIExportList) DeepCopy() *APIExportList {
	if in == nil {
		return nil
	}
	out := new(APIExportList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *APIExportList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIExportSpec) DeepCopyInto(out *APIExportSpec) {
	*out = *in
	if in.LatestResourceSchemas != nil {
		in, out := &in.LatestResourceSchemas, &out.LatestResourceSchemas
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIExportSpec.
func (in *APIExportSpec) DeepCopy() *APIExportSpec {
	if in == nil {
		return nil
	}
	out := new(APIExportSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIExportStatus) DeepCopyInto(out *APIExportStatus) {
	*out = *in
	if in.ResourceSchemasInUse != nil {
		in, out := &in.ResourceSchemasInUse, &out.ResourceSchemasInUse
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIExportStatus.
func (in *APIExportStatus) DeepCopy() *APIExportStatus {
	if in == nil {
		return nil
	}
	out := new(APIExportStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIResourceSchema) DeepCopyInto(out *APIResourceSchema) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIResourceSchema.
func (in *APIResourceSchema) DeepCopy() *APIResourceSchema {
	if in == nil {
		return nil
	}
	out := new(APIResourceSchema)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *APIResourceSchema) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIResourceSchemaList) DeepCopyInto(out *APIResourceSchemaList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]APIResourceSchema, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIResourceSchemaList.
func (in *APIResourceSchemaList) DeepCopy() *APIResourceSchemaList {
	if in == nil {
		return nil
	}
	out := new(APIResourceSchemaList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject is an autogenerated deepcopy function, copying the receiver, creating a new runtime.Object.
func (in *APIResourceSchemaList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIResourceSchemaSpec) DeepCopyInto(out *APIResourceSchemaSpec) {
	*out = *in
	in.Names.DeepCopyInto(&out.Names)
	if in.Versions != nil {
		in, out := &in.Versions, &out.Versions
		*out = make([]APIResourceVersion, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.Predecessors != nil {
		in, out := &in.Predecessors, &out.Predecessors
		*out = make([]PredecessorSchema, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	if in.Conversions != nil {
		in, out := &in.Conversions, &out.Conversions
		*out = make([]APIResourceVersionConversion, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIResourceSchemaSpec.
func (in *APIResourceSchemaSpec) DeepCopy() *APIResourceSchemaSpec {
	if in == nil {
		return nil
	}
	out := new(APIResourceSchemaSpec)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIResourceVersion) DeepCopyInto(out *APIResourceVersion) {
	*out = *in
	if in.DeprecationWarning != nil {
		in, out := &in.DeprecationWarning, &out.DeprecationWarning
		*out = new(string)
		**out = **in
	}
	in.Schema.DeepCopyInto(&out.Schema)
	if in.Subresources != nil {
		in, out := &in.Subresources, &out.Subresources
		*out = new(v1.CustomResourceSubresources)
		(*in).DeepCopyInto(*out)
	}
	if in.AdditionalPrinterColumns != nil {
		in, out := &in.AdditionalPrinterColumns, &out.AdditionalPrinterColumns
		*out = make([]v1.CustomResourceColumnDefinition, len(*in))
		copy(*out, *in)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIResourceVersion.
func (in *APIResourceVersion) DeepCopy() *APIResourceVersion {
	if in == nil {
		return nil
	}
	out := new(APIResourceVersion)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *APIResourceVersionConversion) DeepCopyInto(out *APIResourceVersionConversion) {
	*out = *in
	if in.CEL != nil {
		in, out := &in.CEL, &out.CEL
		*out = new(CELConversion)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new APIResourceVersionConversion.
func (in *APIResourceVersionConversion) DeepCopy() *APIResourceVersionConversion {
	if in == nil {
		return nil
	}
	out := new(APIResourceVersionConversion)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BoundAPIResource) DeepCopyInto(out *BoundAPIResource) {
	*out = *in
	out.Schema = in.Schema
	if in.StorageVersions != nil {
		in, out := &in.StorageVersions, &out.StorageVersions
		*out = make([]string, len(*in))
		copy(*out, *in)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BoundAPIResource.
func (in *BoundAPIResource) DeepCopy() *BoundAPIResource {
	if in == nil {
		return nil
	}
	out := new(BoundAPIResource)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *BoundAPIResourceSchema) DeepCopyInto(out *BoundAPIResourceSchema) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new BoundAPIResourceSchema.
func (in *BoundAPIResourceSchema) DeepCopy() *BoundAPIResourceSchema {
	if in == nil {
		return nil
	}
	out := new(BoundAPIResourceSchema)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *CELConversion) DeepCopyInto(out *CELConversion) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new CELConversion.
func (in *CELConversion) DeepCopy() *CELConversion {
	if in == nil {
		return nil
	}
	out := new(CELConversion)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *ExportReference) DeepCopyInto(out *ExportReference) {
	*out = *in
	if in.Export != nil {
		in, out := &in.Export, &out.Export
		*out = new(exportsv1alpha1.ExportBindingReference)
		**out = **in
	}
	if in.Workspace != nil {
		in, out := &in.Workspace, &out.Workspace
		*out = new(WorkspaceExportReference)
		**out = **in
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new ExportReference.
func (in *ExportReference) DeepCopy() *ExportReference {
	if in == nil {
		return nil
	}
	out := new(ExportReference)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PredecessorOverrides) DeepCopyInto(out *PredecessorOverrides) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PredecessorOverrides.
func (in *PredecessorOverrides) DeepCopy() *PredecessorOverrides {
	if in == nil {
		return nil
	}
	out := new(PredecessorOverrides)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PredecessorPerVersionOverrides) DeepCopyInto(out *PredecessorPerVersionOverrides) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PredecessorPerVersionOverrides.
func (in *PredecessorPerVersionOverrides) DeepCopy() *PredecessorPerVersionOverrides {
	if in == nil {
		return nil
	}
	out := new(PredecessorPerVersionOverrides)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *PredecessorSchema) DeepCopyInto(out *PredecessorSchema) {
	*out = *in
	if in.Overrides != nil {
		in, out := &in.Overrides, &out.Overrides
		*out = new(PredecessorOverrides)
		**out = **in
	}
	if in.VersionOverrides != nil {
		in, out := &in.VersionOverrides, &out.VersionOverrides
		*out = make([]PredecessorPerVersionOverrides, len(*in))
		copy(*out, *in)
	}
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new PredecessorSchema.
func (in *PredecessorSchema) DeepCopy() *PredecessorSchema {
	if in == nil {
		return nil
	}
	out := new(PredecessorSchema)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto is an autogenerated deepcopy function, copying the receiver, writing into out. in must be non-nil.
func (in *WorkspaceExportReference) DeepCopyInto(out *WorkspaceExportReference) {
	*out = *in
	return
}

// DeepCopy is an autogenerated deepcopy function, copying the receiver, creating a new WorkspaceExportReference.
func (in *WorkspaceExportReference) DeepCopy() *WorkspaceExportReference {
	if in == nil {
		return nil
	}
	out := new(WorkspaceExportReference)
	in.DeepCopyInto(out)
	return out
}
