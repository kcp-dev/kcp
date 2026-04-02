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

package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	authorizationv1 "k8s.io/api/authorization/v1"
)

// AuthzHandlerFunc is a function that generates a response for a SubjectAccessReview.
type AuthzHandlerFunc func(authorizationv1.SubjectAccessReview) (authorizationv1.SubjectAccessReview, error)

// AuthzHandler is an http.Handler that processes Kubernetes authorization webhook requests.
type AuthzHandler struct {
	mu      sync.RWMutex
	handler AuthzHandlerFunc
}

// NewAuthzHandler creates a new authorization webhook handler with an initial handler function.
func NewAuthzHandler(handler AuthzHandlerFunc) *AuthzHandler {
	return &AuthzHandler{
		handler: handler,
	}
}

// SetHandler changes the handler function at runtime.
func (h *AuthzHandler) SetHandler(handler AuthzHandlerFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handler = handler
}

func (h *AuthzHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read body: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var sar authorizationv1.SubjectAccessReview
	if err := json.Unmarshal(body, &sar); err != nil {
		http.Error(w, fmt.Sprintf("Failed to unmarshal request: %v", err), http.StatusBadRequest)
		return
	}

	h.mu.RLock()
	handler := h.handler
	h.mu.RUnlock()

	response, err := handler(sar)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to generate response: %v", err), http.StatusInternalServerError)
		return
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to marshal response: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(responseBytes)
}

// Allow returns a handler function that always allows requests.
func Allow(sar authorizationv1.SubjectAccessReview) (authorizationv1.SubjectAccessReview, error) {
	sar.Status = authorizationv1.SubjectAccessReviewStatus{
		Allowed: true,
		Denied:  false,
	}
	return sar, nil
}

// Deny returns a handler function that always denies requests.
func Deny(sar authorizationv1.SubjectAccessReview) (authorizationv1.SubjectAccessReview, error) {
	sar.Status = authorizationv1.SubjectAccessReviewStatus{
		Allowed: false,
		Denied:  true,
	}
	return sar, nil
}

// NoOpinion returns a handler function that expresses no opinion (neither allows nor denies).
func NoOpinion(sar authorizationv1.SubjectAccessReview) (authorizationv1.SubjectAccessReview, error) {
	sar.Status = authorizationv1.SubjectAccessReviewStatus{
		Allowed: false,
		Denied:  false,
	}
	return sar, nil
}
