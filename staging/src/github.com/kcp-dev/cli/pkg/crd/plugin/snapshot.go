/*
Copyright 2022 The kcp Authors.

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

package plugin

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/kcp-dev/cli/pkg/base"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

// SnapshotOptions contains options for the snapshot command.
type SnapshotOptions struct {
	*base.Options

	Filename     string
	Prefix       string
	OutputFormat string
}

// NewSnapshotOptions provides an instance of SnapshotOptions with default values.
func NewSnapshotOptions(streams genericclioptions.IOStreams) *SnapshotOptions {
	o := &SnapshotOptions{
		Options:      base.NewOptions(streams),
		OutputFormat: "yaml",
	}

	o.OptOutOfDefaultKubectlFlags = true

	return o
}

// BindFlags binds the arguments common to all sub-commands,
// to the corresponding main command flags.
func (o *SnapshotOptions) BindFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&o.Filename, "filename", "f", o.Filename, "Path to a file containing the CRD to convert to an APIResourceSchema, or - for stdin")
	cmd.Flags().StringVar(&o.Prefix, "prefix", o.Prefix, "Prefix to use for the APIResourceSchema's name, before <resource>.<group>")
	cmd.Flags().StringVarP(&o.OutputFormat, "output", "o", o.OutputFormat, "Output format. Valid values are 'json' and 'yaml'")
}

func (o *SnapshotOptions) Validate() error {
	var errs []error

	if err := o.Options.Validate(); err != nil {
		errs = append(errs, err)
	}

	if o.Filename == "" {
		errs = append(errs, fmt.Errorf("--filename is required"))
	}

	if o.Prefix == "" {
		errs = append(errs, fmt.Errorf("--prefix is required"))
	}

	if o.OutputFormat != "json" && o.OutputFormat != "yaml" {
		errs = append(errs, fmt.Errorf("invalid value %q for --output; valid values are json, yaml", o.OutputFormat))
	}

	return utilerrors.NewAggregate(errs)
}

func (o *SnapshotOptions) Run() error {
	var in io.Reader

	if o.Filename == "-" {
		in = o.In
	} else {
		f, err := os.Open(o.Filename)
		if err != nil {
			return fmt.Errorf("error opening %s: %w", o.Filename, err)
		}

		defer f.Close()

		in = f
	}

	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	codecs := serializer.NewCodecFactory(scheme)

	var mediaType string
	switch o.OutputFormat {
	case "json":
		mediaType = runtime.ContentTypeJSON
	case "yaml":
		mediaType = runtime.ContentTypeYAML
	default:
		return fmt.Errorf("unsupported output format %q", o.OutputFormat)
	}

	info, ok := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), mediaType)
	if !ok {
		return fmt.Errorf("unsupported media type %q", mediaType)
	}

	encoder := codecs.EncoderForVersion(info.Serializer, apisv1alpha1.SchemeGroupVersion)

	d := kubeyaml.NewYAMLReader(bufio.NewReader(in))

	for {
		doc, err := d.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}

		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		decoded, _, err := codecs.UniversalDecoder(apiextensionsv1.SchemeGroupVersion).Decode(doc, nil, nil)
		if err != nil {
			return err
		}

		crd, ok := decoded.(*apiextensionsv1.CustomResourceDefinition)
		if !ok {
			return fmt.Errorf("unexpected type for CRD %T", decoded)
		}

		apiResourceSchema, err := apisv1alpha1.CRDToAPIResourceSchema(crd, o.Prefix)
		if err != nil {
			return fmt.Errorf("error converting CRD: %w", err)
		}

		out, err := runtime.Encode(encoder, apiResourceSchema)
		if err != nil {
			return fmt.Errorf("error converting CRD to an APIResourceSchema: %w", err)
		}

		fmt.Fprintln(o.Out, string(out))
		fmt.Fprintln(o.Out, "---")
	}

	return nil
}
