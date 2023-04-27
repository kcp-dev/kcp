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
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"text/template"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/martinlindhe/base36"
	"github.com/spf13/cobra"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/cliplugins/base"
	"github.com/kcp-dev/kcp/pkg/cliplugins/helpers"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/reconciler/workload/apiexport"
	apiresourcev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apiresource/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
)

//go:embed *.yaml
var embeddedResources embed.FS

const (
	SyncerSecretConfigKey   = "kubeconfig"
	SyncerIDPrefix          = "kcp-syncer-"
	DNSIDPrefix             = "kcp-dns-"
	MaxSyncTargetNameLength = validation.DNS1123SubdomainMaxLength - (9 + len(SyncerIDPrefix))
)

// SyncOptions contains options for configuring a SyncTarget and its corresponding syncer.
type SyncOptions struct {
	*base.Options

	// ResourcesToSync is a list of fully-qualified resource names that should be synced by the syncer.
	ResourcesToSync []string
	// APIExports is a list of APIExport to be supported by the synctarget.
	APIExports []string
	// SyncerImage is the container image that should be used for the syncer.
	SyncerImage string
	// Replicas is the number of replicas to configure in the syncer's deployment.
	Replicas int
	// OutputFile is the path to a file where the YAML for the syncer should be written.
	OutputFile string
	// DownstreamNamespace is the name of the namespace in the physical cluster where the syncer deployment is created.
	DownstreamNamespace string
	// KCPNamespace is the name of the namespace in the kcp workspace where the service account is created for the
	// syncer.
	KCPNamespace string
	// QPS is the refill rate for the syncer client's rate limiter bucket (steady state requests per second).
	QPS float32
	// Burst is the maximum size for the syncer client's rate limiter bucket when idle.
	Burst int
	// SyncTargetName is the name of the SyncTarget in the kcp workspace.
	SyncTargetName string
	// SyncTargetLabels are the labels to be applied to the SyncTarget in the kcp workspace.
	SyncTargetLabels []string
	// APIImportPollInterval is the time interval to push apiimport.
	APIImportPollInterval time.Duration
	// FeatureGates is used to configure which feature gates are enabled.
	FeatureGates string
	// DownstreamNamespaceCleanDelay is the time to wait before deleting of a downstream namespace.
	DownstreamNamespaceCleanDelay time.Duration
}

// NewSyncOptions returns a new SyncOptions.
func NewSyncOptions(streams genericclioptions.IOStreams) *SyncOptions {
	return &SyncOptions{
		Options: base.NewOptions(streams),

		Replicas:                      1,
		KCPNamespace:                  "default",
		QPS:                           20,
		Burst:                         30,
		APIImportPollInterval:         1 * time.Minute,
		APIExports:                    []string{"root:compute:kubernetes"},
		DownstreamNamespaceCleanDelay: 30 * time.Second,
	}
}

// BindFlags binds fields SyncOptions as command line flags to cmd's flagset.
func (o *SyncOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)

	cmd.Flags().StringSliceVar(&o.ResourcesToSync, "resources", o.ResourcesToSync, "Resources to synchronize with kcp, each resource should be in the format of resourcename.<gvr_of_the_resource>,"+
		"e.g. to sync routes to physical cluster the resource name should be given as --resource routes.route.openshift.io")
	cmd.Flags().StringSliceVar(&o.APIExports, "apiexports", o.APIExports,
		"APIExport to be supported by the syncer, each APIExport should be in the format of <absolute_ref_to_workspace>:<apiexport>, "+
			"e.g. root:compute:kubernetes is the kubernetes APIExport in root:compute workspace")
	cmd.Flags().StringVar(&o.SyncerImage, "syncer-image", o.SyncerImage, "The syncer image to use in the syncer's deployment YAML. Images are published at https://github.com/kcp-dev/kcp/pkgs/container/kcp%2Fsyncer.")
	cmd.Flags().IntVar(&o.Replicas, "replicas", o.Replicas, "Number of replicas of the syncer deployment.")
	cmd.Flags().StringVar(&o.KCPNamespace, "kcp-namespace", o.KCPNamespace, "The name of the kcp namespace to create a service account in.")
	cmd.Flags().StringVarP(&o.OutputFile, "output-file", "o", o.OutputFile, "The manifest file to be created and applied to the physical cluster. Use - for stdout.")
	cmd.Flags().StringVarP(&o.DownstreamNamespace, "namespace", "n", o.DownstreamNamespace, "The namespace to create the syncer in the physical cluster. By default this is \"kcp-syncer-<synctarget-name>-<uid>\".")
	cmd.Flags().Float32Var(&o.QPS, "qps", o.QPS, "QPS to use when talking to API servers.")
	cmd.Flags().IntVar(&o.Burst, "burst", o.Burst, "Burst to use when talking to API servers.")
	cmd.Flags().StringVar(&o.FeatureGates, "feature-gates", o.FeatureGates,
		"A set of key=value pairs that describe feature gates for alpha/experimental features. "+
			"Options are:\n"+strings.Join(kcpfeatures.KnownFeatures(), "\n")) // hide kube-only gates
	cmd.Flags().DurationVar(&o.APIImportPollInterval, "api-import-poll-interval", o.APIImportPollInterval, "Polling interval for API import.")
	cmd.Flags().DurationVar(&o.DownstreamNamespaceCleanDelay, "downstream-namespace-clean-delay", o.DownstreamNamespaceCleanDelay, "Time to wait before deleting a downstream namespaces.")
	cmd.Flags().StringSliceVar(&o.SyncTargetLabels, "labels", o.SyncTargetLabels, "Labels to apply on the SyncTarget created in kcp, each label should be in the format of key=value.")
}

