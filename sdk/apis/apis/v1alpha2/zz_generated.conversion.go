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

// Code generated by conversion-gen. DO NOT EDIT.

package v1alpha2

import (
	unsafe "unsafe"

	v1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	v1 "k8s.io/api/core/v1"
	conversion "k8s.io/apimachinery/pkg/conversion"
	runtime "k8s.io/apimachinery/pkg/runtime"
)

func init() {
	localSchemeBuilder.Register(RegisterConversions)
}

// RegisterConversions adds conversion functions to the given scheme.
// Public to allow building arbitrary schemes.
func RegisterConversions(s *runtime.Scheme) error {
	if err := s.AddGeneratedConversionFunc((*APIExportList)(nil), (*v1alpha1.APIExportList)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_APIExportList_To_v1alpha1_APIExportList(a.(*APIExportList), b.(*v1alpha1.APIExportList), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.APIExportList)(nil), (*APIExportList)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_APIExportList_To_v1alpha2_APIExportList(a.(*v1alpha1.APIExportList), b.(*APIExportList), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*APIExportStatus)(nil), (*v1alpha1.APIExportStatus)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_APIExportStatus_To_v1alpha1_APIExportStatus(a.(*APIExportStatus), b.(*v1alpha1.APIExportStatus), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.APIExportStatus)(nil), (*APIExportStatus)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_APIExportStatus_To_v1alpha2_APIExportStatus(a.(*v1alpha1.APIExportStatus), b.(*APIExportStatus), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*GroupResource)(nil), (*v1alpha1.GroupResource)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_GroupResource_To_v1alpha1_GroupResource(a.(*GroupResource), b.(*v1alpha1.GroupResource), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.GroupResource)(nil), (*GroupResource)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_GroupResource_To_v1alpha2_GroupResource(a.(*v1alpha1.GroupResource), b.(*GroupResource), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*Identity)(nil), (*v1alpha1.Identity)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_Identity_To_v1alpha1_Identity(a.(*Identity), b.(*v1alpha1.Identity), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.Identity)(nil), (*Identity)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_Identity_To_v1alpha2_Identity(a.(*v1alpha1.Identity), b.(*Identity), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*LocalAPIExportPolicy)(nil), (*v1alpha1.LocalAPIExportPolicy)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_LocalAPIExportPolicy_To_v1alpha1_LocalAPIExportPolicy(a.(*LocalAPIExportPolicy), b.(*v1alpha1.LocalAPIExportPolicy), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.LocalAPIExportPolicy)(nil), (*LocalAPIExportPolicy)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_LocalAPIExportPolicy_To_v1alpha2_LocalAPIExportPolicy(a.(*v1alpha1.LocalAPIExportPolicy), b.(*LocalAPIExportPolicy), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*MaximalPermissionPolicy)(nil), (*v1alpha1.MaximalPermissionPolicy)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_MaximalPermissionPolicy_To_v1alpha1_MaximalPermissionPolicy(a.(*MaximalPermissionPolicy), b.(*v1alpha1.MaximalPermissionPolicy), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.MaximalPermissionPolicy)(nil), (*MaximalPermissionPolicy)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_MaximalPermissionPolicy_To_v1alpha2_MaximalPermissionPolicy(a.(*v1alpha1.MaximalPermissionPolicy), b.(*MaximalPermissionPolicy), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*PermissionClaim)(nil), (*v1alpha1.PermissionClaim)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim(a.(*PermissionClaim), b.(*v1alpha1.PermissionClaim), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.PermissionClaim)(nil), (*PermissionClaim)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(a.(*v1alpha1.PermissionClaim), b.(*PermissionClaim), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*ResourceSelector)(nil), (*v1alpha1.ResourceSelector)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_ResourceSelector_To_v1alpha1_ResourceSelector(a.(*ResourceSelector), b.(*v1alpha1.ResourceSelector), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.ResourceSelector)(nil), (*ResourceSelector)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_ResourceSelector_To_v1alpha2_ResourceSelector(a.(*v1alpha1.ResourceSelector), b.(*ResourceSelector), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*VirtualWorkspace)(nil), (*v1alpha1.VirtualWorkspace)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_VirtualWorkspace_To_v1alpha1_VirtualWorkspace(a.(*VirtualWorkspace), b.(*v1alpha1.VirtualWorkspace), scope)
	}); err != nil {
		return err
	}
	if err := s.AddGeneratedConversionFunc((*v1alpha1.VirtualWorkspace)(nil), (*VirtualWorkspace)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_VirtualWorkspace_To_v1alpha2_VirtualWorkspace(a.(*v1alpha1.VirtualWorkspace), b.(*VirtualWorkspace), scope)
	}); err != nil {
		return err
	}
	if err := s.AddConversionFunc((*v1alpha1.APIExportSpec)(nil), (*APIExportSpec)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_APIExportSpec_To_v1alpha2_APIExportSpec(a.(*v1alpha1.APIExportSpec), b.(*APIExportSpec), scope)
	}); err != nil {
		return err
	}
	if err := s.AddConversionFunc((*v1alpha1.APIExport)(nil), (*APIExport)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha1_APIExport_To_v1alpha2_APIExport(a.(*v1alpha1.APIExport), b.(*APIExport), scope)
	}); err != nil {
		return err
	}
	if err := s.AddConversionFunc((*APIExportSpec)(nil), (*v1alpha1.APIExportSpec)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_APIExportSpec_To_v1alpha1_APIExportSpec(a.(*APIExportSpec), b.(*v1alpha1.APIExportSpec), scope)
	}); err != nil {
		return err
	}
	if err := s.AddConversionFunc((*APIExport)(nil), (*v1alpha1.APIExport)(nil), func(a, b interface{}, scope conversion.Scope) error {
		return Convert_v1alpha2_APIExport_To_v1alpha1_APIExport(a.(*APIExport), b.(*v1alpha1.APIExport), scope)
	}); err != nil {
		return err
	}
	return nil
}

