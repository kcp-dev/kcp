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
	"bytes"
	"fmt"
)

// TransformFileFunc transforms a resource file before being applied to the cluster.
type TransformFileFunc func(bs []byte) ([]byte, error)

// Option allows to customize the bootstrap process.
type Option struct {
	// TransformFileFunc is a function that transforms a resource file before being applied to the cluster.
	TransformFile TransformFileFunc
}

// ReplaceOption allows to customize the bootstrap process.
func ReplaceOption(pairs ...string) Option {
	return Option{
		TransformFile: func(bs []byte) ([]byte, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("odd number of arguments: %v", pairs)
			}
			for i := 0; i < len(pairs); i += 2 {
				bs = bytes.ReplaceAll(bs, []byte(pairs[i]), []byte(pairs[i+1]))
			}
			return bs, nil
		},
	}
}

func applyTransformers(raw []byte, transformers ...TransformFileFunc) ([]byte, error) {
	var err error
	for _, transformer := range transformers {
		raw, err = transformer(raw)
		if err != nil {
			return nil, err
		}
	}
	return raw, nil
}
