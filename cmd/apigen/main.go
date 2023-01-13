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

package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bombsimon/logrusr/v3"
	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/sirupsen/logrus"
	"github.com/spf13/pflag"

	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubernetes/pkg/apis/admissionregistration"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/pkg/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/core"
)

const (
	apiResourceSchemaNamePrefix = "apiresourceschema-"
	apiExportNamePrefix         = "apiexport-"
)

type options struct {
	inputDir  string
	outputDir string
}

func bindOptions(fs *pflag.FlagSet) *options {
	o := options{}
	fs.StringVar(&o.inputDir, "input-dir", "", "Directory containing CustomResourceDefinition YAML files.")
	fs.StringVar(&o.outputDir, "output-dir", "", "Directory where APIResourceSchemas and APIExports will be written.")
	return &o
}

func (o *options) Validate() error {
	if o.inputDir == "" {
		return fmt.Errorf("--input-dir is required")
	}
	if o.outputDir == "" {
		return fmt.Errorf("--output-dir is required")
	}
	return nil
}

func getLogger() logr.Logger {
	return logrusr.New(logrus.New())
}

const name = "apigen"

var (
	scheme = runtime.NewScheme()
	codecs = serializer.NewCodecFactory(scheme)
)

func main() {
	logger := getLogger()
	logger = logger.WithName(name)

	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		logger.Error(err, "Could not initialize scheme")
		os.Exit(1)
	}

	fs := pflag.NewFlagSet(name, pflag.ContinueOnError)
	opts := bindOptions(fs)

	if err := fs.Parse(os.Args[1:]); err != nil {
		logger.Error(err, "Could not parse options.")
		os.Exit(1)
	}

	if err := opts.Validate(); err != nil {
		logger.Error(err, "Invalid options.")
		os.Exit(1)
	}

	crds, err := loadCustomResourceDefinitions(logger, opts.inputDir)
	if err != nil {
		logger.Error(err, "Could not load CustomResourceDefinitions.")
		os.Exit(1)
	}

	if _, err := os.Stat(opts.outputDir); os.IsNotExist(err) {
		if err := os.MkdirAll(opts.outputDir, os.FileMode(0755)); err != nil {
			logger.Error(err, "Could not create directory to write output files.")
			os.Exit(1)
		}
	}
	previousApiResourceSchemas, err := loadAPIResourceSchemas(logger, opts.outputDir)
	if err != nil {
		logger.Error(err, "Could not load previous APIResourceSchemas.")
		os.Exit(1)
	}

	gitHEAD, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		logger.Error(err, "Could not get git revision")
		os.Exit(1)
	}

	prefix := fmt.Sprintf("v%s-%s", time.Now().Format("060102"), strings.TrimSpace(string(gitHEAD)))

	currentApiResourceSchemas, err := convertToSchemas(prefix, crds)
	if err != nil {
		logger.Error(err, "Could not convert CustomResourceDefinitions to APIResourceSchemas.")
		os.Exit(1)
	}

	apiResourceSchemas := resolveLatestAPIResourceSchemas(logger, previousApiResourceSchemas, currentApiResourceSchemas)
	apiExports, err := generateExports(opts.outputDir, apiResourceSchemas)
	if err != nil {
		logger.Error(err, "Could not generate APIExports.")
		os.Exit(1)
	}

	if err := writeObjects(logger, opts.outputDir, apiExports, apiResourceSchemas); err != nil {
		logger.Error(err, "Could not write manifests.")
		os.Exit(1)
	}
}

func loadCustomResourceDefinitions(logger logr.Logger, baseDir string) (map[metav1.GroupResource]*apiextensionsv1.CustomResourceDefinition, error) {
	logger.Info(fmt.Sprintf("Loading CustomResourceDefinitions from %s", baseDir))
	crds := map[metav1.GroupResource]*apiextensionsv1.CustomResourceDefinition{}
	if err := filepath.Walk(baseDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(info.Name()) != ".yaml" {
			return nil
		}

		parts := strings.Split(strings.TrimSuffix(info.Name(), ".yaml"), "_")
		if len(parts) != 2 {
			return fmt.Errorf("could not parse filename %s as group_resource.yaml", info.Name())
		}
		gr := metav1.GroupResource{
			Group:    parts[0],
			Resource: parts[1],
		}
		if gr.Group == apis.GroupName || gr.Group == rbacv1.GroupName || gr.Group == admissionregistration.GroupName {
			logger.Info(fmt.Sprintf("Skipping CustomResourceDefinition %s from %s", gr.String(), path))
			return nil
		}
		crd, err := readCustomResourceDefinition(path, gr)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", info.Name(), err)
		}
		crds[gr] = crd
		logger.Info(fmt.Sprintf("Loaded CustomResourceDefinition for %s from %s", gr.String(), path))
		return nil
	}); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("Loaded %d CustomResourceDefinitions", len(crds)))
	return crds, nil
}