func autoConvert_v1alpha2_APIExport_To_v1alpha1_APIExport(in *APIExport, out *v1alpha1.APIExport, s conversion.Scope) error {
	out.ObjectMeta = in.ObjectMeta
	if err := Convert_v1alpha2_APIExportSpec_To_v1alpha1_APIExportSpec(&in.Spec, &out.Spec, s); err != nil {
		return err
	}
	if err := Convert_v1alpha2_APIExportStatus_To_v1alpha1_APIExportStatus(&in.Status, &out.Status, s); err != nil {
		return err
	}
	return nil
}

func autoConvert_v1alpha1_APIExport_To_v1alpha2_APIExport(in *v1alpha1.APIExport, out *APIExport, s conversion.Scope) error {
	out.ObjectMeta = in.ObjectMeta
	if err := Convert_v1alpha1_APIExportSpec_To_v1alpha2_APIExportSpec(&in.Spec, &out.Spec, s); err != nil {
		return err
	}
	if err := Convert_v1alpha1_APIExportStatus_To_v1alpha2_APIExportStatus(&in.Status, &out.Status, s); err != nil {
		return err
	}
	return nil
}

func autoConvert_v1alpha2_APIExportList_To_v1alpha1_APIExportList(in *APIExportList, out *v1alpha1.APIExportList, s conversion.Scope) error {
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]v1alpha1.APIExport, len(*in))
		for i := range *in {
			if err := Convert_v1alpha2_APIExport_To_v1alpha1_APIExport(&(*in)[i], &(*out)[i], s); err != nil {
				return err
			}
		}
	} else {
		out.Items = nil
	}
	return nil
}

// Convert_v1alpha2_APIExportList_To_v1alpha1_APIExportList is an autogenerated conversion function.
func Convert_v1alpha2_APIExportList_To_v1alpha1_APIExportList(in *APIExportList, out *v1alpha1.APIExportList, s conversion.Scope) error {
	return autoConvert_v1alpha2_APIExportList_To_v1alpha1_APIExportList(in, out, s)
}

func autoConvert_v1alpha1_APIExportList_To_v1alpha2_APIExportList(in *v1alpha1.APIExportList, out *APIExportList, s conversion.Scope) error {
	out.ListMeta = in.ListMeta
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]APIExport, len(*in))
		for i := range *in {
			if err := Convert_v1alpha1_APIExport_To_v1alpha2_APIExport(&(*in)[i], &(*out)[i], s); err != nil {
				return err
			}
		}
	} else {
		out.Items = nil
	}
	return nil
}

// Convert_v1alpha1_APIExportList_To_v1alpha2_APIExportList is an autogenerated conversion function.
func Convert_v1alpha1_APIExportList_To_v1alpha2_APIExportList(in *v1alpha1.APIExportList, out *APIExportList, s conversion.Scope) error {
	return autoConvert_v1alpha1_APIExportList_To_v1alpha2_APIExportList(in, out, s)
}

