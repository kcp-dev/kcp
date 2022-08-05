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

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
)

func TestGetHomeLogicalClusterName(t *testing.T) {
	testCases := []struct {
		bucketLevels int
		bucketSize   int
		homePrefix   string
		userName     string
		expectedHome string
	}{
		{
			bucketLevels: 2,
			bucketSize:   2,
			homePrefix:   "root:users",
			userName:     "user-1",
			expectedHome: "root:users:bi:ie:user-1",
		},
		{
			bucketLevels: 2,
			bucketSize:   2,
			homePrefix:   "root:homes",
			userName:     "user-1",
			expectedHome: "root:homes:bi:ie:user-1",
		},
		{
			bucketLevels: 2,
			bucketSize:   2,
			homePrefix:   "root:users",
			userName:     "user-2",
			expectedHome: "root:users:cv:ef:user-2",
		},
		{
			bucketLevels: 2,
			bucketSize:   2,
			homePrefix:   "root:users",
			userName:     "system:apiserver",
			expectedHome: "root:users:ev:vo:system-apiserver",
		},
		{
			bucketLevels: 2,
			bucketSize:   2,
			homePrefix:   "root:users",
			userName:     "2system:apiserver@company-2à-",
			expectedHome: "root:users:fk:wp:system-apiserver-company-2",
		},
		{
			bucketLevels: 5,
			bucketSize:   4,
			homePrefix:   "root:users",
			userName:     "user-1",
			expectedHome: "root:users:biht:ieal:ertd:yfel:kbap:user-1",
		},
	}

	for _, testCase := range testCases {
		t.Run(
			fmt.Sprintf("levels: %d size: %d prefix: %s userName: %s", testCase.bucketLevels, testCase.bucketSize, testCase.homePrefix, testCase.userName),
			func(t *testing.T) {
				require.Equal(t,
					testCase.expectedHome,
					homeWorkspaceHandlerBuilder{
						bucketLevels: testCase.bucketLevels,
						bucketSize:   testCase.bucketSize,
						homePrefix:   logicalcluster.New(testCase.homePrefix),
					}.build().getHomeLogicalClusterName(testCase.userName).String(),
				)
			})
	}
}