func readCustomResourceDefinition(path string, gr metav1.GroupResource) (*apiextensionsv1.CustomResourceDefinition, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read CRD %s: %w", gr.String(), err)
	}

	expectedGvk := &schema.GroupVersionKind{Group: apiextensionsv1.GroupName, Version: "v1", Kind: "CustomResourceDefinition"}

	obj, gvk, err := extensionsapiserver.Codecs.UniversalDeserializer().Decode(raw, expectedGvk, &apiextensionsv1.CustomResourceDefinition{})
	if err != nil {
		return nil, fmt.Errorf("could not decode raw CRD %s: %w", gr.String(), err)
	}

	if !equality.Semantic.DeepEqual(gvk, expectedGvk) {
		return nil, fmt.Errorf("decoded CRD %s into incorrect GroupVersionKind, got %#v, wanted %#v", gr.String(), gvk, expectedGvk)
	}

	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return nil, fmt.Errorf("decoded CRD %s into incorrect type, got %T, wanted %T", gr.String(), obj, &apiextensionsv1.CustomResourceDefinition{})
	}

	return crd, nil
}

func loadAPIResourceSchemas(logger logr.Logger, baseDir string) (map[metav1.GroupResource]*apisv1alpha1.APIResourceSchema, error) {
	logger.Info(fmt.Sprintf("Loading APIResourceSchemas from %s", baseDir))
	apiResourceSchemas := map[metav1.GroupResource]*apisv1alpha1.APIResourceSchema{}
	if err := filepath.Walk(baseDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasPrefix(info.Name(), apiResourceSchemaNamePrefix) || filepath.Ext(info.Name()) != ".yaml" {
			return nil
		}

		parts := strings.SplitN(strings.TrimSuffix(strings.TrimPrefix(info.Name(), apiResourceSchemaNamePrefix), ".yaml"), ".", 2)
		if len(parts) != 2 {
			return fmt.Errorf("could not parse filename %s as %sresource.group.yaml", info.Name(), apiResourceSchemaNamePrefix)
		}
		gr := metav1.GroupResource{
			Group:    parts[1],
			Resource: parts[0],
		}
		apiResourceSchema, err := readAPIResourceSchema(path, gr)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", info.Name(), err)
		}
		apiResourceSchemas[gr] = apiResourceSchema
		logger.Info(fmt.Sprintf("Loaded APIResourceSchema for %s from %s", gr.String(), path))
		return nil
	}); err != nil {
		return nil, err
	}
	logger.Info(fmt.Sprintf("Loaded %d APIResourceSchemas", len(apiResourceSchemas)))
	return apiResourceSchemas, nil
}

func readAPIResourceSchema(path string, gr metav1.GroupResource) (*apisv1alpha1.APIResourceSchema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read APIResourceSchema %s: %w", gr.String(), err)
	}

	expectedGvk := &schema.GroupVersionKind{Group: apis.GroupName, Version: "v1alpha1", Kind: "APIResourceSchema"}

	obj, gvk, err := codecs.UniversalDeserializer().Decode(raw, expectedGvk, &apisv1alpha1.APIResourceSchema{})
	if err != nil {
		return nil, fmt.Errorf("could not decode raw APIResourceSchema %s: %w", gr.String(), err)
	}

	if !equality.Semantic.DeepEqual(gvk, expectedGvk) {
		return nil, fmt.Errorf("decoded APIResourceSchema %s into incorrect GroupVersionKind, got %#v, wanted %#v", gr.String(), gvk, expectedGvk)
	}

	apiResourceSchema, ok := obj.(*apisv1alpha1.APIResourceSchema)
	if !ok {
		return nil, fmt.Errorf("decoded APIResourceSchema %s into incorrect type, got %T, wanted %T", gr.String(), obj, &apisv1alpha1.APIResourceSchema{})
	}

	return apiResourceSchema, nil
}

func convertToSchemas(prefix string, crds map[metav1.GroupResource]*apiextensionsv1.CustomResourceDefinition) (map[metav1.GroupResource]*apisv1alpha1.APIResourceSchema, error) {
	apiResourceSchemas := map[metav1.GroupResource]*apisv1alpha1.APIResourceSchema{}
	for gr, crd := range crds {
		apiResourceSchema, err := apisv1alpha1.CRDToAPIResourceSchema(crd, prefix)
		if err != nil {
			return nil, err
		}
		apiResourceSchemas[gr] = apiResourceSchema
	}
	return apiResourceSchemas, nil
}

func resolveLatestAPIResourceSchemas(logger logr.Logger, previous, current map[metav1.GroupResource]*apisv1alpha1.APIResourceSchema) map[metav1.GroupResource]*apisv1alpha1.APIResourceSchema {
	resolved := map[metav1.GroupResource]*apisv1alpha1.APIResourceSchema{}
	for gr, currentSchema := range current {
		resolvedSchema := currentSchema
		if previousSchema, existed := previous[gr]; existed {
			if diff := cmp.Diff(previousSchema.Spec, currentSchema.Spec, compareSchemas()); diff == "" {
				logger.Info(fmt.Sprintf("Using previous APIResourceSchema for %s, as no changes were detected.", gr.String()))
				resolvedSchema = previousSchema
			}
		}
		resolved[gr] = resolvedSchema
	}
	return resolved
}

