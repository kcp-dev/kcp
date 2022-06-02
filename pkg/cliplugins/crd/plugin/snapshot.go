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

package plugin

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

type CRDSnapshot struct {
	options *Options
}

func NewCRDSnapshot(opts *Options) *CRDSnapshot {
	return &CRDSnapshot{
		options: opts,
	}
}

func (c *CRDSnapshot) Execute() error {
	var in io.Reader

	if c.options.Filename == "-" {
		in = c.options.In
	} else {
		f, err := os.Open(c.options.Filename)
		if err != nil {
			return fmt.Errorf("error opening %s: %w", c.options.Filename, err)
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
	switch c.options.OutputFormat {
	case "json":
		mediaType = runtime.ContentTypeJSON
	case "yaml":
		mediaType = runtime.ContentTypeYAML
	default:
		return fmt.Errorf("unsupported output format %q", c.options.OutputFormat)
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

		apiResourceSchema, err := apisv1alpha1.CRDToAPIResourceSchema(crd, c.options.Prefix)
		if err != nil {
			return fmt.Errorf("error converting CRD: %w", err)
		}

		out, err := runtime.Encode(encoder, apiResourceSchema)
		if err != nil {
			return fmt.Errorf("error converting CRD to an APIResourceSchema: %w", err)
		}

		fmt.Fprintln(c.options.Out, string(out))
		fmt.Fprintln(c.options.Out, "---")
	}

	return nil
}