func TestNeedsAutomaticCreation(t *testing.T) {
	testCases := []struct {
		bucketLevels int
		homePrefix   string

		workspaceName string

		expectedNeedsCheck    bool
		expectedWorkspaceType string
	}{
		{
			bucketLevels: 2,
			homePrefix:   "root:users",

			workspaceName:      "org1:team1:proj1",
			expectedNeedsCheck: false,
		},
		{
			bucketLevels: 2,
			homePrefix:   "root:users",

			workspaceName:         "root:users:ab:cd:user-1",
			expectedNeedsCheck:    true,
			expectedWorkspaceType: "home",
		},
		{
			bucketLevels: 1,
			homePrefix:   "root:users",

			workspaceName:      "root:users:ab:cd:user-1",
			expectedNeedsCheck: false,
		},
		{
			bucketLevels: 2,
			homePrefix:   "root:users",

			workspaceName:         "root:users:ab:cd",
			expectedNeedsCheck:    true,
			expectedWorkspaceType: "homebucket",
		},
		{
			bucketLevels: 2,
			homePrefix:   "root:users",

			workspaceName:         "root:users:ab",
			expectedNeedsCheck:    true,
			expectedWorkspaceType: "homebucket",
		},
		{
			bucketLevels: 2,
			homePrefix:   "root:users",

			workspaceName:      "root:users",
			expectedNeedsCheck: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(
			fmt.Sprintf("levels: %d prefix: %s", testCase.bucketLevels, testCase.homePrefix),
			func(t *testing.T) {
				needsCheck, workspaceType := homeWorkspaceHandlerBuilder{
					bucketLevels: testCase.bucketLevels,
					homePrefix:   logicalcluster.New(testCase.homePrefix),
				}.build().needsAutomaticCreation(logicalcluster.New(testCase.workspaceName))

				require.Equal(t, testCase.expectedNeedsCheck, needsCheck)
				require.Equal(t, testCase.expectedWorkspaceType, string(workspaceType))
			})
	}
}

func TestSearchForReadyWorkspaceInLocalInformers(t *testing.T) {
	creationDelaySeconds := 5
	testCases := []struct {
		testName string

		workspaceName string
		isHome        bool
		userName      string

		getLocalClusterWorkspace func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error)

		mocks homeWorkspaceFeatureLogic

		expectedFound             bool
		expectedRetryAfterSeconds int
		expectedCheckError        string
		expectedToSearchForRBAC   bool
	}{
		{
			testName: "return error when unexpected error on local ClusterWorkspace get",

			workspaceName: "root:users:ab:cd:user-1",
			isHome:        true,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return nil, errors.New("an error")
			},

			expectedCheckError: "an error",
		},
		{
			testName: "return 'not found' when workspace not found in local informer",

			workspaceName: "root:users:ab:cd:user-1",
			isHome:        true,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return nil, kerrors.NewNotFound(schema.GroupResource{}, "user-1")
			},

			expectedFound: false,
		},
		{
			testName: "return 'found' when home workspace ready and RBAC resources found in local informers",

			workspaceName: "root:users:ab:cd:user-1",
			isHome:        true,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:ab:cd:user-1").inPhase(tenancyv1alpha1.ClusterWorkspacePhaseReady).ClusterWorkspace, nil
			},

			mocks: homeWorkspaceFeatureLogic{
				searchForHomeWorkspaceRBACResourcesInLocalInformers: func(homeWorkspace logicalcluster.Name) (found bool, err error) {
					return true, nil
				},
			},

			expectedFound:           true,
			expectedToSearchForRBAC: true,
		},
		{
			testName: "retry quickly when workspace ready albeit RBAC resources not found in local informers",

			workspaceName: "root:users:ab:cd:user-1",
			isHome:        true,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:ab:cd:user-1").inPhase(tenancyv1alpha1.ClusterWorkspacePhaseReady).ClusterWorkspace, nil
			},

			mocks: homeWorkspaceFeatureLogic{
				searchForHomeWorkspaceRBACResourcesInLocalInformers: func(homeWorkspace logicalcluster.Name) (found bool, err error) {
					return false, nil
				},
			},

			expectedFound:             true,
			expectedRetryAfterSeconds: 1,
			expectedToSearchForRBAC:   true,
		},
		{
			testName: "return error when workspace ready but error when searching RBAC resources in local informers",

			workspaceName: "root:users:ab:cd:user-1",
			isHome:        true,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:ab:cd:user-1").inPhase(tenancyv1alpha1.ClusterWorkspacePhaseReady).ClusterWorkspace, nil
			},

			mocks: homeWorkspaceFeatureLogic{
				searchForHomeWorkspaceRBACResourcesInLocalInformers: func(homeWorkspace logicalcluster.Name) (found bool, err error) {
					return false, errors.New("error")
				},
			},

			expectedFound:             false,
			expectedRetryAfterSeconds: 0,
			expectedToSearchForRBAC:   true,
			expectedCheckError:        "error",
		},
		{
			testName: "retry when workspace not ready nor unschedulable",

			workspaceName: "root:users:ab:cd:user-1",
			isHome:        true,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:ab:cd:user-1").ClusterWorkspace, nil
			},

			mocks: homeWorkspaceFeatureLogic{
				searchForHomeWorkspaceRBACResourcesInLocalInformers: func(homeWorkspace logicalcluster.Name) (found bool, err error) {
					return false, nil
				},
			},

			expectedFound:             true,
			expectedRetryAfterSeconds: creationDelaySeconds,
			expectedToSearchForRBAC:   false,
		},
		{
			testName: "return found when home workspace still initializing",

			workspaceName: "root:users:ab:cd:user-1",
			isHome:        true,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:ab:cd:user-1").inPhase(tenancyv1alpha1.ClusterWorkspacePhaseInitializing).ClusterWorkspace, nil
			},

			mocks: homeWorkspaceFeatureLogic{
				searchForHomeWorkspaceRBACResourcesInLocalInformers: func(homeWorkspace logicalcluster.Name) (found bool, err error) {
					return true, nil
				},
			},

			expectedFound:             true,
			expectedRetryAfterSeconds: 0,
			expectedToSearchForRBAC:   true,
		},
		{
			testName: "return error when workspace unschedulale (reason: unschedulable)",

			workspaceName: "root:users:ab:cd:user-1",
			isHome:        true,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:ab:cd:user-1").unschedulable().ClusterWorkspace, nil
			},

			expectedFound:             false,
			expectedRetryAfterSeconds: 0,
			expectedToSearchForRBAC:   false,
			expectedCheckError:        `clusterworkspaces.tenancy.kcp.dev "root:users:ab:cd:user-1" is forbidden: unschedulable workspace cannot be accessed`,
		},
		{
			testName: "return error when workspace unschedulale (reason: unknown)",

			workspaceName: "root:users:ab:cd:user-1",
			isHome:        true,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:ab:cd:user-1").unschedulable().ClusterWorkspace, nil
			},

			expectedFound:             false,
			expectedRetryAfterSeconds: 0,
			expectedToSearchForRBAC:   false,
			expectedCheckError:        `clusterworkspaces.tenancy.kcp.dev "root:users:ab:cd:user-1" is forbidden: unschedulable workspace cannot be accessed`,
		},
		{
			testName: "return error when workspace unschedulale (reason: unreschedulable)",

			workspaceName: "root:users:ab:cd:user-1",
			isHome:        true,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:ab:cd:user-1").unschedulable().ClusterWorkspace, nil
			},

			expectedFound:             false,
			expectedRetryAfterSeconds: 0,
			expectedToSearchForRBAC:   false,
			expectedCheckError:        `clusterworkspaces.tenancy.kcp.dev "root:users:ab:cd:user-1" is forbidden: unschedulable workspace cannot be accessed`,
		},
		{
			testName: "don't check workspace phase not RBAC when workspace is a not a home workspace",

			workspaceName: "root:users:ab:cd",
			isHome:        false,
			userName:      "user-1",

			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:ab:cd:user-1").ClusterWorkspace, nil
			},

			expectedFound:           true,
			expectedToSearchForRBAC: false,
		},
	}

	for _, testCase := range testCases {
		t.Run(
			testCase.testName,
			func(t *testing.T) {
				searchedForRBAC := false
				handler := homeWorkspaceHandlerBuilder{
					bucketLevels:         2,
					bucketSize:           2,
					homePrefix:           logicalcluster.New("root:users"),
					creationDelaySeconds: creationDelaySeconds,
					localInformers: localInformersAccess{
						getClusterWorkspace: testCase.getLocalClusterWorkspace,
					},
				}.build()

				handler.homeWorkspaceFeatureLogic.searchForHomeWorkspaceRBACResourcesInLocalInformers = func(homeWorkspace logicalcluster.Name) (found bool, err error) {
					searchedForRBAC = true
					return testCase.mocks.searchForHomeWorkspaceRBACResourcesInLocalInformers(homeWorkspace)
				}

				found, retryAfterSeconds, checkError := handler.searchForWorkspaceAndRBACInLocalInformers(logicalcluster.New(testCase.workspaceName), testCase.isHome, testCase.userName)

				require.Equal(t, testCase.expectedFound, found, "'found' value is wrong")
				require.Equal(t, testCase.expectedRetryAfterSeconds, retryAfterSeconds, "'retryAfterSeconds' value is wrong")
				var checkErrorString string
				if checkError != nil {
					checkErrorString = checkError.Error()
				}
				require.Equal(t, testCase.expectedCheckError, checkErrorString, "'checkError' value is wrong")
				require.Equal(t, testCase.expectedToSearchForRBAC, searchedForRBAC, "'searchedForRBAC' value is wrong")
			})
	}
}