func autoConvert_v1alpha2_APIExportSpec_To_v1alpha1_APIExportSpec(in *APIExportSpec, out *v1alpha1.APIExportSpec, s conversion.Scope) error {
	// WARNING: in.ResourceSchemas requires manual conversion: does not exist in peer-type
	out.Identity = (*v1alpha1.Identity)(unsafe.Pointer(in.Identity))
	out.MaximalPermissionPolicy = (*v1alpha1.MaximalPermissionPolicy)(unsafe.Pointer(in.MaximalPermissionPolicy))
	out.PermissionClaims = *(*[]v1alpha1.PermissionClaim)(unsafe.Pointer(&in.PermissionClaims))
	return nil
}

func autoConvert_v1alpha1_APIExportSpec_To_v1alpha2_APIExportSpec(in *v1alpha1.APIExportSpec, out *APIExportSpec, s conversion.Scope) error {
	// WARNING: in.LatestResourceSchemas requires manual conversion: does not exist in peer-type
	out.Identity = (*Identity)(unsafe.Pointer(in.Identity))
	out.MaximalPermissionPolicy = (*MaximalPermissionPolicy)(unsafe.Pointer(in.MaximalPermissionPolicy))
	out.PermissionClaims = *(*[]PermissionClaim)(unsafe.Pointer(&in.PermissionClaims))
	return nil
}

func autoConvert_v1alpha2_APIExportStatus_To_v1alpha1_APIExportStatus(in *APIExportStatus, out *v1alpha1.APIExportStatus, s conversion.Scope) error {
	out.IdentityHash = in.IdentityHash
	out.Conditions = *(*conditionsv1alpha1.Conditions)(unsafe.Pointer(&in.Conditions))
	out.VirtualWorkspaces = *(*[]v1alpha1.VirtualWorkspace)(unsafe.Pointer(&in.VirtualWorkspaces))
	return nil
}

// Convert_v1alpha2_APIExportStatus_To_v1alpha1_APIExportStatus is an autogenerated conversion function.
func Convert_v1alpha2_APIExportStatus_To_v1alpha1_APIExportStatus(in *APIExportStatus, out *v1alpha1.APIExportStatus, s conversion.Scope) error {
	return autoConvert_v1alpha2_APIExportStatus_To_v1alpha1_APIExportStatus(in, out, s)
}

func autoConvert_v1alpha1_APIExportStatus_To_v1alpha2_APIExportStatus(in *v1alpha1.APIExportStatus, out *APIExportStatus, s conversion.Scope) error {
	out.IdentityHash = in.IdentityHash
	out.Conditions = *(*conditionsv1alpha1.Conditions)(unsafe.Pointer(&in.Conditions))
	out.VirtualWorkspaces = *(*[]VirtualWorkspace)(unsafe.Pointer(&in.VirtualWorkspaces))
	return nil
}

// Convert_v1alpha1_APIExportStatus_To_v1alpha2_APIExportStatus is an autogenerated conversion function.
func Convert_v1alpha1_APIExportStatus_To_v1alpha2_APIExportStatus(in *v1alpha1.APIExportStatus, out *APIExportStatus, s conversion.Scope) error {
	return autoConvert_v1alpha1_APIExportStatus_To_v1alpha2_APIExportStatus(in, out, s)
}

func autoConvert_v1alpha2_GroupResource_To_v1alpha1_GroupResource(in *GroupResource, out *v1alpha1.GroupResource, s conversion.Scope) error {
	out.Group = in.Group
	out.Resource = in.Resource
	return nil
}

// Convert_v1alpha2_GroupResource_To_v1alpha1_GroupResource is an autogenerated conversion function.
func Convert_v1alpha2_GroupResource_To_v1alpha1_GroupResource(in *GroupResource, out *v1alpha1.GroupResource, s conversion.Scope) error {
	return autoConvert_v1alpha2_GroupResource_To_v1alpha1_GroupResource(in, out, s)
}

func autoConvert_v1alpha1_GroupResource_To_v1alpha2_GroupResource(in *v1alpha1.GroupResource, out *GroupResource, s conversion.Scope) error {
	out.Group = in.Group
	out.Resource = in.Resource
	return nil
}

// Convert_v1alpha1_GroupResource_To_v1alpha2_GroupResource is an autogenerated conversion function.
func Convert_v1alpha1_GroupResource_To_v1alpha2_GroupResource(in *v1alpha1.GroupResource, out *GroupResource, s conversion.Scope) error {
	return autoConvert_v1alpha1_GroupResource_To_v1alpha2_GroupResource(in, out, s)
}

