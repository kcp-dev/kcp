/*
Copyright 2022 The kcp Authors.

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

package forwardingregistry

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/watch"
)

func TestUpdateToCreateOptions(t *testing.T) {
	updateOptions := reflect.TypeOf(metav1.UpdateOptions{})
	numField := updateOptions.NumField()
	require.Equalf(t, 4, numField, "UpdateOptions is expected to have 4 fields")

	fields := make([]string, numField)
	for i := range numField {
		fields[i] = updateOptions.Field(i).Name
	}

	// Assert the UpdateOptions struct fields set has not changed
	expectedFields := []string{
		"TypeMeta",
		"DryRun",
		"FieldManager",
		"FieldValidation",
	}
	require.ElementsMatchf(t, expectedFields, fields, "UpdateOptions struct fields have changed")

	// Assert the CreateOptions fields match that of the UpdateOptions
	uo := &metav1.UpdateOptions{
		DryRun: []string{
			"All",
		},
		FieldManager:    "manager",
		FieldValidation: "Strict",
	}
	co := updateToCreateOptions(uo)

	expectedCreateOptions := metav1.CreateOptions{
		TypeMeta: metav1.TypeMeta{
			Kind:       "CreateOptions",
			APIVersion: "meta.k8s.io/v1",
		},
		DryRun: []string{
			"All",
		},
		FieldManager:    "manager",
		FieldValidation: "Strict",
	}
	require.Equalf(t, expectedCreateOptions, co, "CreateOptions should have the same fields as the UpdateOptions")
}

func TestShouldPrefetchWatchListResourceVersion(t *testing.T) {
	sendInitialEvents := true
	require.True(t, shouldPrefetchWatchListResourceVersion("hash", metav1.ListOptions{SendInitialEvents: &sendInitialEvents}))
	require.False(t, shouldPrefetchWatchListResourceVersion("", metav1.ListOptions{SendInitialEvents: &sendInitialEvents}))
	require.False(t, shouldPrefetchWatchListResourceVersion("hash", metav1.ListOptions{}))
	require.False(t, shouldPrefetchWatchListResourceVersion("hash", metav1.ListOptions{
		SendInitialEvents: &sendInitialEvents,
		ResourceVersion:   "10",
	}))
}

func TestWaitForWatchListResourceVersionWithRetry(t *testing.T) {
	t.Run("retries transient error", func(t *testing.T) {
		attempt := 0
		lw := &fakeListerWatcher{
			listFn: func() (*unstructured.UnstructuredList, error) {
				if attempt < 2 {
					attempt++
					return nil, apierrors.NewServiceUnavailable("cache not ready")
				}
				attempt++
				return &unstructured.UnstructuredList{
					Object: map[string]interface{}{
						"metadata": map[string]interface{}{"resourceVersion": "42"},
					},
				}, nil
			},
		}

		resourceVersion, err := waitForWatchListResourceVersionWithRetry(context.Background(), lw, 3, 0)
		require.NoError(t, err)
		require.Equal(t, "42", resourceVersion)
		require.Equal(t, 3, lw.listCalls)
	})

	t.Run("fails fast for non-retryable error", func(t *testing.T) {
		lw := &fakeListerWatcher{
			listFn: func() (*unstructured.UnstructuredList, error) {
				return nil, apierrors.NewBadRequest("invalid list")
			},
		}

		_, err := waitForWatchListResourceVersionWithRetry(context.Background(), lw, 3, 0)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid list")
		require.Equal(t, 1, lw.listCalls)
	})

	t.Run("fails after retries on empty resourceVersion", func(t *testing.T) {
		lw := &fakeListerWatcher{
			listFn: func() (*unstructured.UnstructuredList, error) {
				return &unstructured.UnstructuredList{}, nil
			},
		}

		_, err := waitForWatchListResourceVersionWithRetry(context.Background(), lw, 2, 0)
		require.EqualError(t, err, "empty resourceVersion from list response")
		require.Equal(t, 2, lw.listCalls)
	})

	t.Run("returns context error when canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		lw := &fakeListerWatcher{
			listFn: func() (*unstructured.UnstructuredList, error) {
				return nil, apierrors.NewTimeoutError("timed out", 1)
			},
		}

		_, err := waitForWatchListResourceVersionWithRetry(ctx, lw, 2, time.Second)
		require.ErrorIs(t, err, context.Canceled)
	})
}

type fakeListerWatcher struct {
	listFn    func() (*unstructured.UnstructuredList, error)
	watchFn   func() (watch.Interface, error)
	listCalls int
}

func (f *fakeListerWatcher) List(_ context.Context, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if f.listFn == nil {
		return nil, fmt.Errorf("listFn must be provided")
	}
	f.listCalls++
	return f.listFn()
}

func (f *fakeListerWatcher) Watch(_ context.Context, _ metav1.ListOptions) (watch.Interface, error) {
	if f.watchFn == nil {
		return watch.NewEmptyWatch(), nil
	}
	return f.watchFn()
}