func TestTryToCreate(t *testing.T) {
	creationDelaySeconds := 5
	testCases := []struct {
		testName string

		context       context.Context
		userName      string
		workspaceName string
		workspaceType string

		createClusterWorkspace func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error
		getClusterWorkspace    func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error)

		mocks homeWorkspaceFeatureLogic

		expectedRetryAfterSeconds int
		expectedCreateError       string
		expectedCreatedWorkspace  *tenancyv1alpha1.ClusterWorkspace
		expectedToCreateRBAC      bool
	}{
		{
			testName: "return error when unexpected error on creating ClusterWorkspace",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return errors.New("an error")
			},

			expectedCreateError:      "an error",
			expectedCreatedWorkspace: nil,
		},
		{
			testName: "retry quicky when success creating non-home ClusterWorkspace",

			workspaceName: "root:users:ab:cd",
			workspaceType: "homebucket",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return nil
			},
			expectedCreatedWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: "cd"},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
					Path: "root", Name: tenancyv1alpha1.ClusterWorkspaceTypeName("homebucket"),
				}},
			},
			expectedRetryAfterSeconds: 1,
		},
		{
			testName: "retry quicky when success creating non-home ClusterWorkspace",

			workspaceName: "root:users:ab:cd",
			workspaceType: "homebucket",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return nil
			},
			expectedCreatedWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: "cd"},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
					Path: "root", Name: tenancyv1alpha1.ClusterWorkspaceTypeName("homebucket"),
				}},
			},
			expectedRetryAfterSeconds: 1,
		},
		{
			testName: "retry quicky when non-home ClusterWorkspace already exists",

			workspaceName: "root:users:ab:cd",
			workspaceType: "homebucket",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewAlreadyExists(schema.GroupResource{}, "cd")
			},
			expectedRetryAfterSeconds: 1,
		},
		{
			testName: "return error when the owner does not match",

			workspaceName: "root:users:ab:cd:u--r-1",
			workspaceType: "home",
			userName:      "u$€r-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewAlreadyExists(schema.GroupResource{}, "u--r-1")
			},

			getClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "u--r-1",
						Annotations: map[string]string{
							"experimental.tenancy.kcp.dev/owner": "u§Ɛr-1",
						},
					},
				}, nil
			},

			expectedCreateError: "clusterworkspaces.tenancy.kcp.dev \"u--r-1\" is forbidden: workspace access not permitted",
		},
		{
			testName: "create RBAC and retry quicky when success creating home ClusterWorkspace",

			workspaceName: "root:users:ab:cd:user-1",
			workspaceType: "home",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return nil
			},

			mocks: homeWorkspaceFeatureLogic{
				createHomeWorkspaceRBACResources: func(ctx context.Context, userName string, homeWorkspace logicalcluster.Name) error {
					return nil
				},
			},

			expectedCreatedWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "user-1",
					Annotations: map[string]string{
						"experimental.tenancy.kcp.dev/owner": "{\"username\":\"user-1\"}",
					},
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
					Path: "root", Name: tenancyv1alpha1.ClusterWorkspaceTypeName("home"),
				}},
			},
			expectedRetryAfterSeconds: 1,
			expectedToCreateRBAC:      true,
		},
		{
			testName: "create RBAC and retry quicky when home ClusterWorkspace already exists",

			workspaceName: "root:users:ab:cd:user-1",
			workspaceType: "home",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewAlreadyExists(schema.GroupResource{}, "user-1")
			},

			getClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "user-1",
						Annotations: map[string]string{
							"experimental.tenancy.kcp.dev/owner": `{"username": "user-1"}`,
						},
					},
				}, nil
			},

			mocks: homeWorkspaceFeatureLogic{
				createHomeWorkspaceRBACResources: func(ctx context.Context, userName string, homeWorkspace logicalcluster.Name) error {
					return nil
				},
			},

			expectedRetryAfterSeconds: 1,
			expectedToCreateRBAC:      true,
		},
		{
			testName: "return error when error trying to create RBAC after home ClusterWorkspace exists",

			workspaceName: "root:users:ab:cd:user-1",
			workspaceType: "home",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewAlreadyExists(schema.GroupResource{}, "user-1")
			},

			getClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "user-1",
						Annotations: map[string]string{
							"experimental.tenancy.kcp.dev/owner": `{"username":"user-1"}`,
						},
					},
				}, nil
			},

			mocks: homeWorkspaceFeatureLogic{
				createHomeWorkspaceRBACResources: func(ctx context.Context, userName string, homeWorkspace logicalcluster.Name) error {
					return errors.New("error when creating RBAC")
				},
			},

			expectedCreateError:  "error when creating RBAC",
			expectedToCreateRBAC: true,
		},
		{
			testName: "retry later when creating ClusterWorkspace returns error suggesting delay",

			workspaceName: "root:users:ab:cd",
			workspaceType: "homebucket",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewServerTimeout(schema.GroupResource{}, "", 7)
			},

			expectedRetryAfterSeconds: 7,
		},
		{
			testName: "return error on Forbidden error and the parent is already the home root",

			workspaceName: "root:users:ab",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewForbidden(schema.GroupResource{}, "forbidden", errors.New("an error"))
			},

			expectedCreateError:      "forbidden: an error",
			expectedCreatedWorkspace: nil,
		},
		{
			testName: "return error on Forbidden error and the parent exists and is Ready",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewForbidden(schema.GroupResource{}, "forbidden", errors.New("an error"))
			},

			getClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{Name: "cd"},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: tenancyv1alpha1.ClusterWorkspaceTypeName("homebucket"),
						Path: "root",
					}},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseReady,
					},
				}, nil
			},

			expectedCreateError:      "forbidden: an error",
			expectedCreatedWorkspace: nil,
		},
		{
			testName: "return error on Forbidden error and the parent exists but is unschedulable (reason: unschedulable)",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewForbidden(schema.GroupResource{}, "forbidden", errors.New("an error"))
			},

			getClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{Name: "cd"},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: tenancyv1alpha1.ClusterWorkspaceTypeName("homebucket"),
						Path: "root",
					}},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
						Conditions: v1alpha1.Conditions{
							v1alpha1.Condition{
								Type:   tenancyv1alpha1.WorkspaceScheduled,
								Status: corev1.ConditionFalse,
								Reason: tenancyv1alpha1.WorkspaceReasonUnschedulable,
							},
						},
					},
				}, nil
			},

			expectedCreateError:      "forbidden: an error",
			expectedCreatedWorkspace: nil,
		},
		{
			testName: "return error on Forbidden error and the parent exists but is unschedulable (reason: unknown)",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewForbidden(schema.GroupResource{}, "forbidden", errors.New("an error"))
			},

			getClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{Name: "cd"},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: tenancyv1alpha1.ClusterWorkspaceTypeName("homebucket"),
						Path: "root",
					}},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
						Conditions: v1alpha1.Conditions{
							v1alpha1.Condition{
								Type:   tenancyv1alpha1.WorkspaceScheduled,
								Status: corev1.ConditionFalse,
								Reason: tenancyv1alpha1.WorkspaceReasonReasonUnknown,
							},
						},
					},
				}, nil
			},

			expectedCreateError:      "forbidden: an error",
			expectedCreatedWorkspace: nil,
		},
		{
			testName: "return error on Forbidden error and the parent exists but is unschedulable (reason: unreschedulable)",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewForbidden(schema.GroupResource{}, "forbidden", errors.New("an error"))
			},

			getClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{Name: "cd"},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: tenancyv1alpha1.ClusterWorkspaceTypeName("homebucket"),
						Path: "root",
					}},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
						Conditions: v1alpha1.Conditions{
							v1alpha1.Condition{
								Type:   tenancyv1alpha1.WorkspaceScheduled,
								Status: corev1.ConditionFalse,
								Reason: tenancyv1alpha1.WorkspaceReasonUnreschedulable,
							},
						},
					},
				}, nil
			},

			expectedCreateError:      "forbidden: an error",
			expectedCreatedWorkspace: nil,
		},
		{
			testName: "retry after creation delay if error on Forbidden error and the parent exists but is not Ready though not unschedulable",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				return kerrors.NewForbidden(schema.GroupResource{}, "forbidden", errors.New("an error"))
			},

			getClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return &tenancyv1alpha1.ClusterWorkspace{
					ObjectMeta: metav1.ObjectMeta{Name: "cd"},
					Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: tenancyv1alpha1.ClusterWorkspaceTypeName("homebucket"),
						Path: "root",
					}},
					Status: tenancyv1alpha1.ClusterWorkspaceStatus{
						Phase: tenancyv1alpha1.ClusterWorkspacePhaseScheduling,
					},
				}, nil
			},

			expectedRetryAfterSeconds: creationDelaySeconds,
		},
		{
			testName: "Go down one level, create parent and return quickly, on Forbidden error and the parent doesn't exist",

			workspaceName: "root:users:ab:cd:user-1",
			workspaceType: "home",
			userName:      "user-1",

			createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
				if workspace.String() == "root:users:ab:cd" && cw.Name == "user-1" {
					return kerrors.NewForbidden(schema.GroupResource{}, "", nil)
				}
				return nil
			},
			getClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, name string) (*tenancyv1alpha1.ClusterWorkspace, error) {
				if workspace.String() == "root:users" && name == "ab" {
					return &tenancyv1alpha1.ClusterWorkspace{
						ObjectMeta: metav1.ObjectMeta{Name: "ab"},
						Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
							Name: tenancyv1alpha1.ClusterWorkspaceTypeName("homebucket"),
							Path: "root",
						}},
					}, nil
				}
				return nil, kerrors.NewNotFound(schema.GroupResource{}, "")
			},
			expectedRetryAfterSeconds: 1,
			expectedToCreateRBAC:      false,
			expectedCreatedWorkspace: &tenancyv1alpha1.ClusterWorkspace{
				ObjectMeta: metav1.ObjectMeta{Name: "cd"},
				Spec: tenancyv1alpha1.ClusterWorkspaceSpec{Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
					Path: "root", Name: tenancyv1alpha1.ClusterWorkspaceTypeName("homebucket"),
				}},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(
			testCase.testName,
			func(t *testing.T) {
				createdRBAC := false
				var createdWorkspace *tenancyv1alpha1.ClusterWorkspace
				handler := homeWorkspaceHandlerBuilder{
					bucketLevels:         2,
					bucketSize:           2,
					homePrefix:           logicalcluster.New("root:users"),
					creationDelaySeconds: creationDelaySeconds,
					kcp: externalKubeClientsAccess{
						createClusterWorkspace: func(ctx context.Context, workspace logicalcluster.Name, cw *tenancyv1alpha1.ClusterWorkspace) error {
							err := testCase.createClusterWorkspace(ctx, workspace, cw)
							if err == nil {
								createdWorkspace = cw
							}
							return err
						},
						getClusterWorkspace: testCase.getClusterWorkspace,
					},
				}.build()

				handler.homeWorkspaceFeatureLogic.createHomeWorkspaceRBACResources = func(ctx context.Context, userName string, homeWorkspace logicalcluster.Name) error {
					createdRBAC = true
					return testCase.mocks.createHomeWorkspaceRBACResources(ctx, userName, homeWorkspace)
				}

				user := &kuser.DefaultInfo{
					Name: testCase.userName,
				}
				retryAfterSeconds, createError := handler.tryToCreate(testCase.context, user, logicalcluster.New(testCase.workspaceName), tenancyv1alpha1.ClusterWorkspaceTypeName(testCase.workspaceType))

				require.Equal(t, testCase.expectedRetryAfterSeconds, retryAfterSeconds, "'retryAfterSeconds' value is wrong")
				var createErrorString string
				if createError != nil {
					createErrorString = createError.Error()
				}
				require.Equal(t, testCase.expectedCreateError, createErrorString, "'createError' value is wrong")
				require.Equal(t, testCase.expectedCreatedWorkspace, createdWorkspace, "'createdWorkspace' value is wrong")
				require.Equal(t, testCase.expectedToCreateRBAC, createdRBAC, "'createdRBAC' value is wrong")
			})
	}
}