// Complete ensures all dynamically populated fields are initialized.
func (o *SyncOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	o.SyncTargetName = args[0]

	return nil
}

// Validate validates the SyncOptions are complete and usable.
func (o *SyncOptions) Validate() error {
	var errs []error

	if err := o.Options.Validate(); err != nil {
		errs = append(errs, err)
	}

	if o.SyncerImage == "" {
		errs = append(errs, errors.New("--syncer-image is required"))
	}

	if o.KCPNamespace == "" {
		errs = append(errs, errors.New("--kcp-namespace is required"))
	}

	if o.Replicas < 0 {
		errs = append(errs, errors.New("--replicas cannot be negative"))
	}
	if o.Replicas > 1 {
		// TODO: relax when we have leader-election in the syncer
		errs = append(errs, errors.New("only 0 and 1 are valid values for --replicas"))
	}

	if o.OutputFile == "" {
		errs = append(errs, errors.New("--output-file is required"))
	}

	// see pkg/syncer/shared/GetDNSID
	if len(o.SyncTargetName)+len(DNSIDPrefix)+8+8+2 > 254 {
		errs = append(errs, fmt.Errorf("the maximum length of the sync-target-name is %d", MaxSyncTargetNameLength))
	}

	for _, l := range o.SyncTargetLabels {
		if len(strings.Split(l, "=")) != 2 {
			errs = append(errs, fmt.Errorf("label '%s' is not in the format of key=value", l))
		}
	}

	for _, apiExport := range o.APIExports {
		_, name := logicalcluster.NewPath(apiExport).Split()
		if name == workloadv1alpha1.ImportedAPISExportName {
			errs = append(errs, fmt.Errorf("%s is a reserved APIExport name and should not be set", workloadv1alpha1.ImportedAPISExportName))
		}
	}

	return utilerrors.NewAggregate(errs)
}

