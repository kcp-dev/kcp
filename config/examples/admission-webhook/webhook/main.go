/*
Copyright 2025 The kcp Authors.

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

// A minimal validating admission webhook server for the cowboys API.
//
// Rule enforced: a Cowboy's spec.intent must NOT be "bad".
//
// Stdlib only -- it decodes the AdmissionReview as generic JSON so there are no
// Kubernetes dependencies to download.
//
//	go run . --tls-cert=tls.crt --tls-key=tls.key --addr=:9443
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"time"
)

func main() {
	var certFile, keyFile, addr string
	flag.StringVar(&certFile, "tls-cert", "tls.crt", "path to TLS serving cert")
	flag.StringVar(&keyFile, "tls-key", "tls.key", "path to TLS serving key")
	flag.StringVar(&addr, "addr", ":9443", "listen address")
	flag.Parse()

	http.HandleFunc("/validate", validate)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	log.Printf("cowboy validating webhook listening on %s", addr)
	log.Fatal(srv.ListenAndServeTLS(certFile, keyFile))
}

func validate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var review map[string]any
	if err := json.Unmarshal(body, &review); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	req, _ := review["request"].(map[string]any)
	uid, _ := req["uid"].(string)
	obj, _ := req["object"].(map[string]any)
	meta, _ := obj["metadata"].(map[string]any)
	spec, _ := obj["spec"].(map[string]any)

	name, _ := meta["name"].(string)
	intent, _ := spec["intent"].(string)

	// kcp stamps the object with the consumer workspace it actually lives in.
	cluster := ""
	if ann, ok := meta["annotations"].(map[string]any); ok {
		cluster, _ = ann["kcp.io/cluster"].(string)
	}
	log.Printf("reviewing cowboy %q from workspace cluster %q intent=%q", name, cluster, intent)

	resp := map[string]any{"uid": uid, "allowed": true}
	if intent == "bad" {
		resp["allowed"] = false
		resp["status"] = map[string]any{
			"code":    403,
			"message": "cowboy \"" + name + "\" rejected: spec.intent must not be \"bad\" (workspace " + cluster + ")",
		}
	}

	out := map[string]any{
		"apiVersion": "admission.k8s.io/v1",
		"kind":       "AdmissionReview",
		"response":   resp,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("error writing response: %v", err)
	}
}