func TestSearchForHomeWorkspaceRBACResourcesInLocalInformers(t *testing.T) {
	creationDelaySeconds := 5
	testCases := []struct {
		testName string

		workspaceName string
		userName      string

		getLocalClusterRole        func(lcluster logicalcluster.Name, name string) (*rbacv1.ClusterRole, error)
		getLocalClusterRoleBinding func(lcluster logicalcluster.Name, name string) (*rbacv1.ClusterRoleBinding, error)

		expectedFound       bool
		expectedSearchError string
	}{
		{
			testName: "return error when unexpected error on ClusterRole search",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			getLocalClusterRole: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRole, error) {
				return nil, errors.New("error searching for ClusterRole")
			},
			getLocalClusterRoleBinding: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRoleBinding, error) {
				return &rbacv1.ClusterRoleBinding{}, nil
			},

			expectedFound:       false,
			expectedSearchError: "error searching for ClusterRole",
		},
		{
			testName: "return error when unexpected error on ClusterRoleBinding search",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			getLocalClusterRole: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRole, error) {
				return &rbacv1.ClusterRole{}, nil
			},
			getLocalClusterRoleBinding: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRoleBinding, error) {
				return nil, errors.New("error searching for ClusterRoleBinding")
			},

			expectedFound:       false,
			expectedSearchError: "error searching for ClusterRoleBinding",
		},
		{
			testName: "return false when ClusterRole not found",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			getLocalClusterRole: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRole, error) {
				return nil, kerrors.NewNotFound(schema.GroupResource{Group: "rbac.k8s.io/v1", Resource: "clusterroles"}, "name")
			},
			getLocalClusterRoleBinding: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRoleBinding, error) {
				return &rbacv1.ClusterRoleBinding{}, nil
			},

			expectedFound: false,
		},
		{
			testName: "return false when ClusterRoleBinding not found",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			getLocalClusterRole: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRole, error) {
				return &rbacv1.ClusterRole{}, nil
			},
			getLocalClusterRoleBinding: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRoleBinding, error) {
				return nil, kerrors.NewNotFound(schema.GroupResource{Group: "rbac.k8s.io/v1", Resource: "clusterrolebindings"}, name)
			},

			expectedFound: false,
		},
		{
			testName: "return true only when expected ClusterRoles and ClusterRoleBindings have been found",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			getLocalClusterRole: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRole, error) {
				switch {
				case workspace.String() == "root:users:ab:cd" && name == "system:kcp:tenancy:home-owner:user-1":
					fallthrough
				case workspace.String() == "root:users:ab:cd:user-1" && name == "home-owner":
					return &rbacv1.ClusterRole{}, nil
				default:
					return nil, kerrors.NewNotFound(schema.GroupResource{Group: "rbac.k8s.io/v1", Resource: "clusterrolebindings"}, name)
				}
			},
			getLocalClusterRoleBinding: func(workspace logicalcluster.Name, name string) (*rbacv1.ClusterRoleBinding, error) {
				switch {
				case workspace.String() == "root:users:ab:cd" && name == "system:kcp:tenancy:home-owner:user-1":
					fallthrough
				case workspace.String() == "root:users:ab:cd:user-1" && name == "home-owner":
					return &rbacv1.ClusterRoleBinding{}, nil
				default:
					return nil, kerrors.NewNotFound(schema.GroupResource{Group: "rbac.k8s.io/v1", Resource: "clusterrolebindings"}, name)
				}
			},

			expectedFound: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(
			testCase.testName,
			func(t *testing.T) {
				handler := homeWorkspaceHandlerBuilder{
					bucketLevels:         2,
					bucketSize:           2,
					homePrefix:           logicalcluster.New("root:users"),
					creationDelaySeconds: creationDelaySeconds,
					localInformers: localInformersAccess{
						getClusterRole:        testCase.getLocalClusterRole,
						getClusterRoleBinding: testCase.getLocalClusterRoleBinding,
					},
				}.build()

				found, searchError := handler.searchForHomeWorkspaceRBACResourcesInLocalInformers(logicalcluster.New(testCase.workspaceName))

				require.Equal(t, testCase.expectedFound, found, "'found' value is wrong")
				var searchErrorString string
				if searchError != nil {
					searchErrorString = searchError.Error()
				}
				require.Equal(t, testCase.expectedSearchError, searchErrorString, "'searchError' value is wrong")
			})
	}
}