// compareSchemas compares JSON Schemas by unmarshalling them and comparing their values, instead
// of comparing their raw []byte() representations, as those are not semantically meaningful.
func compareSchemas() cmp.Option {
	return cmp.FilterPath(func(path cmp.Path) bool {
		return path.String() == "Versions.Schema.Raw"
	}, cmp.Comparer(func(a, b []byte) bool {
		var A, B apiextensionsv1.JSONSchemaProps
		if err := yaml.Unmarshal(a, &A); err != nil {
			panic(err)
		}
		if err := yaml.Unmarshal(b, &B); err != nil {
			panic(err)
		}
		return cmp.Diff(A, B) == ""
	}))
}

func generateExports(outputDir string, allSchemas map[metav1.GroupResource]*apisv1alpha1.APIResourceSchema) ([]*apisv1alpha1.APIExport, error) {
	byExport := map[string][]string{}
	for gr, apiResourceSchema := range allSchemas {
		if gr.Group == core.GroupName && gr.Resource == "logicalclusters" {
			continue
		} else if gr.Group == core.GroupName && gr.Resource == "shards" {
			// we export shards by themselves, not with the rest of the tenancy group
			byExport["shards."+core.GroupName] = []string{apiResourceSchema.Name}
		} else {
			byExport[gr.Group] = append(byExport[gr.Group], apiResourceSchema.Name)
		}
	}

	exports := make([]*apisv1alpha1.APIExport, 0, len(byExport))
	for exportName, schemas := range byExport {
		sort.Strings(schemas)

		export := apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: exportName,
			},
		}

		inputFilePath := filepath.Join(outputDir, fmt.Sprintf("%s%s.yaml", apiExportNamePrefix, exportName))
		if _, err := os.Stat(inputFilePath); err == nil {
			raw, err := os.ReadFile(inputFilePath)
			if err != nil {
				return nil, err
			}
			if err := yaml.Unmarshal(raw, &export); err != nil {
				return nil, fmt.Errorf("could not unmarshal APIExport manifest %s: %w", inputFilePath, err)
			}
		}

		export.Spec.LatestResourceSchemas = schemas

		exports = append(exports, &export)
	}

	return exports, nil
}

func writeObjects(logger logr.Logger, outputDir string, exports []*apisv1alpha1.APIExport, schemas map[metav1.GroupResource]*apisv1alpha1.APIResourceSchema) error {
	logger.Info(fmt.Sprintf("Writing %d manifests to %s", len(exports)+len(schemas), outputDir))

	codecs := serializer.NewCodecFactory(scheme)
	info, ok := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), runtime.ContentTypeYAML)
	if !ok {
		return fmt.Errorf("unsupported media type %q", runtime.ContentTypeYAML)
	}
	encoder := codecs.EncoderForVersion(info.Serializer, apisv1alpha1.SchemeGroupVersion)

	writtenExports := sets.String{}
	for _, export := range exports {
		out, err := runtime.Encode(encoder, export)
		if err != nil {
			return err
		}
		output := filepath.Join(outputDir, fmt.Sprintf("%s%s.yaml", apiExportNamePrefix, export.Name))
		if err := os.WriteFile(output, out, 0644); err != nil {
			return err
		}
		writtenExports.Insert(output)
		logger.Info(fmt.Sprintf("Wrote APIExport %s to %s", export.Name, output))
	}

	writtenSchemas := sets.String{}
	for gr, apiResourceSchema := range schemas {
		out, err := runtime.Encode(encoder, apiResourceSchema)
		if err != nil {
			return err
		}
		output := filepath.Join(outputDir, fmt.Sprintf("%s%s.yaml", apiResourceSchemaNamePrefix, gr.String()))
		if err := os.WriteFile(output, out, 0644); err != nil {
			return err
		}
		writtenSchemas.Insert(output)
		logger.Info(fmt.Sprintf("Wrote APIResourceSchema %s to %s", gr.String(), output))
	}

	logger.Info("Pruning output directory.")
	return filepath.Walk(outputDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(info.Name()) != ".yaml" {
			return nil
		}

		if strings.HasPrefix(info.Name(), apiExportNamePrefix) && !writtenExports.Has(path) {
			logger.Info(fmt.Sprintf("Pruning APIExport %s", path))
			if err := os.Remove(path); err != nil {
				return err
			}
		}

		if strings.HasPrefix(info.Name(), apiResourceSchemaNamePrefix) && !writtenSchemas.Has(path) {
			logger.Info(fmt.Sprintf("Pruning APIResourceSchema %s", path))
			if err := os.Remove(path); err != nil {
				return err
			}
		}
		return nil
	})
}