// Run prepares a kcp workspace for use with a syncer and outputs the
// configuration required to deploy a syncer to the pcluster to stdout.
func (o *SyncOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}

	var output io.Writer
	if o.OutputFile == "-" {
		output = o.IOStreams.Out
	} else {
		outputFile, err := os.Create(o.OutputFile)
		if err != nil {
			return err
		}
		defer outputFile.Close()
		output = outputFile
	}

	labels := map[string]string{}
	for _, l := range o.SyncTargetLabels {
		parts := strings.Split(l, "=")
		if len(parts) != 2 {
			continue
		}
		labels[parts[0]] = parts[1]
	}

	token, syncerID, syncTarget, err := o.enableSyncerForWorkspace(ctx, config, o.SyncTargetName, o.KCPNamespace, labels)
	if err != nil {
		return err
	}

	expectedResourcesForPermission, err := o.getResourcesForPermission(ctx, config, o.SyncTargetName)
	if err != nil {
		return err
	}

	configURL, _, err := helpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to workspace", config.Host)
	}

	// Make sure the generated URL has the port specified correctly.
	if _, _, err = net.SplitHostPort(configURL.Host); err != nil {
		var addrErr *net.AddrError
		const missingPort = "missing port in address"
		if errors.As(err, &addrErr) && addrErr.Err == missingPort {
			if configURL.Scheme == "https" {
				configURL.Host = net.JoinHostPort(configURL.Host, "443")
			} else {
				configURL.Host = net.JoinHostPort(configURL.Host, "80")
			}
		} else {
			return fmt.Errorf("failed to parse host %q: %w", configURL.Host, err)
		}
	}

	if o.DownstreamNamespace == "" {
		o.DownstreamNamespace = syncerID
	}

	// Compose the syncer's upstream configuration server URL without any path. This is
	// required so long as the API importer and syncer expect to require cluster clients.
	//
	// TODO(marun) It's probably preferable that the syncer and importer are provided a
	// cluster configuration since they only operate against a single workspace.
	serverURL := configURL.Scheme + "://" + configURL.Host
	input := templateInput{
		ServerURL:    serverURL,
		CAData:       base64.StdEncoding.EncodeToString(config.CAData),
		Token:        token,
		KCPNamespace: o.KCPNamespace,
		Namespace:    o.DownstreamNamespace,

		SyncTargetPath: logicalcluster.From(syncTarget).Path().String(),
		SyncTarget:     o.SyncTargetName,
		SyncTargetUID:  string(syncTarget.UID),

		Image:                               o.SyncerImage,
		Replicas:                            o.Replicas,
		ResourcesToSync:                     o.ResourcesToSync,
		QPS:                                 o.QPS,
		Burst:                               o.Burst,
		FeatureGatesString:                  o.FeatureGates,
		APIImportPollIntervalString:         o.APIImportPollInterval.String(),
		DownstreamNamespaceCleanDelayString: o.DownstreamNamespaceCleanDelay.String(),
	}

	resources, err := renderSyncerResources(input, syncerID, sets.List[string](expectedResourcesForPermission))
	if err != nil {
		return err
	}

	_, err = output.Write(resources)
	if o.OutputFile != "-" {
		fmt.Fprintf(o.ErrOut, "\nWrote physical cluster manifest to %s for namespace %q. Use\n\n  KUBECONFIG=<pcluster-config> kubectl apply -f %q\n\nto apply it. "+
			"Use\n\n  KUBECONFIG=<pcluster-config> kubectl get deployment -n %q %s\n\nto verify the syncer pod is running.\n", o.OutputFile, o.DownstreamNamespace, o.OutputFile, o.DownstreamNamespace, syncerID)
	}
	return err
}

// getSyncerID returns a unique ID for a syncer derived from the name and its UID. It's
// a valid DNS segment and can be used as namespace or object names.
func getSyncerID(syncTarget *workloadv1alpha1.SyncTarget) string {
	syncerHash := sha256.Sum224([]byte(syncTarget.UID))
	base36hash := strings.ToLower(base36.EncodeBytes(syncerHash[:]))
	return fmt.Sprintf("kcp-syncer-%s-%s", syncTarget.Name, base36hash[:8])
}