type workspacedCreatedResource struct {
	workspace string
	resource  runtime.Object
}

func TestCreateHomeWorkspaceRBACResources(t *testing.T) {
	creationDelaySeconds := 5
	testCases := []struct {
		testName string

		context       context.Context
		workspaceName string
		userName      string

		createClusterRole        func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRole) error
		createClusterRoleBinding func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRoleBinding) error

		expectedCreateError      string
		expectedCreatedResources []workspacedCreatedResource
	}{
		{
			testName: "return error when unexpected error on ClusterRole create on parent",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			createClusterRole: func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRole) error {
				if workspace.String() == "root:users:ab:cd" {
					return errors.New("error when creating ClusterRole")
				}
				return nil
			},

			createClusterRoleBinding: func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRoleBinding) error {
				return nil
			},

			expectedCreateError: "error when creating ClusterRole",
		},
		{
			testName: "return error when unexpected error on ClusterRoleBinding create on parent",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			createClusterRole: func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRole) error {
				return nil
			},

			createClusterRoleBinding: func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRoleBinding) error {
				if workspace.String() == "root:users:ab:cd" {
					return errors.New("error when creating ClusterRoleBinding")
				}
				return nil
			},

			expectedCreateError: "error when creating ClusterRoleBinding",
		},
		{
			testName: "Successfully create RBAC resources",

			workspaceName: "root:users:ab:cd:user-1",
			userName:      "user-1",

			createClusterRole: func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRole) error {
				return nil
			},

			createClusterRoleBinding: func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRoleBinding) error {
				return nil
			},

			expectedCreatedResources: []workspacedCreatedResource{
				{
					workspace: "root:users:ab:cd",
					resource: &rbacv1.ClusterRole{
						ObjectMeta: metav1.ObjectMeta{Name: "system:kcp:tenancy:home-owner:user-1"},
						Rules: []rbacv1.PolicyRule{
							{
								APIGroups:     []string{tenancyv1beta1.SchemeGroupVersion.Group},
								Resources:     []string{"clusterworkspaces/content"},
								Verbs:         []string{"access"},
								ResourceNames: []string{"user-1"},
							},
						},
					},
				},
				{
					workspace: "root:users:ab:cd",
					resource: &rbacv1.ClusterRoleBinding{
						ObjectMeta: metav1.ObjectMeta{Name: "system:kcp:tenancy:home-owner:user-1"},
						RoleRef: rbacv1.RoleRef{
							Kind:     "ClusterRole",
							APIGroup: "rbac.authorization.k8s.io",
							Name:     "system:kcp:tenancy:home-owner:user-1",
						},
						Subjects: []rbacv1.Subject{
							{
								Kind:      "User",
								Name:      "user-1",
								Namespace: "",
							},
						},
					},
				},
			},
		},
		{
			testName: "Successfully create RBAC resources with cleaned user Name",

			workspaceName: "root:users:ab:cd:system-apiserver-company-2",
			userName:      "2system:apiserver@company-2à-",

			createClusterRole: func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRole) error {
				return nil
			},

			createClusterRoleBinding: func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRoleBinding) error {
				return nil
			},

			expectedCreatedResources: []workspacedCreatedResource{
				{
					workspace: "root:users:ab:cd",
					resource: &rbacv1.ClusterRole{
						ObjectMeta: metav1.ObjectMeta{Name: "system:kcp:tenancy:home-owner:system-apiserver-company-2"},
						Rules: []rbacv1.PolicyRule{
							{
								APIGroups:     []string{tenancyv1beta1.SchemeGroupVersion.Group},
								Resources:     []string{"clusterworkspaces/content"},
								Verbs:         []string{"access"},
								ResourceNames: []string{"system-apiserver-company-2"},
							},
						},
					},
				},
				{
					workspace: "root:users:ab:cd",
					resource: &rbacv1.ClusterRoleBinding{
						ObjectMeta: metav1.ObjectMeta{Name: "system:kcp:tenancy:home-owner:system-apiserver-company-2"},
						RoleRef: rbacv1.RoleRef{
							Kind:     "ClusterRole",
							APIGroup: "rbac.authorization.k8s.io",
							Name:     "system:kcp:tenancy:home-owner:system-apiserver-company-2",
						},
						Subjects: []rbacv1.Subject{
							{
								Kind:      "User",
								Name:      "2system:apiserver@company-2à-",
								Namespace: "",
							},
						},
					},
				},
				{
					workspace: "root:users:ab:cd:system-apiserver-company-2",
					resource: &rbacv1.ClusterRole{
						ObjectMeta: metav1.ObjectMeta{Name: "home-owner"},
						Rules: []rbacv1.PolicyRule{
							{
								APIGroups: []string{tenancyv1beta1.SchemeGroupVersion.Group},
								Resources: []string{"clusterworkspaces/workspace"},
								Verbs:     []string{"create", "get", "list", "watch"},
							},
						},
					},
				},
				{
					workspace: "root:users:ab:cd:system-apiserver-company-2",
					resource: &rbacv1.ClusterRoleBinding{
						ObjectMeta: metav1.ObjectMeta{Name: "home-owner"},
						RoleRef: rbacv1.RoleRef{
							Kind:     "ClusterRole",
							APIGroup: "rbac.authorization.k8s.io",
							Name:     "home-owner",
						},
						Subjects: []rbacv1.Subject{
							{
								Kind:      "User",
								Name:      "2system:apiserver@company-2à-",
								Namespace: "",
							},
						},
					},
				},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(
			testCase.testName,
			func(t *testing.T) {
				var createdResources []workspacedCreatedResource

				handler := homeWorkspaceHandlerBuilder{
					bucketLevels:         2,
					bucketSize:           2,
					homePrefix:           logicalcluster.New("root:users"),
					creationDelaySeconds: creationDelaySeconds,
					kcp: externalKubeClientsAccess{
						createClusterRole: func(ctx context.Context, workspace logicalcluster.Name, cr *rbacv1.ClusterRole) error {
							err := testCase.createClusterRole(ctx, workspace, cr)
							if err == nil {
								createdResources = append(createdResources, workspacedCreatedResource{
									workspace: workspace.String(),
									resource:  cr,
								})
							}
							return err
						},
						createClusterRoleBinding: func(ctx context.Context, workspace logicalcluster.Name, crb *rbacv1.ClusterRoleBinding) error {
							err := testCase.createClusterRoleBinding(ctx, workspace, crb)
							if err == nil {
								createdResources = append(createdResources, workspacedCreatedResource{
									workspace: workspace.String(),
									resource:  crb,
								})
							}
							return err
						},
					},
				}.build()

				createError := handler.createHomeWorkspaceRBACResources(testCase.context, testCase.userName, logicalcluster.New(testCase.workspaceName))

				var createErrorString string
				if createError != nil {
					createErrorString = createError.Error()
				}
				require.Equal(t, testCase.expectedCreateError, createErrorString, "'createError' value is wrong")
			})
	}
}

