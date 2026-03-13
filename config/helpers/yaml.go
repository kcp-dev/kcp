/*
Copyright 2026 The kcp Authors.

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
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

// ParseYAML parses the given YAML data into unstructured.Unstructured objects.
//
// Taken from init-agent:
// https://github.com/kcp-dev/init-agent/blob/1e747d414a1bd2417b77bef845f8c68487428890/internal/manifest/yaml.go#L29-L54
func ParseYAML(data []byte) ([]*unstructured.Unstructured, error) {
	var results []*unstructured.Unstructured

	d := yamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))

	for {
		doc, err := d.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading yaml doc: %w", err)
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		raw := map[string]any{}
		if err := yamlutil.Unmarshal(doc, raw); err != nil {
			return nil, fmt.Errorf("error decoding yaml doc: %w", err)
		}

		results = append(results, &unstructured.Unstructured{Object: raw})
	}

	return results, nil
}
