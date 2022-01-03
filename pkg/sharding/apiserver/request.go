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

package apiserver

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/rest"
)

// requestFor is a factory for *rest.Request from the source *http.Request, ensuring the source is not mutated
func requestFor(req *http.Request) func(client *rest.RESTClient, mutators ...func(runtime.Object) error) (*rest.Request, error) {
	return func(client *rest.RESTClient, mutators ...func(runtime.Object) error) (*rest.Request, error) {
		body, err := ioutil.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		if err := req.Body.Close(); err != nil {
			return nil, err
		}
		if len(mutators) > 0 && len(body) > 0 {
			// mutate the body as needed, using unstructured as our intermediate decoding
			obj, _, err := unstructured.UnstructuredJSONScheme.Decode(body, nil, nil)
			if err != nil {
				return nil, fmt.Errorf("could not decode body as unstructured: %w", err)
			}
			for _, mutator := range mutators {
				if err := mutator(obj); err != nil {
					return nil, fmt.Errorf("could not mutate body: %w", err)
				}
			}
			encoded := &bytes.Buffer{}
			if err := unstructured.UnstructuredJSONScheme.Encode(obj, encoded); err != nil {
				return nil, fmt.Errorf("could not encode mutated body: %w", err)
			}
			body = encoded.Bytes()
		}
		request := rest.NewRequest(client).
			Verb(req.Method).
			RequestURI(req.URL.RequestURI()).
			SetHeader("X-Kubernetes-Sharded-Request", "true").
			Body(body).Debug()
		if len(body) > 0 {
			// usually, the `.Body()` call would figure out the correct encoding for the
			// object that is being passed along on the wire, but we're not decoding it here
			// nor do we necessarily have the types registered anyway, so we should just
			// trust that the end client set the right thing as we're forwarding it blindly
			request.SetHeader("Content-Type", req.Header.Get("Content-Type"))
		}
		return request, nil
	}
}

// clientFor is a factory for *rest.RESTClient from the *rest.Config, ensuring that the source is not mutated
func clientFor(userInfo user.Info, negotiatedSerializer runtime.NegotiatedSerializer) func(cfg *rest.Config) (*rest.RESTClient, error) {
	return func(cfg *rest.Config) (*rest.RESTClient, error) {
		// until we have a central authority for authn data, we're sitting behind the auth stack for the
		// proximal kcp the client hit and using impersonation to make it look like everyone's in agreement
		cloned := rest.CopyConfig(cfg)
		cloned.Impersonate = rest.ImpersonationConfig{
			UserName: userInfo.GetName(),
			Groups:   userInfo.GetGroups(),
			Extra:    userInfo.GetExtra(),
		}
		cloned.ContentConfig.NegotiatedSerializer = negotiatedSerializer
		// we handle conversions on our end for the client
		cloned.ContentConfig.ContentType = "application/json"
		return rest.UnversionedRESTClientFor(cloned)
	}
}