func TestServeHTTP(t *testing.T) {
	creationDelaySeconds := 5
	testCases := []struct {
		testName string

		contextCluster     *request.Cluster
		contextRequestInfo *request.RequestInfo
		contextUser        *kuser.DefaultInfo
		userName           string

		synced                   bool
		getLocalClusterWorkspace func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error)
		authz                    authorizer.AuthorizerFunc

		mocks homeWorkspaceFeatureLogic

		expectedToDelegate      bool
		expectedResponseBody    string
		expectedStatusCode      int
		expectedResponseHeaders map[string]string
	}{
		{
			testName:           "Error when no cluster in context",
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{},
			synced:             true,

			expectedStatusCode:   500,
			expectedToDelegate:   false,
			expectedResponseBody: `Internal Server Error: "/dummy-target": no cluster in the request context - RequestInfo: &amp;request.RequestInfo{IsResourceRequest:false, Path:"", Verb:"", APIPrefix:"", APIGroup:"", APIVersion:"", Namespace:"", Resource:"", Subresource:"", Name:"", Parts:[]string(nil)}`,
		},
		{
			testName:           "Error when no user in context",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root:org1:proj1")},
			contextRequestInfo: &request.RequestInfo{},
			synced:             true,

			expectedStatusCode:   500,
			expectedToDelegate:   false,
			expectedResponseBody: `Internal Server Error: "/dummy-target": No user in HomeWorkspaces filter !`,
		},
		{
			testName:       "Error when no RequestInfo in context",
			contextCluster: &request.Cluster{Name: logicalcluster.New("root:org1:proj1")},
			contextUser:    &kuser.DefaultInfo{Name: "user-1"},
			synced:         true,

			expectedStatusCode:   500,
			expectedToDelegate:   false,
			expectedResponseBody: `Internal Server Error: "/dummy-target": no request Info`,
		},
		{
			testName:           "delegate to next handler when request not under home root nor a 'get ~' request",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root:org1:proj1")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{},
			synced:             true,

			expectedStatusCode: 200,
			expectedToDelegate: true,
		},
		{
			testName:           "delegate to next handler when handler informers are not synced",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "~", Verb: "get"},
			synced:             false,

			expectedStatusCode: 200,
			expectedToDelegate: true,
		},
		{
			testName:           "try to create home workspace when it doesn't exist",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "~", Verb: "get"},
			synced:             true,
			authz: func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionAllow, "", nil
			},
			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return nil, kerrors.NewNotFound(tenancyv1alpha1.Resource("clusterworkspaces"), fullName.String())
			},
			mocks: homeWorkspaceFeatureLogic{
				searchForWorkspaceAndRBACInLocalInformers: func(workspaceName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
					return
				},
				tryToCreate: func(ctx context.Context, user kuser.Info, workspaceToCheck logicalcluster.Name, workspaceType tenancyv1alpha1.ClusterWorkspaceTypeName) (retryAfterSeconds int, createError error) {
					retryAfterSeconds = 11
					return
				},
			},

			expectedStatusCode:   429,
			expectedToDelegate:   false,
			expectedResponseBody: "Creating the home workspace",
			expectedResponseHeaders: map[string]string{
				"Retry-After": "11",
			},
		},
		{
			testName:           "return error when home workspace has a different owner",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "~", Verb: "get"},

			synced: true,
			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:bi:ie:user-2").withType("root:home").withRV("someRealResourceVersion").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:   tenancyv1alpha1.ClusterWorkspacePhaseReady,
					BaseURL: "https://example.com/clusters/root:users:bi:ie:user-1",
				}).ownedBy("user-2").ClusterWorkspace, nil
			},

			expectedStatusCode:   403,
			expectedToDelegate:   false,
			expectedResponseBody: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"clusterworkspaces.tenancy.kcp.dev \"~\" is forbidden: User \"user-1\" cannot get resource \"clusterworkspaces/workspace\" in API group \"tenancy.kcp.dev\" at the cluster scope: workspace access not permitted","reason":"Forbidden","details":{"name":"~","group":"tenancy.kcp.dev","kind":"clusterworkspaces"},"code":403}`,
		},
		{
			testName:           "return error if error when getting home workspace in the local informers",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "~", Verb: "get"},

			synced: true,
			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return nil, errors.New("an error")
			},

			expectedStatusCode:   500,
			expectedToDelegate:   false,
			expectedResponseBody: `Internal Server Error: "/dummy-target": an error`,
		},
		{
			testName:           "return the real home workspace when it already exists and is ready in informer, and RBAC objects are in informer",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "~", Verb: "get"},

			synced: true,
			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:bi:ie:user-1").withType("root:home").withRV("someRealResourceVersion").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:   tenancyv1alpha1.ClusterWorkspacePhaseReady,
					BaseURL: "https://example.com/clusters/root:users:bi:ie:user-1",
				}).ownedBy("user-1").ClusterWorkspace, nil
			},
			authz: func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionAllow, "", nil
			},
			mocks: homeWorkspaceFeatureLogic{
				searchForHomeWorkspaceRBACResourcesInLocalInformers: func(logicalClusterName logicalcluster.Name) (found bool, err error) {
					return true, nil
				},
			},

			expectedStatusCode:   200,
			expectedToDelegate:   false,
			expectedResponseBody: `{"kind":"Workspace","apiVersion":"tenancy.kcp.dev/v1beta1","metadata":{"name":"user-1","resourceVersion":"someRealResourceVersion","creationTimestamp":null,"annotations":{"kcp.dev/cluster":"root:users:bi:ie"}},"spec":{"type":{"name":"home","path":"root"}},"status":{"URL":"https://example.com/clusters/root:users:bi:ie:user-1","phase":"Ready"}}`,
		},
		{
			testName:           "try to create home workspace when the home workspace is not ready yet",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "~", Verb: "get"},

			synced: true,
			authz: func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionAllow, "", nil
			},
			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:bi:ie:user-1").withType("root:home").withRV("someRealResourceVersion").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:   tenancyv1alpha1.ClusterWorkspacePhaseInitializing,
					BaseURL: "https://example.com/clusters/root:users:bi:ie:user-1",
				}).ownedBy("user-1").ClusterWorkspace, nil
			},
			mocks: homeWorkspaceFeatureLogic{
				searchForWorkspaceAndRBACInLocalInformers: func(workspaceName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
					return false, 0, nil
				},
				searchForHomeWorkspaceRBACResourcesInLocalInformers: func(logicalClusterName logicalcluster.Name) (bool, error) {
					return false, nil
				},
				tryToCreate: func(ctx context.Context, user kuser.Info, workspaceToCheck logicalcluster.Name, workspaceType tenancyv1alpha1.ClusterWorkspaceTypeName) (retryAfterSeconds int, createError error) {
					retryAfterSeconds = 11
					return
				},
			},

			expectedStatusCode:   429,
			expectedToDelegate:   false,
			expectedResponseBody: "Creating the home workspace",
			expectedResponseHeaders: map[string]string{
				"Retry-After": "11",
			},
		},
		{
			testName:           "return virtual home workspace when home already exists, but RBAC objects are not in informer",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "~", Verb: "get"},

			synced: true,
			getLocalClusterWorkspace: func(fullName logicalcluster.Name) (*tenancyv1alpha1.ClusterWorkspace, error) {
				return newWorkspace("root:users:bi:ie:user-1").withType("root:home").withRV("someRealResourceVersion").withStatus(tenancyv1alpha1.ClusterWorkspaceStatus{
					Phase:   tenancyv1alpha1.ClusterWorkspacePhaseReady,
					BaseURL: "https://example.com/clusters/root:users:bi:ie:user-1",
				}).ownedBy("user-1").ClusterWorkspace, nil
			},
			authz: func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionAllow, "", nil
			},
			mocks: homeWorkspaceFeatureLogic{
				searchForHomeWorkspaceRBACResourcesInLocalInformers: func(logicalClusterName logicalcluster.Name) (found bool, err error) {
					return false, nil
				},
				searchForWorkspaceAndRBACInLocalInformers: func(workspaceName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
					return
				},
				tryToCreate: func(ctx context.Context, user kuser.Info, workspaceToCheck logicalcluster.Name, workspaceType tenancyv1alpha1.ClusterWorkspaceTypeName) (retryAfterSeconds int, createError error) {
					retryAfterSeconds = 11
					return
				},
			},

			expectedStatusCode:   429,
			expectedToDelegate:   false,
			expectedResponseBody: "Creating the home workspace",
			expectedResponseHeaders: map[string]string{
				"Retry-After": "11",
			},
		},
		{
			testName:           "return error when workspace cannot be checked in local informers",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root:users:bi:ie")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "user-1", Verb: "create"},

			synced: true,
			mocks: homeWorkspaceFeatureLogic{
				searchForWorkspaceAndRBACInLocalInformers: func(workspaceName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
					return false, 0, errors.New("an error")
				},
			},

			expectedStatusCode:   500,
			expectedResponseBody: `Internal Server Error: "/dummy-target": an error`,
		},
		{
			testName:           "retry later if instructed so when checking for the workspace in local informers",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root:users:bi:ie")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "user-1", Verb: "create"},

			synced: true,
			mocks: homeWorkspaceFeatureLogic{
				searchForWorkspaceAndRBACInLocalInformers: func(workspaceName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
					found = true
					retryAfterSeconds = 10
					return
				},
			},

			expectedStatusCode:   429,
			expectedToDelegate:   false,
			expectedResponseBody: "Creating the home workspace",
			expectedResponseHeaders: map[string]string{
				"Retry-After": "10",
			},
		},
		{
			testName:           "return Forbidden and don't try to create home workspace because user is the wrong one",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root:users:bi:ie:user-1")},
			contextUser:        &kuser.DefaultInfo{Name: "user-2"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "create-test", Verb: "create"},

			synced: true,
			mocks: homeWorkspaceFeatureLogic{
				searchForWorkspaceAndRBACInLocalInformers: func(workspaceName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
					return
				},
			},

			expectedStatusCode:   403,
			expectedResponseBody: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"clusterworkspaces.tenancy.kcp.dev \"~\" is forbidden: User \"user-2\" cannot create resource \"clusterworkspaces/workspace\" in API group \"tenancy.kcp.dev\" at the cluster scope: workspace access not permitted","reason":"Forbidden","details":{"name":"~","group":"tenancy.kcp.dev","kind":"clusterworkspaces"},"code":403}`,
		},
		{
			testName:           "return Forbidden and don't try to create home workspace because user doesn't have permission",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root:users:bi:ie:user-1")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "create-test", Verb: "create"},

			synced: true,
			authz: func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionDeny, "refused for a given reason", nil
			},
			mocks: homeWorkspaceFeatureLogic{
				searchForWorkspaceAndRBACInLocalInformers: func(workspaceName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
					return
				},
			},

			expectedStatusCode:   403,
			expectedResponseBody: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"clusterworkspaces.tenancy.kcp.dev \"~\" is forbidden: User \"user-1\" cannot create resource \"clusterworkspaces/workspace\" in API group \"tenancy.kcp.dev\" at the cluster scope: refused for a given reason","reason":"Forbidden","details":{"name":"~","group":"tenancy.kcp.dev","kind":"clusterworkspaces"},"code":403}`,
		},
		{
			testName:           "return Forbidden and don't try to create home workspace because unable to check user permission",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root:users:bi:ie:user-1")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "create-test", Verb: "create"},

			synced: true,
			authz: func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionDeny, "", errors.New("error when checking user permission")
			},
			mocks: homeWorkspaceFeatureLogic{
				searchForWorkspaceAndRBACInLocalInformers: func(workspaceName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
					return
				},
			},

			expectedStatusCode:   403,
			expectedResponseBody: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"clusterworkspaces.tenancy.kcp.dev \"~\" is forbidden: User \"user-1\" cannot create resource \"clusterworkspaces/workspace\" in API group \"tenancy.kcp.dev\" at the cluster scope: workspace access not permitted","reason":"Forbidden","details":{"name":"~","group":"tenancy.kcp.dev","kind":"clusterworkspaces"},"code":403}`,
		},
		{
			testName:           "try to create when home workspace doesn't exist and user has permission",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root:users:bi:ie:user-1")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "create-test", Verb: "create"},

			synced: true,
			authz: func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionAllow, "", nil
			},
			mocks: homeWorkspaceFeatureLogic{
				searchForWorkspaceAndRBACInLocalInformers: func(workspaceName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
					return
				},
				tryToCreate: func(ctx context.Context, user kuser.Info, workspaceToCheck logicalcluster.Name, workspaceType tenancyv1alpha1.ClusterWorkspaceTypeName) (retryAfterSeconds int, createError error) {
					retryAfterSeconds = 11
					return
				},
			},

			expectedStatusCode:   429,
			expectedToDelegate:   false,
			expectedResponseBody: "Creating the home workspace",
			expectedResponseHeaders: map[string]string{
				"Retry-After": "11",
			},
		},
		{
			testName:           "try to create when home workspace doesn't exist and user has permission, but creation failed",
			contextCluster:     &request.Cluster{Name: logicalcluster.New("root:users:bi:ie:user-1")},
			contextUser:        &kuser.DefaultInfo{Name: "user-1"},
			contextRequestInfo: &request.RequestInfo{IsResourceRequest: true, APIGroup: "tenancy.kcp.dev", Resource: "workspaces", Name: "create-test", Verb: "create"},

			synced: true,
			authz: func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
				return authorizer.DecisionAllow, "", nil
			},
			mocks: homeWorkspaceFeatureLogic{
				searchForWorkspaceAndRBACInLocalInformers: func(workspaceName logicalcluster.Name, isHome bool, userName string) (found bool, retryAfterSeconds int, checkError error) {
					return
				},
				tryToCreate: func(ctx context.Context, user kuser.Info, workspaceToCheck logicalcluster.Name, workspaceType tenancyv1alpha1.ClusterWorkspaceTypeName) (retryAfterSeconds int, createError error) {
					return 0, errors.New("error when trying to create the home workspace")
				},
			},

			expectedStatusCode:   500,
			expectedToDelegate:   false,
			expectedResponseBody: `{"kind":"Status","apiVersion":"v1","metadata":{},"status":"Failure","message":"error when trying to create the home workspace","code":500}`,
		},
	}

	for _, testCase := range testCases {
		t.Run(
			testCase.testName,
			func(t *testing.T) {
				delegatedToHandlerChain := false

				ctx := context.TODO()
				if testCase.contextCluster != nil {
					ctx = request.WithCluster(ctx, *testCase.contextCluster)
				}
				if testCase.contextRequestInfo != nil {
					ctx = request.WithRequestInfo(ctx, testCase.contextRequestInfo)
				}
				if testCase.contextUser != nil {
					ctx = request.WithUser(ctx, testCase.contextUser)
				}

				r := httptest.NewRequest("GET", "/dummy-target", nil).WithContext(ctx)

				rw := httptest.NewRecorder()

				handler := homeWorkspaceHandlerBuilder{
					bucketLevels:         2,
					bucketSize:           2,
					homePrefix:           logicalcluster.New("root:users"),
					creationDelaySeconds: creationDelaySeconds,

					externalHost: "example.com",
					authz:        testCase.authz,
					localInformers: localInformersAccess{
						getClusterWorkspace: testCase.getLocalClusterWorkspace,
						synced:              func() bool { return testCase.synced },
					},
					apiHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						delegatedToHandlerChain = true
					}),
				}.build()

				overrideLogic(handler, testCase.mocks).ServeHTTP(rw, r)

				result := rw.Result()
				require.Equal(t, testCase.expectedStatusCode, result.StatusCode, "'statusCode' value is wrong")
				require.Equal(t, testCase.expectedToDelegate, delegatedToHandlerChain, "'delegatedToHandlerChain' value is wrong")
				bytes, err := io.ReadAll(result.Body)
				require.NoError(t, err, "Request body cannot be read")
				require.Equal(t, testCase.expectedResponseBody, strings.TrimRight(string(bytes), "\n"), "response body value is wrong")
				for expectedHeaderKey, expectedHeader := range testCase.expectedResponseHeaders {
					require.Equalf(t, expectedHeader, result.Header.Get(expectedHeaderKey), "response header %q value are wrong", expectedHeaderKey)
				}
			})
	}
}