func (o *SyncOptions) applySyncTarget(ctx context.Context, kcpClient kcpclient.Interface, syncTargetName string, labels map[string]string) (*workloadv1alpha1.SyncTarget, error) {
	supportedAPIExports := make([]tenancyv1alpha1.APIExportReference, 0, len(o.APIExports))
	for _, export := range o.APIExports {
		lclusterName, name := logicalcluster.NewPath(export).Split()
		supportedAPIExports = append(supportedAPIExports, tenancyv1alpha1.APIExportReference{
			Export: name,
			Path:   lclusterName.String(),
		})
	}

	// create local apiexport if resources flag is set
	if len(o.ResourcesToSync) > 0 {
		apiExport, err := kcpClient.ApisV1alpha1().APIExports().Get(ctx, workloadv1alpha1.ImportedAPISExportName, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			fmt.Fprintf(o.ErrOut, "Creating APIExport %q\n", workloadv1alpha1.ImportedAPISExportName)
			apiExport = &apisv1alpha1.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: workloadv1alpha1.ImportedAPISExportName,
					Annotations: map[string]string{
						workloadv1alpha1.ComputeAPIExportAnnotationKey: "true",
					},
				},
				Spec: apisv1alpha1.APIExportSpec{
					LatestResourceSchemas: []string{},
				},
			}
			apiExport, _ = mergeLatestResourceSchema(apiExport, o.ResourcesToSync)
			_, err = kcpClient.ApisV1alpha1().APIExports().Create(ctx, apiExport, metav1.CreateOptions{})
			if err != nil && !apierrors.IsAlreadyExists(err) {
				return nil, err
			}
		case err != nil:
			return nil, err
		default:
			if apiExport, modified := mergeLatestResourceSchema(apiExport, o.ResourcesToSync); modified {
				_, err = kcpClient.ApisV1alpha1().APIExports().Update(ctx, apiExport, metav1.UpdateOptions{})
				if err != nil {
					return nil, err
				}
			}
		}

		// if ResourcesToSync is not empty, add export in synctarget workspace.
		if !sets.New[string](o.APIExports...).Has(workloadv1alpha1.ImportedAPISExportName) {
			supportedAPIExports = append(supportedAPIExports, tenancyv1alpha1.APIExportReference{
				Export: workloadv1alpha1.ImportedAPISExportName,
			})
		}
	}

	syncTarget, err := kcpClient.WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})

	switch {
	case apierrors.IsNotFound(err):
		// Create the sync target that will serve as a point of coordination between
		// kcp and the syncer (e.g. heartbeating from the syncer and virtual cluster urls
		// to the syncer).
		fmt.Fprintf(o.ErrOut, "Creating synctarget %q\n", syncTargetName)
		syncTarget, err = kcpClient.WorkloadV1alpha1().SyncTargets().Create(ctx,
			&workloadv1alpha1.SyncTarget{
				ObjectMeta: metav1.ObjectMeta{
					Name:   syncTargetName,
					Labels: labels,
				},
				Spec: workloadv1alpha1.SyncTargetSpec{
					SupportedAPIExports: supportedAPIExports,
				},
			},
			metav1.CreateOptions{},
		)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create synctarget %q: %w", syncTargetName, err)
		}
		if err == nil {
			return syncTarget, nil
		}
	case err != nil:
		return nil, err
	}

	if equality.Semantic.DeepEqual(labels, syncTarget.ObjectMeta.Labels) && equality.Semantic.DeepEqual(supportedAPIExports, syncTarget.Spec.SupportedAPIExports) {
		return syncTarget, nil
	}

	// Patch synctarget with updated exports
	oldData, err := json.Marshal(workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			Labels: syncTarget.ObjectMeta.Labels,
		},
		Spec: workloadv1alpha1.SyncTargetSpec{
			SupportedAPIExports: syncTarget.Spec.SupportedAPIExports,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal old data for syncTarget %s: %w", syncTargetName, err)
	}

	newData, err := json.Marshal(workloadv1alpha1.SyncTarget{
		ObjectMeta: metav1.ObjectMeta{
			UID:             syncTarget.UID,
			ResourceVersion: syncTarget.ResourceVersion,
			Labels:          labels,
		}, // to ensure they appear in the patch as preconditions
		Spec: workloadv1alpha1.SyncTargetSpec{
			SupportedAPIExports: supportedAPIExports,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal new data for syncTarget %s: %w", syncTargetName, err)
	}

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	if err != nil {
		return nil, fmt.Errorf("failed to create merge patch for syncTarget %q because: %w", syncTargetName, err)
	}

	if syncTarget, err = kcpClient.WorkloadV1alpha1().SyncTargets().Patch(ctx, syncTargetName, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
		return nil, fmt.Errorf("failed to patch syncTarget %s: %w", syncTargetName, err)
	}
	return syncTarget, nil
}

// getResourcesForPermission get all resources to sync from syncTarget status and resources flags. It is used to generate the rbac on
// physical cluster for syncer.
func (o *SyncOptions) getResourcesForPermission(ctx context.Context, config *rest.Config, syncTargetName string) (sets.Set[string], error) {
	kcpClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kcp client: %w", err)
	}

	// Poll synctarget to get all resources to sync, the ResourcesToSync set from the flag should be also added, since
	// its related APIResourceSchemas will not be added until the syncer is started.
	expectedResourcesForPermission := sets.New[string](o.ResourcesToSync...)
	// secrets and configmaps are always needed.
	expectedResourcesForPermission.Insert("secrets", "configmaps")
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, 30*time.Second, func(ctx context.Context) (bool, error) {
		syncTarget, err := kcpClient.WorkloadV1alpha1().SyncTargets().Get(ctx, syncTargetName, metav1.GetOptions{})
		if err != nil {
			return false, nil //nolint:nilerr
		}

		if len(syncTarget.Spec.SupportedAPIExports) == 0 {
			return true, nil
		}

		// skip if there is only the local imported-apis APIExport in the synctarget workspace, since we may not get syncedResources yet.
		if len(syncTarget.Spec.SupportedAPIExports) == 1 &&
			syncTarget.Spec.SupportedAPIExports[0].Export == workloadv1alpha1.ImportedAPISExportName {
			return true, nil
		}

		if len(syncTarget.Status.SyncedResources) == 0 {
			return false, nil
		}
		for _, rs := range syncTarget.Status.SyncedResources {
			expectedResourcesForPermission.Insert(fmt.Sprintf("%s.%s", rs.Resource, rs.Group))
		}
		return true, nil
	})
	if err != nil {
		return nil, fmt.Errorf("error waiting for getting resources to sync in syncTarget %s, %w", syncTargetName, err)
	}

	return expectedResourcesForPermission, nil
}

