// Hand-written stand-in for the output of deepcopy-gen. When code
// generation lands in this repo (alongside client-gen, etc.) this
// file will be regenerated automatically; until then the manual
// implementations below keep us honest with runtime.Object.

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
)

// DeepCopyInto copies all properties of this object into another
// object of the same type that is provided as a pointer.
func (in *SelfClusterAccessReview) DeepCopyInto(out *SelfClusterAccessReview) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Status.DeepCopyInto(&out.Status)
}

// DeepCopy creates a deep copy of SelfClusterAccessReview.
func (in *SelfClusterAccessReview) DeepCopy() *SelfClusterAccessReview {
	if in == nil {
		return nil
	}
	out := new(SelfClusterAccessReview)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *SelfClusterAccessReview) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}

// DeepCopyInto copies all properties into another object of the
// same type.
func (in *SelfClusterAccessReviewStatus) DeepCopyInto(out *SelfClusterAccessReviewStatus) {
	*out = *in
	if in.Clusters != nil {
		out.Clusters = make([]AccessEndpointSlice, len(in.Clusters))
		copy(out.Clusters, in.Clusters)
	}
}

// DeepCopy creates a deep copy of SelfClusterAccessReviewStatus.
func (in *SelfClusterAccessReviewStatus) DeepCopy() *SelfClusterAccessReviewStatus {
	if in == nil {
		return nil
	}
	out := new(SelfClusterAccessReviewStatus)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies all properties of this object into another.
func (in *AccessEndpointSlice) DeepCopyInto(out *AccessEndpointSlice) {
	*out = *in
}

// DeepCopy creates a deep copy of AccessEndpointSlice.
func (in *AccessEndpointSlice) DeepCopy() *AccessEndpointSlice {
	if in == nil {
		return nil
	}
	out := new(AccessEndpointSlice)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies all properties into another object.
func (in *SelfClusterAccessReviewList) DeepCopyInto(out *SelfClusterAccessReviewList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]SelfClusterAccessReview, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

// DeepCopy creates a deep copy of SelfClusterAccessReviewList.
func (in *SelfClusterAccessReviewList) DeepCopy() *SelfClusterAccessReviewList {
	if in == nil {
		return nil
	}
	out := new(SelfClusterAccessReviewList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *SelfClusterAccessReviewList) DeepCopyObject() runtime.Object {
	return in.DeepCopy()
}