func autoConvert_v1alpha2_Identity_To_v1alpha1_Identity(in *Identity, out *v1alpha1.Identity, s conversion.Scope) error {
	out.SecretRef = (*v1.SecretReference)(unsafe.Pointer(in.SecretRef))
	return nil
}

// Convert_v1alpha2_Identity_To_v1alpha1_Identity is an autogenerated conversion function.
func Convert_v1alpha2_Identity_To_v1alpha1_Identity(in *Identity, out *v1alpha1.Identity, s conversion.Scope) error {
	return autoConvert_v1alpha2_Identity_To_v1alpha1_Identity(in, out, s)
}

func autoConvert_v1alpha1_Identity_To_v1alpha2_Identity(in *v1alpha1.Identity, out *Identity, s conversion.Scope) error {
	out.SecretRef = (*v1.SecretReference)(unsafe.Pointer(in.SecretRef))
	return nil
}

// Convert_v1alpha1_Identity_To_v1alpha2_Identity is an autogenerated conversion function.
func Convert_v1alpha1_Identity_To_v1alpha2_Identity(in *v1alpha1.Identity, out *Identity, s conversion.Scope) error {
	return autoConvert_v1alpha1_Identity_To_v1alpha2_Identity(in, out, s)
}

func autoConvert_v1alpha2_LocalAPIExportPolicy_To_v1alpha1_LocalAPIExportPolicy(in *LocalAPIExportPolicy, out *v1alpha1.LocalAPIExportPolicy, s conversion.Scope) error {
	return nil
}

// Convert_v1alpha2_LocalAPIExportPolicy_To_v1alpha1_LocalAPIExportPolicy is an autogenerated conversion function.
func Convert_v1alpha2_LocalAPIExportPolicy_To_v1alpha1_LocalAPIExportPolicy(in *LocalAPIExportPolicy, out *v1alpha1.LocalAPIExportPolicy, s conversion.Scope) error {
	return autoConvert_v1alpha2_LocalAPIExportPolicy_To_v1alpha1_LocalAPIExportPolicy(in, out, s)
}

func autoConvert_v1alpha1_LocalAPIExportPolicy_To_v1alpha2_LocalAPIExportPolicy(in *v1alpha1.LocalAPIExportPolicy, out *LocalAPIExportPolicy, s conversion.Scope) error {
	return nil
}

// Convert_v1alpha1_LocalAPIExportPolicy_To_v1alpha2_LocalAPIExportPolicy is an autogenerated conversion function.
func Convert_v1alpha1_LocalAPIExportPolicy_To_v1alpha2_LocalAPIExportPolicy(in *v1alpha1.LocalAPIExportPolicy, out *LocalAPIExportPolicy, s conversion.Scope) error {
	return autoConvert_v1alpha1_LocalAPIExportPolicy_To_v1alpha2_LocalAPIExportPolicy(in, out, s)
}

func autoConvert_v1alpha2_MaximalPermissionPolicy_To_v1alpha1_MaximalPermissionPolicy(in *MaximalPermissionPolicy, out *v1alpha1.MaximalPermissionPolicy, s conversion.Scope) error {
	out.Local = (*v1alpha1.LocalAPIExportPolicy)(unsafe.Pointer(in.Local))
	return nil
}

// Convert_v1alpha2_MaximalPermissionPolicy_To_v1alpha1_MaximalPermissionPolicy is an autogenerated conversion function.
func Convert_v1alpha2_MaximalPermissionPolicy_To_v1alpha1_MaximalPermissionPolicy(in *MaximalPermissionPolicy, out *v1alpha1.MaximalPermissionPolicy, s conversion.Scope) error {
	return autoConvert_v1alpha2_MaximalPermissionPolicy_To_v1alpha1_MaximalPermissionPolicy(in, out, s)
}

func autoConvert_v1alpha1_MaximalPermissionPolicy_To_v1alpha2_MaximalPermissionPolicy(in *v1alpha1.MaximalPermissionPolicy, out *MaximalPermissionPolicy, s conversion.Scope) error {
	out.Local = (*LocalAPIExportPolicy)(unsafe.Pointer(in.Local))
	return nil
}

// Convert_v1alpha1_MaximalPermissionPolicy_To_v1alpha2_MaximalPermissionPolicy is an autogenerated conversion function.
func Convert_v1alpha1_MaximalPermissionPolicy_To_v1alpha2_MaximalPermissionPolicy(in *v1alpha1.MaximalPermissionPolicy, out *MaximalPermissionPolicy, s conversion.Scope) error {
	return autoConvert_v1alpha1_MaximalPermissionPolicy_To_v1alpha2_MaximalPermissionPolicy(in, out, s)
}