// enableSyncerForWorkspace creates a sync target with the given name and creates a service
// account for the syncer in the given namespace. The expectation is that the provided config is
// for a logical cluster (workspace). Returns the token the syncer will use to connect to kcp.
func (o *SyncOptions) enableSyncerForWorkspace(ctx context.Context, config *rest.Config, syncTargetName, namespace string, labels map[string]string) (saToken string, syncerID string, syncTarget *workloadv1alpha1.SyncTarget, err error) {
	kcpClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to create kcp client: %w", err)
	}

	syncTarget, err = o.applySyncTarget(ctx, kcpClient, syncTargetName, labels)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to apply synctarget %q: %w", syncTargetName, err)
	}

	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	syncerID = getSyncerID(syncTarget)

	syncTargetOwnerReferences := []metav1.OwnerReference{{
		APIVersion: workloadv1alpha1.SchemeGroupVersion.String(),
		Kind:       "SyncTarget",
		Name:       syncTarget.Name,
		UID:        syncTarget.UID,
	}}
	sa, err := kubeClient.CoreV1().ServiceAccounts(namespace).Get(ctx, syncerID, metav1.GetOptions{})

	switch {
	case apierrors.IsNotFound(err):
		fmt.Fprintf(o.ErrOut, "Creating service account %q\n", syncerID)
		if sa, err = kubeClient.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:            syncerID,
				OwnerReferences: syncTargetOwnerReferences,
			},
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return "", "", nil, fmt.Errorf("failed to create ServiceAccount %s|%s/%s: %w", syncTargetName, namespace, syncerID, err)
		}
	case err == nil:
		oldData, err := json.Marshal(corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: sa.OwnerReferences,
			},
		})
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to marshal old data for ServiceAccount %s|%s/%s: %w", syncTargetName, namespace, syncerID, err)
		}

		newData, err := json.Marshal(corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				UID:             sa.UID,
				ResourceVersion: sa.ResourceVersion,
				OwnerReferences: mergeOwnerReference(sa.ObjectMeta.OwnerReferences, syncTargetOwnerReferences),
			},
		})
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to marshal new data for ServiceAccount %s|%s/%s: %w", syncTargetName, namespace, syncerID, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to create patch for ServiceAccount %s|%s/%s: %w", syncTargetName, namespace, syncerID, err)
		}

		fmt.Fprintf(o.ErrOut, "Updating service account %q.\n", syncerID)
		if sa, err = kubeClient.CoreV1().ServiceAccounts(namespace).Patch(ctx, sa.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			return "", "", nil, fmt.Errorf("failed to patch ServiceAccount %s|%s/%s: %w", syncTargetName, syncerID, namespace, err)
		}
	default:
		return "", "", nil, fmt.Errorf("failed to get the ServiceAccount %s|%s/%s: %w", syncTargetName, syncerID, namespace, err)
	}

	// Create a cluster role that provides the syncer the minimal permissions
	// required by KCP to manage the sync target, and by the syncer virtual
	// workspace to sync.
	rules := []rbacv1.PolicyRule{
		{
			Verbs:         []string{"sync"},
			APIGroups:     []string{workloadv1alpha1.SchemeGroupVersion.Group},
			ResourceNames: []string{syncTargetName},
			Resources:     []string{"synctargets"},
		},
		{
			Verbs:         []string{"get"},
			APIGroups:     []string{workloadv1alpha1.SchemeGroupVersion.Group},
			ResourceNames: []string{syncTargetName},
			Resources:     []string{"synctargets/tunnel"},
		},
		{
			Verbs:         []string{"get", "list", "watch"},
			APIGroups:     []string{workloadv1alpha1.SchemeGroupVersion.Group},
			Resources:     []string{"synctargets"},
			ResourceNames: []string{syncTargetName},
		},
		{
			Verbs:         []string{"update", "patch"},
			APIGroups:     []string{workloadv1alpha1.SchemeGroupVersion.Group},
			ResourceNames: []string{syncTargetName},
			Resources:     []string{"synctargets/status"},
		},
		{
			Verbs:     []string{"get", "create", "update", "delete", "list", "watch"},
			APIGroups: []string{apiresourcev1alpha1.SchemeGroupVersion.Group},
			Resources: []string{"apiresourceimports"},
		},
		{
			Verbs:           []string{"access"},
			NonResourceURLs: []string{"/"},
		},
	}

	cr, err := kubeClient.RbacV1().ClusterRoles().Get(ctx,
		syncerID,
		metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		fmt.Fprintf(o.ErrOut, "Creating cluster role %q to give service account %q\n\n 1. write and sync access to the synctarget %q\n 2. write access to apiresourceimports.\n\n", syncerID, syncerID, syncerID)
		if _, err = kubeClient.RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:            syncerID,
				OwnerReferences: syncTargetOwnerReferences,
			},
			Rules: rules,
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return "", "", nil, err
		}
	case err == nil:
		oldData, err := json.Marshal(rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				OwnerReferences: cr.OwnerReferences,
			},
			Rules: cr.Rules,
		})
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to marshal old data for ClusterRole %s|%s: %w", syncTargetName, syncerID, err)
		}

		newData, err := json.Marshal(rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				UID:             cr.UID,
				ResourceVersion: cr.ResourceVersion,
				OwnerReferences: mergeOwnerReference(cr.OwnerReferences, syncTargetOwnerReferences),
			},
			Rules: rules,
		})
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to marshal new data for ClusterRole %s|%s: %w", syncTargetName, syncerID, err)
		}

		patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
		if err != nil {
			return "", "", nil, fmt.Errorf("failed to create patch for ClusterRole %s|%s: %w", syncTargetName, syncerID, err)
		}

		fmt.Fprintf(o.ErrOut, "Updating cluster role %q with\n\n 1. write and sync access to the synctarget %q\n 2. write access to apiresourceimports.\n\n", syncerID, syncerID)
		if _, err = kubeClient.RbacV1().ClusterRoles().Patch(ctx, cr.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}); err != nil {
			return "", "", nil, fmt.Errorf("failed to patch ClusterRole %s|%s/%s: %w", syncTargetName, syncerID, namespace, err)
		}
	default:
		return "", "", nil, err
	}

	// Grant the service account the role created just above in the workspace
	subjects := []rbacv1.Subject{{
		Kind:      "ServiceAccount",
		Name:      syncerID,
		Namespace: namespace,
	}}
	roleRef := rbacv1.RoleRef{
		Kind:     "ClusterRole",
		Name:     syncerID,
		APIGroup: "rbac.authorization.k8s.io",
	}

	_, err = kubeClient.RbacV1().ClusterRoleBindings().Get(ctx,
		syncerID,
		metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return "", "", nil, err
	}
	if err == nil {
		if err := kubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, syncerID, metav1.DeleteOptions{}); err != nil {
			return "", "", nil, err
		}
	}

	fmt.Fprintf(o.ErrOut, "Creating or updating cluster role binding %q to bind service account %q to cluster role %q.\n", syncerID, syncerID, syncerID)
	if _, err = kubeClient.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:            syncerID,
			OwnerReferences: syncTargetOwnerReferences,
		},
		Subjects: subjects,
		RoleRef:  roleRef,
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", "", nil, err
	}

	// Wait for the service account to be updated with the name of the token secret
	tokenSecretName := ""
	err = wait.PollImmediateWithContext(ctx, 100*time.Millisecond, 20*time.Second, func(ctx context.Context) (bool, error) {
		serviceAccount, err := kubeClient.CoreV1().ServiceAccounts(namespace).Get(ctx, sa.Name, metav1.GetOptions{})
		if err != nil {
			klog.FromContext(ctx).V(5).WithValues("err", err).Info("failed to retrieve ServiceAccount")
			return false, nil
		}
		if len(serviceAccount.Secrets) == 0 {
			return false, nil
		}
		tokenSecretName = serviceAccount.Secrets[0].Name
		return true, nil
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("timed out waiting for token secret name to be set on ServiceAccount %s/%s", namespace, sa.Name)
	}

	// Retrieve the token that the syncer will use to authenticate to kcp
	tokenSecret, err := kubeClient.CoreV1().Secrets(namespace).Get(ctx, tokenSecretName, metav1.GetOptions{})
	if err != nil {
		return "", "", nil, fmt.Errorf("failed to retrieve Secret: %w", err)
	}
	saTokenBytes := tokenSecret.Data["token"]
	if len(saTokenBytes) == 0 {
		return "", "", nil, fmt.Errorf("token secret %s/%s is missing a value for `token`", namespace, tokenSecretName)
	}

	return string(saTokenBytes), syncerID, syncTarget, nil
}