func overrideLogic(handler *homeWorkspaceHandler, overridenLogic homeWorkspaceFeatureLogic) *homeWorkspaceHandler {
	if overridenLogic.createHomeWorkspaceRBACResources != nil {
		handler.createHomeWorkspaceRBACResources = overridenLogic.createHomeWorkspaceRBACResources
	}
	if overridenLogic.searchForHomeWorkspaceRBACResourcesInLocalInformers != nil {
		handler.searchForHomeWorkspaceRBACResourcesInLocalInformers = overridenLogic.searchForHomeWorkspaceRBACResourcesInLocalInformers
	}
	if overridenLogic.searchForWorkspaceAndRBACInLocalInformers != nil {
		handler.searchForWorkspaceAndRBACInLocalInformers = overridenLogic.searchForWorkspaceAndRBACInLocalInformers
	}
	if overridenLogic.tryToCreate != nil {
		handler.tryToCreate = overridenLogic.tryToCreate
	}
	return handler
}

type wsBuilder struct {
	*tenancyv1alpha1.ClusterWorkspace
}

func newWorkspace(qualifiedName string) wsBuilder {
	path, name := logicalcluster.New(qualifiedName).Split()
	return wsBuilder{&tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: path.String(),
			},
		},
	}}
}

func (b wsBuilder) inPhase(phase tenancyv1alpha1.ClusterWorkspacePhaseType) wsBuilder {
	b.Status.Phase = phase
	return b
}

