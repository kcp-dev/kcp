package config

import (
	"context"
	"embed"
	"fmt"
	"sync"
	"time"

	crdhelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apiextensionsv1client "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/typed/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog/v2"
)

//go:embed *.yaml
var rawCustomResourceDefinitions embed.FS

// BootstrapCustomResourceDefinitions creates the CRDs using the target client and waits
// for all of them to become established in parallel. This call is blocking.
func BootstrapCustomResourceDefinitions(ctx context.Context, client apiextensionsv1client.CustomResourceDefinitionInterface, gks []metav1.GroupKind) error {
	wg := sync.WaitGroup{}
	bootstrapErrChan := make(chan error, len(gks))
	for _, gk := range gks {
		wg.Add(1)
		go func(gk metav1.GroupKind) {
			defer wg.Done()
			bootstrapErrChan <- BootstrapCustomResourceDefinition(ctx, client, gk)
		}(gk)
	}
	wg.Wait()
	close(bootstrapErrChan)
	var bootstrapErrors []error
	for err := range bootstrapErrChan {
		bootstrapErrors = append(bootstrapErrors, err)
	}
	if err := kerrors.NewAggregate(bootstrapErrors); err != nil {
		return fmt.Errorf("could not bootstrap CRDs: %w", err)
	}
	return nil
}

// BootstrapCustomResourceDefinition creates the CRD using the target client and waits
// for it to become established. This call is blocking.
func BootstrapCustomResourceDefinition(ctx context.Context, client apiextensionsv1client.CustomResourceDefinitionInterface, gk metav1.GroupKind) error {
	start := time.Now()
	klog.Infof("bootstrapping %v", gk.String())
	defer klog.Infof("bootstrapped %v after %s", gk.String(), time.Since(start).String())
	raw, err := rawCustomResourceDefinitions.ReadFile(fmt.Sprintf("%s_%s.yaml", gk.Group, gk.Kind))
	if err != nil {
		return fmt.Errorf("could not read CRD %s: %w", gk.String(), err)
	}
	expectedGvk := &schema.GroupVersionKind{Group: apiextensionsv1.GroupName, Version: "v1", Kind: "CustomResourceDefinition"}
	obj, gvk, err := extensionsapiserver.Codecs.UniversalDeserializer().Decode(raw, expectedGvk, &apiextensionsv1.CustomResourceDefinition{})
	if err != nil {
		return fmt.Errorf("could not decode raw CRD %s: %w", gk.String(), err)
	}
	if !equality.Semantic.DeepEqual(gvk, expectedGvk) {
		return fmt.Errorf("decoded CRD %s into incorrect GroupVersionKind, got %#v, wanted %#v", gk.String(), gvk, expectedGvk)
	}
	rawCrd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return fmt.Errorf("decoded CRD %s into incorrect type, got %T, wanted %T", gk.String(), rawCrd, &apiextensionsv1.CustomResourceDefinition{})
	}

	crd, err := client.Create(ctx, rawCrd, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("could not create CRD %s: %w", gk.String(), err)
	}
	watcher, err := client.Watch(ctx, metav1.ListOptions{
		FieldSelector:   fields.OneTermEqualSelector("metadata.name", crd.Name).String(),
		ResourceVersion: crd.ResourceVersion,
	})
	if err != nil {
		return fmt.Errorf("could not watch CRD %s: %w", gk.String(), err)
	}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("failed to wait for CRD %s to be established: %w", gk.String(), ctx.Err())
		case event := <-watcher.ResultChan():
			switch event.Type {
			case watch.Added, watch.Bookmark:
				continue
			case watch.Modified:
				updated, ok := event.Object.(*apiextensionsv1.CustomResourceDefinition)
				if !ok {
					continue
				}
				if crdhelpers.IsCRDConditionTrue(updated, apiextensionsv1.Established) {
					return nil
				}
			case watch.Deleted:
				return fmt.Errorf("CRD %s was deleted before being established", gk.String())
			case watch.Error:
				return fmt.Errorf("encountered error while watching CRD %s: %#v", gk.String(), event.Object)
			}
		}
	}
}
