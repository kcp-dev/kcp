/*
Copyright 2021 The KCP Authors.

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
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"time"

	"k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apiextensions-apiserver/pkg/crdserverscheme"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/runtime/serializer/versioning"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/net"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilwaitgroup "k8s.io/apimachinery/pkg/util/waitgroup"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/metrics"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/features"
	"k8s.io/apiserver/pkg/registry/rest"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	clientrest "k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"k8s.io/kubernetes/pkg/printers"
	"k8s.io/kubernetes/pkg/printers/storage"
)

func NewShardedHandler(clients map[string]*clientrest.Config, maxRequestBodyBytes int64, requestTimeout time.Duration) *ShardedHandler {
	return &ShardedHandler{
		clients:             clients,
		wg:                  &utilwaitgroup.SafeWaitGroup{},
		maxRequestBodyBytes: maxRequestBodyBytes,
		requestTimeout:      requestTimeout,
	}
}

type ShardedHandler struct {
	clients             map[string]*clientrest.Config
	wg                  *utilwaitgroup.SafeWaitGroup
	maxRequestBodyBytes int64
	requestTimeout      time.Duration
}

// Follow-ups:
// - for scale, can we
//  - write representative scale tests
//  - reduce the number of per-request allocations
//  - reduce the amount of body copy / deserialization
// - investigate a simpler/new impl for serving a watch stream
//  - as a follow-up, can we interact without the RequestScope stuff
// - investigate how 1k shards can be supported in a header (limit 1024 chars)

// run the e2e in CI

func (h *ShardedHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	scheme := runtime.NewScheme()
	codecs := serializer.NewCodecFactory(scheme)

	unversionedVersion := schema.GroupVersion{Group: "", Version: "v1"}
	unversionedTypes := []runtime.Object{
		&metav1.Status{},
		&metav1.WatchEvent{},
		&metav1.APIVersions{},
		&metav1.APIGroupList{},
		&metav1.APIGroup{},
		&metav1.APIResourceList{},
	}
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "", Version: "v1"})
	scheme.AddUnversionedTypes(unversionedVersion, unversionedTypes...)

	requestInfo, ok := request.RequestInfoFrom(req.Context())
	if !ok {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("missing requestInfo")),
			codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
		return
	}
	equivalentResourceRegistry := runtime.NewEquivalentResourceRegistry()
	groupVersion := schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}
	resource := groupVersion.WithResource(requestInfo.Resource)

	scheme.AddUnversionedTypes(groupVersion, unversionedTypes...)
	parameterCodec := runtime.NewParameterCodec(scheme)

	negotiatedSerializer := &unstructuredNegotiatedSerializer{
		scheme: &delegatingUnstructuredScheme{
			delegate: scheme,
		},
	}

	// TODO: or, trust the delegate servers with table-writing and just pass along what they give?
	tableGenerator := printers.NewTableGenerator()
	if err := tableGenerator.DefaultHandler([]metav1.TableColumnDefinition{
		{Name: "cluster", Type: "string", Description: metav1.ObjectMeta{}.SwaggerDoc()["cluster"], Priority: 0},
		{Name: "namespace", Type: "string", Description: metav1.ObjectMeta{}.SwaggerDoc()["namespace"], Priority: 0},
		{Name: "name", Type: "string", Format: "name", Description: metav1.ObjectMeta{}.SwaggerDoc()["name"], Priority: 0},
	}, func(object runtime.Object, options printers.GenerateOptions) ([]metav1.TableRow, error) {
		row := func(obj runtime.Object) (metav1.TableRow, error) {
			switch m := obj.(type) {
			case metav1.Object:
				return metav1.TableRow{
					Cells:  []interface{}{m.GetClusterName(), m.GetNamespace(), m.GetName()},
					Object: runtime.RawExtension{Object: obj},
				}, nil
			default:
				return metav1.TableRow{}, fmt.Errorf("could not create table rows from %T", obj)
			}
		}

		var rows []metav1.TableRow
		switch t := object.(type) {
		case *unstructured.Unstructured:
			r, err := row(t)
			if err != nil {
				return nil, err
			}
			rows = append(rows, r)
		case *unstructured.UnstructuredList:
			for i := range t.Items {
				r, err := row(&t.Items[i])
				if err != nil {
					return nil, err
				}
				rows = append(rows, r)
			}
		default:
			return nil, fmt.Errorf("object was not unstructured, but %T", object)
		}

		return rows, nil
	}); err != nil {
		utilruntime.HandleError(err)
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(err),
			codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
		return
	}
	tableConverter := storage.TableConvertor{TableGenerator: tableGenerator}

	nopAuthorizer := authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
		return authorizer.DecisionAllow, "", nil
	})

	requestScope := &handlers.RequestScope{
		Namer: nopScopeNamer{
			selfLinker: meta.NewAccessor(),
		},
		Serializer:                  negotiatedSerializer,
		ParameterCodec:              parameterCodec,
		StandardSerializers:         negotiatedSerializer.SupportedMediaTypes(),
		Creater:                     apiserver.UnstructuredCreator{},
		Convertor:                   nopConverter{},
		Defaulter:                   nopDefaulter{},
		Typer:                       crdserverscheme.NewUnstructuredObjectTyper(),
		UnsafeConvertor:             nopConverter{},
		Authorizer:                  nopAuthorizer,
		EquivalentResourceMapper:    equivalentResourceRegistry,
		TableConvertor:              tableConverter,
		Resource:                    resource,
		Subresource:                 requestInfo.Subresource,
		MetaGroupVersion:            metav1.SchemeGroupVersion,
		HubGroupVersion:             groupVersion,
		MaxRequestBodyBytes:         h.maxRequestBodyBytes,
		AcceptsGroupVersionDelegate: everythingAcceptor{},
	}

	nopAdmission := admission.NewHandler()

	userInfo, ok := request.UserFrom(req.Context())
	if !ok {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("missing userInfo")),
			codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
		return
	}

	// some handlers like patch end up reading the body before we get a chance to handle things, so
	// we need to deep-copy the request as best we can so that we can forward the requests along
	// TODO: is there a better way?
	cloned := net.CloneRequest(req)
	// copied from DumpRequest
	if req.Body == nil || req.Body == http.NoBody {
		// No copying needed. Preserve the magic sentinel meaning of NoBody.
		req.Body = http.NoBody
		cloned.Body = http.NoBody
	} else {
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(req.Body); err != nil {
			responsewriters.ErrorNegotiated(
				apierrors.NewInternalError(fmt.Errorf("failed reading request body")),
				codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
			)
			return
		}
		if err := req.Body.Close(); err != nil {
			responsewriters.ErrorNegotiated(
				apierrors.NewInternalError(fmt.Errorf("failed closing request body")),
				codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
			)
			return
		}
		req.Body = io.NopCloser(&buf)
		cloned.Body = io.NopCloser(bytes.NewReader(buf.Bytes()))
	}

	klog.V(10).Info("v=== INCOMING ===v")
	v, e := httputil.DumpRequest(cloned, true)
	klog.V(10).Info(string(v))
	if e != nil {
		klog.Error(e)
	}
	klog.V(10).Info("^=== INCOMING ===^")

	s := NewMux(storageBase{
		TableConvertor:                 tableConverter,
		shards:                         h.clients,
		shardIdentifierResourceVersion: 1,
		requestFor:                     requestFor(cloned),
		clientFor:                      clientFor(userInfo, negotiatedSerializer),
	})

	var handlerFunc http.HandlerFunc
	switch requestInfo.Verb {
	case "get":
		handlerFunc = handlers.GetResource(s, requestScope)
	case "list":
		forceWatch := false
		handlerFunc = handlers.ListResource(s, s, requestScope, forceWatch, h.requestTimeout)
	case "watch":
		forceWatch := true
		handlerFunc = handlers.ListResource(s, s, requestScope, forceWatch, h.requestTimeout)
	case "create":
		handlerFunc = handlers.CreateResource(s, requestScope, nopAdmission)
	case "update":
		handlerFunc = handlers.UpdateResource(s, requestScope, nopAdmission)
	case "patch":
		supportedTypes := []string{
			string(types.JSONPatchType),
			string(types.MergePatchType),
		}
		if legacyscheme.Scheme.IsGroupRegistered(requestInfo.APIGroup) {
			supportedTypes = append(supportedTypes, string(types.StrategicMergePatchType))
		}
		if utilfeature.DefaultFeatureGate.Enabled(features.ServerSideApply) {
			supportedTypes = append(supportedTypes, string(types.ApplyPatchType))
		}
		handlerFunc = handlers.PatchResource(s, requestScope, nopAdmission, supportedTypes)
	case "delete":
		allowsOptions := true
		handlerFunc = handlers.DeleteResource(s, allowsOptions, requestScope, nopAdmission)
	case "deletecollection":
		checkBody := true
		handlerFunc = handlers.DeleteCollection(s, checkBody, requestScope, nopAdmission)
	default:
		responsewriters.ErrorNegotiated(
			apierrors.NewMethodNotSupported(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Verb),
			codecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, req,
		)
		handlerFunc = nil
	}
	if handlerFunc != nil {
		handlerFunc = metrics.InstrumentHandlerFunc(requestInfo.Verb, requestInfo.APIGroup, requestInfo.APIVersion, requestInfo.Resource, requestInfo.Subresource, metrics.CleanScope(requestInfo), metrics.APIServerComponent, false, "", handlerFunc)
		handlerFunc(w, req)
		return
	}
}

type everythingAcceptor struct{}

func (e everythingAcceptor) AcceptsGroupVersion(gv schema.GroupVersion) bool {
	return true
}

var _ rest.GroupVersionAcceptor = everythingAcceptor{}

type unstructuredNegotiatedSerializer struct {
	scheme *delegatingUnstructuredScheme
}

func (s unstructuredNegotiatedSerializer) SupportedMediaTypes() []runtime.SerializerInfo {
	return []runtime.SerializerInfo{
		{
			MediaType:        "application/json",
			MediaTypeType:    "application",
			MediaTypeSubType: "json",
			EncodesAsText:    true,
			Serializer:       json.NewSerializer(json.DefaultMetaFactory, s.scheme, s.scheme, false),
			PrettySerializer: json.NewSerializer(json.DefaultMetaFactory, s.scheme, s.scheme, true),
			StreamSerializer: &runtime.StreamSerializerInfo{
				EncodesAsText: true,
				Serializer:    json.NewSerializer(json.DefaultMetaFactory, s.scheme, s.scheme, false),
				Framer:        json.Framer,
			},
		},
		{
			MediaType:        "application/yaml",
			MediaTypeType:    "application",
			MediaTypeSubType: "yaml",
			EncodesAsText:    true,
			Serializer:       json.NewYAMLSerializer(json.DefaultMetaFactory, s.scheme, s.scheme),
		},
	}
}

func (s unstructuredNegotiatedSerializer) EncoderForVersion(encoder runtime.Encoder, gv runtime.GroupVersioner) runtime.Encoder {
	return versioning.NewCodec(encoder, nil, s.scheme, s.scheme, s.scheme, nil, nopGroupVersioner{}, nil, "shardedNegotiatedSerializer")
}

func (s unstructuredNegotiatedSerializer) DecoderToVersion(decoder runtime.Decoder, gv runtime.GroupVersioner) runtime.Decoder {
	return versioning.NewCodec(nil, decoder, s.scheme, s.scheme, s.scheme, nil, nil, nopGroupVersioner{}, "shardedNegotiatedDeserializer")
}

var _ runtime.NegotiatedSerializer = unstructuredNegotiatedSerializer{}

type nopGroupVersioner struct{}

func (n nopGroupVersioner) KindForGroupVersionKinds(kinds []schema.GroupVersionKind) (target schema.GroupVersionKind, ok bool) {
	return schema.GroupVersionKind{}, true
}

func (n nopGroupVersioner) Identifier() string {
	return ""
}

var _ runtime.GroupVersioner = nopGroupVersioner{}

type nopConverter struct{}

func (u nopConverter) Convert(in, out, context interface{}) error {
	sv, err := conversion.EnforcePtr(in)
	if err != nil {
		return err
	}
	dv, err := conversion.EnforcePtr(out)
	if err != nil {
		return err
	}
	dv.Set(sv)
	return nil
}

func (u nopConverter) ConvertToVersion(in runtime.Object, gv runtime.GroupVersioner) (out runtime.Object, err error) {
	return in, nil
}

func (u nopConverter) ConvertFieldLabel(gvk schema.GroupVersionKind, label, value string) (string, string, error) {
	return label, value, nil
}

var _ runtime.ObjectConvertor = nopConverter{}

type nopScopeNamer struct {
	selfLinker runtime.SelfLinker
}

func (n nopScopeNamer) Namespace(req *http.Request) (namespace string, err error) {
	namespace, _, err = n.Name(req)
	return namespace, err
}

func (n nopScopeNamer) Name(req *http.Request) (namespace, name string, err error) {
	requestInfo, ok := request.RequestInfoFrom(req.Context())
	if !ok {
		return "", "", fmt.Errorf("missing requestInfo")
	}
	return requestInfo.Namespace, requestInfo.Name, nil
}

func (n nopScopeNamer) ObjectName(obj runtime.Object) (namespace, name string, err error) {
	name, err = n.selfLinker.Name(obj)
	if err != nil {
		return "", "", err
	}
	namespace, err = n.selfLinker.Namespace(obj)
	if err != nil {
		return "", "", err
	}
	return namespace, name, err
}

func (n nopScopeNamer) SetSelfLink(obj runtime.Object, url string) error {
	// we are not going to mutate things...
	return nil
}

func (n nopScopeNamer) GenerateLink(requestInfo *request.RequestInfo, obj runtime.Object) (uri string, err error) {
	// this is only called so we can set it later...
	return "", nil
}

func (n nopScopeNamer) GenerateListLink(req *http.Request) (uri string, err error) {
	// this is only called so we can set it later...
	return "", nil
}

var _ handlers.ScopeNamer = nopScopeNamer{}

type nopDefaulter struct{}

func (n nopDefaulter) Default(in runtime.Object) {}

var _ runtime.ObjectDefaulter = nopDefaulter{}

type delegatingUnstructuredScheme struct {
	delegate *runtime.Scheme
}

func (d *delegatingUnstructuredScheme) ObjectKinds(object runtime.Object) ([]schema.GroupVersionKind, bool, error) {
	return apiserver.NewUnstructuredObjectTyper(d.delegate).ObjectKinds(object)
}

func (d *delegatingUnstructuredScheme) Recognizes(gvk schema.GroupVersionKind) bool {
	return apiserver.NewUnstructuredObjectTyper(d.delegate).Recognizes(gvk)
}

func (d *delegatingUnstructuredScheme) Convert(in, out, context interface{}) error {
	e := d.delegate.Convert(in, out, context)
	if runtime.IsNotRegisteredError(e) {
		return nopConverter{}.Convert(in, out, context)
	}
	return e
}

func (d *delegatingUnstructuredScheme) ConvertToVersion(in runtime.Object, gv runtime.GroupVersioner) (out runtime.Object, err error) {
	o, e := d.delegate.ConvertToVersion(in, gv)
	if runtime.IsNotRegisteredError(e) {
		return nopConverter{}.ConvertToVersion(in, gv)
	}
	return o, e
}

func (d *delegatingUnstructuredScheme) ConvertFieldLabel(gvk schema.GroupVersionKind, label, value string) (string, string, error) {
	a, b, e := d.delegate.ConvertFieldLabel(gvk, label, value)
	if runtime.IsNotRegisteredError(e) {
		return nopConverter{}.ConvertFieldLabel(gvk, label, value)
	}
	return a, b, e
}

func (d *delegatingUnstructuredScheme) New(kind schema.GroupVersionKind) (out runtime.Object, err error) {
	o, e := d.delegate.New(kind)
	if runtime.IsNotRegisteredError(e) {
		return apiserver.UnstructuredCreator{}.New(kind)
	}
	return o, e
}

var _ runtime.ObjectConvertor = &delegatingUnstructuredScheme{}
var _ runtime.ObjectCreater = &delegatingUnstructuredScheme{}
var _ runtime.ObjectTyper = &delegatingUnstructuredScheme{}