func (b wsBuilder) withRV(rv string) wsBuilder {
	b.ResourceVersion = rv
	return b
}

func (b wsBuilder) withType(qualifiedName string) wsBuilder {
	path, name := logicalcluster.New(qualifiedName).Split()
	b.Spec.Type = tenancyv1alpha1.ClusterWorkspaceTypeReference{
		Path: path.String(),
		Name: tenancyv1alpha1.ClusterWorkspaceTypeName(name),
	}
	return b
}

func (b wsBuilder) withStatus(status tenancyv1alpha1.ClusterWorkspaceStatus) wsBuilder {
	b.Status = status
	return b
}

func (b wsBuilder) unschedulable() wsBuilder {
	b.Status.Conditions = append(b.Status.Conditions, v1alpha1.Condition{
		Type:   tenancyv1alpha1.WorkspaceScheduled,
		Status: corev1.ConditionFalse,
		Reason: tenancyv1alpha1.WorkspaceReasonReasonUnknown,
	})
	return b
}

func (b wsBuilder) ownedBy(user string) wsBuilder {
	bs, err := json.Marshal(map[string]string{
		"username": user,
	})
	if err != nil {
		panic(err)
	}
	return b.withAnnotations(map[string]string{
		"experimental.tenancy.kcp.dev/owner": string(bs),
	})
}

func (b wsBuilder) withAnnotations(annotations map[string]string) wsBuilder {
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	for k, v := range annotations {
		b.Annotations[k] = v
	}
	return b
}