func mergeLatestResourceSchema(apiExport *apisv1alpha1.APIExport, resourceToSync []string) (*apisv1alpha1.APIExport, bool) {
	desiredResourceGroup := sets.New[string]()
	var modified bool
	for _, schema := range apiExport.Spec.LatestResourceSchemas {
		gr, valid := apiexport.ParseAPIResourceSchemaName(schema)
		if !valid {
			continue
		}
		desiredResourceGroup.Insert(gr.String())
	}
	for _, resource := range resourceToSync {
		gr := schema.ParseGroupResource(resource)
		if len(gr.Group) == 0 {
			gr.Group = "core"
		}
		if !desiredResourceGroup.Has(gr.String()) {
			// the rev-0 here is a placeholder and will be replaced by rv of negotiated APIResourceSchema finally.
			schemaName := fmt.Sprintf("rev-0.%s", gr.String())
			apiExport.Spec.LatestResourceSchemas = append(apiExport.Spec.LatestResourceSchemas, schemaName)
			modified = true
		}
	}

	return apiExport, modified
}

// mergeOwnerReference: merge a slice of ownerReference with a given ownerReferences.
func mergeOwnerReference(ownerReferences, newOwnerReferences []metav1.OwnerReference) []metav1.OwnerReference {
	var merged []metav1.OwnerReference

	merged = append(merged, ownerReferences...)

	for _, ownerReference := range newOwnerReferences {
		found := false
		for _, mergedOwnerReference := range merged {
			if mergedOwnerReference.UID == ownerReference.UID {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, ownerReference)
		}
	}

	return merged
}

