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

package Webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"sync"
	"testing"

	v1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

type WebhookServer struct {
	Response     v1.AdmissionResponse
	ObjectGVK    schema.GroupVersionKind
	T            *testing.T
	Deserializer runtime.Decoder

	port string

	Lock  sync.Mutex
	Calls int
}

func (t *WebhookServer) StartServer(ctx context.Context, server framework.RunningServer, port string) {
	dirPath := filepath.Dir(server.KubeconfigPath())
	// using known path to cert and key
	cfg := server.DefaultConfig(t.T)
	cfg.CertFile = filepath.Join(dirPath, "apiserver.crt")
	cfg.KeyFile = filepath.Join(dirPath, "apiserver.key")
	t.port = port
	serv := &http.Server{Addr: fmt.Sprintf(":%v", port), Handler: t}
	go func() {
		<-ctx.Done()
		fmt.Printf("Shutting down the HTTP server")
		err := serv.Shutdown(context.TODO())
		if err != nil {
			fmt.Printf("unable to shutdown server gracefully err: %v", err)
		}
	}()
	go func() {
		err := serv.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile)
		if err != nil && err != http.ErrServerClosed {
			fmt.Printf("unable to shutdown server gracefully err: %v", err)
		}
	}()
}

func (t *WebhookServer) GetURL() *string {
	s := fmt.Sprintf("https://localhost:%v/hello", t.port)
	return &s
}

func (t *WebhookServer) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	// Make sure that this is a request for the object that was set.
	t.T.Log("made it webhook")
	if req.Body == nil {
		msg := "Expected request body to be non-empty"
		t.T.Logf("%v", msg)
		http.Error(resp, msg, http.StatusBadRequest)
	}

	data, err := ioutil.ReadAll(req.Body)
	if err != nil {
		msg := fmt.Sprintf("Request could not be decoded: %v", err)
		t.T.Logf("%v", msg)
		http.Error(resp, msg, http.StatusBadRequest)
	}

	// verify the content type is accurate
	contentType := req.Header.Get("Content-Type")
	if contentType != "application/json" {
		msg := fmt.Sprintf("contentType=%s, expect application/json", contentType)
		t.T.Logf("%v", msg)
		http.Error(resp, msg, http.StatusBadRequest)
		return
	}

	obj, gvk, err := t.Deserializer.Decode(data, nil, nil)
	if err != nil {
		msg := fmt.Sprintf("Unable to decode object: %v", err)
		t.T.Logf("%v", msg)
		http.Error(resp, msg, http.StatusBadRequest)
		return
	}

	if *gvk != v1.SchemeGroupVersion.WithKind("AdmissionReview") {
		msg := fmt.Sprintf("Expected AdmissionReview but got: %T", obj)
		t.T.Logf("%v", msg)
		http.Error(resp, msg, http.StatusBadRequest)
		return
	}
	requestedAdmissionReview, ok := obj.(*v1.AdmissionReview)
	if !ok {
		//return an error
		msg := fmt.Sprintf("Expected AdmissionReview but got: %T", obj)
		t.T.Logf("%v", msg)
		http.Error(resp, msg, http.StatusBadRequest)
		return
	}
	obj, objGVK, err := t.Deserializer.Decode(requestedAdmissionReview.Request.Object.Raw, nil, nil)
	if err != nil {
		msg := fmt.Sprintf("Unable to decode admissions reqeusted object: %v", err)
		t.T.Logf("%v", msg)
		http.Error(resp, msg, http.StatusBadRequest)
		return
	}

	if t.ObjectGVK != *objGVK {
		//return an error
		msg := fmt.Sprintf("Expected ObjectGVK: %v but got: %T", t.ObjectGVK, obj)
		t.T.Logf("%v", msg)
		http.Error(resp, msg, http.StatusBadRequest)
		return
	}

	responseAdmissionReview := &v1.AdmissionReview{
		TypeMeta: requestedAdmissionReview.TypeMeta,
	}
	responseAdmissionReview.Response = &t.Response
	responseAdmissionReview.Response.UID = requestedAdmissionReview.Request.UID

	respBytes, err := json.Marshal(responseAdmissionReview)
	if err != nil {
		t.T.Logf("%v", err)
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	t.Lock.Lock()
	t.Calls = t.Calls + 1
	t.Lock.Unlock()

	resp.Header().Set("Content-Type", "application/json")
	if _, err := resp.Write(respBytes); err != nil {
		t.T.Logf("%v", err)
	}
}
