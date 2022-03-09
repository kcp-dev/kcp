package mutators

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Mutator interface {
	ApplySpec(downstreamObj *unstructured.Unstructured) error
	ApplyStatus(upstreamObj *unstructured.Unstructured) error
}