// templateInput represents the external input required to render the resources to
// deploy the syncer to a pcluster.
type templateInput struct {
	// ServerURL is the logical cluster url the syncer configuration will use
	ServerURL string
	// CAData holds the PEM-encoded bytes of the ca certificate(s) a syncer will use to validate
	// kcp's serving certificate
	CAData string
	// Token is the service account token used to authenticate a syncer for access to a workspace
	Token string
	// KCPNamespace is the name of the kcp namespace of the syncer's service account
	KCPNamespace string
	// Namespace is the name of the syncer namespace on the pcluster
	Namespace string
	// SyncTargetPath is the qualified kcp logical cluster name the syncer will sync from
	SyncTargetPath string
	// SyncTarget is the name of the sync target the syncer will use to
	// communicate its status and read configuration from
	SyncTarget string
	// SyncTargetUID is the UID of the sync target the syncer will use to
	// communicate its status and read configuration from. This information is used by the
	// Syncer in order to avoid a conflict when a synctarget gets deleted and another one is
	// created with the same name.
	SyncTargetUID string
	// ResourcesToSync is the set of qualified resource names (eg. ["services",
	// "deployments.apps.k8s.io") that the syncer will synchronize between the kcp
	// workspace and the pcluster.
	ResourcesToSync []string
	// Image is the name of the container image that the syncer deployment will use
	Image string
	// Replicas is the number of syncer pods to run (should be 0 or 1).
	Replicas int
	// QPS is the qps the syncer uses when talking to an apiserver.
	QPS float32
	// Burst is the burst the syncer uses when talking to an apiserver.
	Burst int
	// FeatureGatesString is the set of features gates.
	FeatureGatesString string
	// APIImportPollIntervalString is the string of interval to poll APIImport.
	APIImportPollIntervalString string
	// DownstreamNamespaceCleanDelay is the time to delay before cleaning the downstream namespace as a string.
	DownstreamNamespaceCleanDelayString string
}

