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

package migrationdump

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	clientv3 "go.etcd.io/etcd/client/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/storage/value"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"

	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpetcd "github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/virtual/migratingworkspaces"
)

var (
	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)
)

func init() {
	errorScheme.AddUnversionedTypes(metav1.Unversioned, &metav1.Status{})
}

// migratingClusters is a local interface for checking if a cluster is migrating.
type migratingClusters interface {
	IsMigrating(name logicalcluster.Name) bool
}

// Handler serves POST requests to HandlerPath by dumping every etcd entry
// that belongs to the cluster carried in the request context.
type Handler struct {
	etcdClient               *clientv3.Client
	etcdStoragePrefix        string
	resourceTransformers     value.ResourceTransformers
	migratingLogicalClusters migratingClusters
}

func NewHandler(
	etcdClient *clientv3.Client,
	etcdStoragePrefix string,
	resourceTransformers value.ResourceTransformers,
	migratingLogicalClusters migratingClusters,
) *Handler {
	return &Handler{
		etcdClient:               etcdClient,
		etcdStoragePrefix:        etcdStoragePrefix,
		resourceTransformers:     resourceTransformers,
		migratingLogicalClusters: migratingLogicalClusters,
	}
}

func (h *Handler) Close() error {
	return h.etcdClient.Close()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	isDigest := r.URL.Path == migratingworkspaces.DigestHandlerPath
	resource := migrationv1alpha1.Resource("logicalclusterdumps")
	if isDigest {
		resource = migrationv1alpha1.Resource("logicalclusterdigests")
	}

	if r.Method != http.MethodPost {
		writeError(w, r, apierrors.NewMethodNotSupported(
			resource,
			r.Method,
		))
		return
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Name.Empty() {
		writeError(w, r, apierrors.NewBadRequest("no cluster in context"))
		return
	}

	user, ok := genericapirequest.UserFrom(ctx)
	if !ok || user == nil {
		writeError(w, r, apierrors.NewUnauthorized("no user info"))
		return
	}
	if !slices.Contains(user.GetGroups(), bootstrappolicy.SystemExternalLogicalClusterAdmin) {
		writeError(w, r, apierrors.NewForbidden(
			resource,
			"",
			fmt.Errorf("user is not in group %s", bootstrappolicy.SystemExternalLogicalClusterAdmin),
		))
		return
	}

	if !h.migratingLogicalClusters.IsMigrating(cluster.Name) {
		writeError(w, r, apierrors.NewNotFound(
			resource,
			string(cluster.Name),
		))
		return
	}

	if isDigest {
		h.serveDigest(ctx, w, r, cluster.Name)
		return
	}
	h.serveDump(ctx, w, r, cluster.Name)
}

func (h *Handler) serveDump(ctx context.Context, w http.ResponseWriter, r *http.Request, clusterName logicalcluster.Name) {
	logger := klog.FromContext(ctx)

	// Decode the request body. Spec is empty today so we don't actually
	// need anything from it, but parsing it ensures the client sent
	// something resembling the expected type.
	var dump migrationv1alpha1.LogicalClusterDump
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&dump); err != nil {
			writeError(w, r, apierrors.NewBadRequest(fmt.Sprintf("failed to decode request body: %v", err)))
			return
		}
	}

	if dump.Spec.Encoding != migrationv1alpha1.DumpEncodingPlaintext {
		writeError(w, r, apierrors.NewBadRequest("logical cluster dumps require Plaintext encoding"))
		return
	}

	logger.V(2).Info("dumping logical cluster page from etcd", "cluster", clusterName, "continue", dump.Spec.Continue)

	entries, nextContinue, err := kcpetcd.ScanEtcdEntries(ctx, h.etcdClient, h.etcdClient, h.etcdStoragePrefix, clusterName, dump.Spec.Continue, "", dump.Spec.Limit, dump.Spec.MaxBytes, h.resourceTransformers)
	if err != nil {
		writeError(w, r, apierrors.NewInternalError(fmt.Errorf("failed to dump logical cluster: %w", err)))
		return
	}

	resp := &migrationv1alpha1.LogicalClusterDump{
		TypeMeta: metav1.TypeMeta{
			APIVersion: migrationv1alpha1.SchemeGroupVersion.String(),
			Kind:       "LogicalClusterDump",
		},
		ObjectMeta: dump.ObjectMeta,
		Status: migrationv1alpha1.LogicalClusterDumpStatus{
			Encoding: migrationv1alpha1.DumpEncodingPlaintext,
			Entries:  entries,
			Continue: nextContinue,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Error(err, "failed to write dump response")
	}
}

func (h *Handler) serveDigest(ctx context.Context, w http.ResponseWriter, r *http.Request, clusterName logicalcluster.Name) {
	logger := klog.FromContext(ctx)

	var digest migrationv1alpha1.LogicalClusterDigest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&digest); err != nil {
			writeError(w, r, apierrors.NewBadRequest(fmt.Sprintf("failed to decode request body: %v", err)))
			return
		}
	}

	logger.V(2).Info("computing logical cluster digest page from etcd", "cluster", clusterName, "continue", digest.Spec.Continue)

	entries, nextContinue, err := kcpetcd.ScanEtcdDigests(ctx, h.etcdClient, h.etcdClient, h.etcdStoragePrefix, clusterName, digest.Spec.Continue, "", digest.Spec.Limit, h.resourceTransformers)
	if err != nil {
		writeError(w, r, apierrors.NewInternalError(fmt.Errorf("failed to compute logical cluster digest: %w", err)))
		return
	}

	resp := &migrationv1alpha1.LogicalClusterDigest{
		TypeMeta: metav1.TypeMeta{
			APIVersion: migrationv1alpha1.SchemeGroupVersion.String(),
			Kind:       "LogicalClusterDigest",
		},
		ObjectMeta: digest.ObjectMeta,
		Status: migrationv1alpha1.LogicalClusterDigestStatus{
			Entries:  entries,
			Continue: nextContinue,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Error(err, "failed to write digest response")
	}
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	responsewriters.ErrorNegotiated(err, errorCodecs, schema.GroupVersion{}, w, r)
}