func autoConvert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim(in *PermissionClaim, out *v1alpha1.PermissionClaim, s conversion.Scope) error {
	if err := Convert_v1alpha2_GroupResource_To_v1alpha1_GroupResource(&in.GroupResource, &out.GroupResource, s); err != nil {
		return err
	}
	out.All = in.All
	out.ResourceSelector = *(*[]v1alpha1.ResourceSelector)(unsafe.Pointer(&in.ResourceSelector))
	out.IdentityHash = in.IdentityHash
	return nil
}

// Convert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim is an autogenerated conversion function.
func Convert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim(in *PermissionClaim, out *v1alpha1.PermissionClaim, s conversion.Scope) error {
	return autoConvert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim(in, out, s)
}

func autoConvert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(in *v1alpha1.PermissionClaim, out *PermissionClaim, s conversion.Scope) error {
	if err := Convert_v1alpha1_GroupResource_To_v1alpha2_GroupResource(&in.GroupResource, &out.GroupResource, s); err != nil {
		return err
	}
	out.All = in.All
	out.ResourceSelector = *(*[]ResourceSelector)(unsafe.Pointer(&in.ResourceSelector))
	out.IdentityHash = in.IdentityHash
	return nil
}

// Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim is an autogenerated conversion function.
func Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(in *v1alpha1.PermissionClaim, out *PermissionClaim, s conversion.Scope) error {
	return autoConvert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(in, out, s)
}

func autoConvert_v1alpha2_ResourceSelector_To_v1alpha1_ResourceSelector(in *ResourceSelector, out *v1alpha1.ResourceSelector, s conversion.Scope) error {
	out.Name = in.Name
	out.Namespace = in.Namespace
	return nil
}

// Convert_v1alpha2_ResourceSelector_To_v1alpha1_ResourceSelector is an autogenerated conversion function.
func Convert_v1alpha2_ResourceSelector_To_v1alpha1_ResourceSelector(in *ResourceSelector, out *v1alpha1.ResourceSelector, s conversion.Scope) error {
	return autoConvert_v1alpha2_ResourceSelector_To_v1alpha1_ResourceSelector(in, out, s)
}

func autoConvert_v1alpha1_ResourceSelector_To_v1alpha2_ResourceSelector(in *v1alpha1.ResourceSelector, out *ResourceSelector, s conversion.Scope) error {
	out.Name = in.Name
	out.Namespace = in.Namespace
	return nil
}

// Convert_v1alpha1_ResourceSelector_To_v1alpha2_ResourceSelector is an autogenerated conversion function.
func Convert_v1alpha1_ResourceSelector_To_v1alpha2_ResourceSelector(in *v1alpha1.ResourceSelector, out *ResourceSelector, s conversion.Scope) error {
	return autoConvert_v1alpha1_ResourceSelector_To_v1alpha2_ResourceSelector(in, out, s)
}

func autoConvert_v1alpha2_VirtualWorkspace_To_v1alpha1_VirtualWorkspace(in *VirtualWorkspace, out *v1alpha1.VirtualWorkspace, s conversion.Scope) error {
	out.URL = in.URL
	return nil
}

// Convert_v1alpha2_VirtualWorkspace_To_v1alpha1_VirtualWorkspace is an autogenerated conversion function.
func Convert_v1alpha2_VirtualWorkspace_To_v1alpha1_VirtualWorkspace(in *VirtualWorkspace, out *v1alpha1.VirtualWorkspace, s conversion.Scope) error {
	return autoConvert_v1alpha2_VirtualWorkspace_To_v1alpha1_VirtualWorkspace(in, out, s)
}

func autoConvert_v1alpha1_VirtualWorkspace_To_v1alpha2_VirtualWorkspace(in *v1alpha1.VirtualWorkspace, out *VirtualWorkspace, s conversion.Scope) error {
	out.URL = in.URL
	return nil
}

// Convert_v1alpha1_VirtualWorkspace_To_v1alpha2_VirtualWorkspace is an autogenerated conversion function.
func Convert_v1alpha1_VirtualWorkspace_To_v1alpha2_VirtualWorkspace(in *v1alpha1.VirtualWorkspace, out *VirtualWorkspace, s conversion.Scope) error {
	return autoConvert_v1alpha1_VirtualWorkspace_To_v1alpha2_VirtualWorkspace(in, out, s)
}