// templateArgs represents the full set of arguments required to render the resources
// required to deploy the syncer.
type templateArgs struct {
	templateInput
	// ServiceAccount is the name of the service account to create in the syncer
	// namespace on the pcluster.
	ServiceAccount string
	// ClusterRole is the name of the cluster role to create for the syncer on the
	// pcluster.
	ClusterRole string
	// ClusterRoleBinding is the name of the cluster role binding to create for the
	// syncer on the pcluster.
	ClusterRoleBinding string
	// DnsRole is the name of the DNS role to create for the syncer on the pcluster.
	DNSRole string
	// DNSRoleBinding is the name of the DNS role binding to create for the
	// syncer on the pcluster.
	DNSRoleBinding string
	// GroupMappings is the mapping of api group to resources that will be used to
	// define the cluster role rules for the syncer in the pcluster. The syncer will be
	// granted full permissions for the resources it will synchronize.
	GroupMappings []groupMapping
	// Secret is the name of the secret that will contain the kubeconfig the syncer
	// will use to connect to the kcp logical cluster (workspace) that it will
	// synchronize from.
	Secret string
	// Key in the syncer secret for the kcp logical cluster kubconfig.
	SecretConfigKey string
	// Deployment is the name of the deployment that will run the syncer in the
	// pcluster.
	Deployment string
	// DeploymentApp is the label value that the syncer's deployment will select its
	// pods with.
	DeploymentApp string
}

// renderSyncerResources renders the resources required to deploy a syncer to a pcluster.
//
// TODO(marun) Is it possible to set owner references in a set of applied resources? Ideally the
// cluster role and role binding would be owned by the namespace to ensure cleanup on deletion
// of the namespace.
func renderSyncerResources(input templateInput, syncerID string, resourceForPermission []string) ([]byte, error) {
	dnsSyncerID := strings.Replace(syncerID, "syncer", "dns", 1)

	tmplArgs := templateArgs{
		templateInput:      input,
		ServiceAccount:     syncerID,
		ClusterRole:        syncerID,
		ClusterRoleBinding: syncerID,
		DNSRole:            dnsSyncerID,
		DNSRoleBinding:     dnsSyncerID,
		GroupMappings:      getGroupMappings(resourceForPermission),
		Secret:             syncerID,
		SecretConfigKey:    SyncerSecretConfigKey,
		Deployment:         syncerID,
		DeploymentApp:      syncerID,
	}

	syncerTemplate, err := embeddedResources.ReadFile("syncer.yaml")
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("syncerTemplate").Parse(string(syncerTemplate))
	if err != nil {
		return nil, err
	}
	buffer := bytes.NewBuffer([]byte{})
	err = tmpl.Execute(buffer, tmplArgs)
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// groupMapping associates an api group to the resources in that group.
type groupMapping struct {
	APIGroup  string
	Resources []string
}

// getGroupMappings returns the set of api groups to resources for the given resources.
func getGroupMappings(resourcesToSync []string) []groupMapping {
	groupMap := make(map[string][]string)

	for _, resource := range resourcesToSync {
		nameParts := strings.SplitN(resource, ".", 2)
		name := nameParts[0]
		apiGroup := ""
		if len(nameParts) > 1 {
			apiGroup = nameParts[1]
		}
		if _, ok := groupMap[apiGroup]; !ok {
			groupMap[apiGroup] = []string{name}
		} else {
			groupMap[apiGroup] = append(groupMap[apiGroup], name)
		}
		// If pods are being synced, add the subresources that are required to
		// support the pod subresources.
		if apiGroup == "" && name == "pods" {
			podSubresources := []string{
				"pods/log",
				"pods/exec",
				"pods/attach",
				"pods/binding",
				"pods/portforward",
				"pods/proxy",
				"pods/ephemeralcontainers",
			}
			groupMap[apiGroup] = append(groupMap[apiGroup], podSubresources...)
		}
	}

	groupMappings := make([]groupMapping, 0, len(groupMap))
	for apiGroup, resources := range groupMap {
		groupMappings = append(groupMappings, groupMapping{
			APIGroup:  apiGroup,
			Resources: resources,
		})
	}

	sortGroupMappings(groupMappings)

	return groupMappings
}

// sortGroupMappings sorts group mappings first by APIGroup and then by Resources.
func sortGroupMappings(groupMappings []groupMapping) {
	sort.Slice(groupMappings, func(i, j int) bool {
		if groupMappings[i].APIGroup == groupMappings[j].APIGroup {
			return strings.Join(groupMappings[i].Resources, ",") < strings.Join(groupMappings[j].Resources, ",")
		}
		return groupMappings[i].APIGroup < groupMappings[j].APIGroup
	})
}
