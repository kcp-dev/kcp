/*
Copyright 2021 The kcp Authors.

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

package helpers

import (
	"context"
	"embed"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
)

// ReadRawFromFS reads a file from the filesystem, applies the
// transformers and templates it.
func ReadRawFromFS(
	ctx context.Context,
	embedFS embed.FS,
	filename string,
	transformers []TransformFileFunc,
	ti *TemplateInput,
) ([]byte, error) {
	raw, err := embedFS.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("could not read %q: %w", filename, err)
	}

	raw, err = applyTransformers(raw, transformers...)
	if err != nil {
		return nil, fmt.Errorf("error applying transformers to %q: %w", filename, err)
	}

	raw, err = Template(ctx, raw, ti)
	if err != nil {
		return nil, fmt.Errorf("error templating manifest %q: %w", filename, err)
	}

	return raw, nil
}

// ReadResourceFromFS uses ReadRawFromFS to read a file and returns it
// based on the batteries input as a slice of unstructured.Unstructured.
func ReadResourceFromFS(
	ctx context.Context,
	embedFS embed.FS,
	filename string,
	transformers []TransformFileFunc,
	batteries sets.Set[string],
) ([]*unstructured.Unstructured, error) {
	ti := NewTemplateInput(batteries)
	raw, err := ReadRawFromFS(ctx, embedFS, filename, transformers, ti)
	if err != nil {
		return nil, err
	}

	resources, err := ParseYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("error parsing manifests from %q as yaml: %w", filename, err)
	}

	ret := make([]*unstructured.Unstructured, 0, len(resources))
	for _, resource := range resources {
		value, found := resource.GetAnnotations()[annotationBattery]
		if !found {
			// resource has no batteries annotation, add it to be
			// installed
			ret = append(ret, resource)
			continue
		}

		annotated := sets.New[string](strings.Split(value, ",")...)
		if batteries.Intersection(annotated).Len() > 0 {
			// One of the requested batteries is in the annotation, add
			// the resource to be installed
			ret = append(ret, resource)
		}
	}

	return ret, nil
}

// ReadResourcesFromFS calls ReadResourceFromFS for each file in the
// filesystem and returns an aggregation of all resources.
func ReadResourcesFromFS(
	ctx context.Context,
	embedFS embed.FS,
	transformers []TransformFileFunc,
	batteries sets.Set[string],
) ([]*unstructured.Unstructured, error) {
	files, err := embedFS.ReadDir(".")
	if err != nil {
		return nil, err
	}

	ret := []*unstructured.Unstructured{}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		us, err := ReadResourceFromFS(
			ctx,
			embedFS,
			f.Name(),
			transformers,
			batteries,
		)
		if err != nil {
			return nil, err
		}
		ret = append(ret, us...)
	}

	return ret, nil
}
