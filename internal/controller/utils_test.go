package controller

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	ecv1alpha1 "go.etcd.io/etcd-operator/api/v1alpha1"
	"go.etcd.io/etcd-operator/internal/etcdutils"
	"go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"testing"
)

func TestMain(m *testing.M) {
	ctrl.SetLogger(logr.Discard()) // No-op logger
	m.Run()
}

func TestPrepareOwnerReference(t *testing.T) {
	scheme := runtime.NewScheme()
	ec := &ecv1alpha1.EtcdCluster{}
	ec.SetName("test-etcd")
	ec.SetNamespace("default")
	ec.SetUID("1234")

	scheme.AddKnownTypes(schema.GroupVersion{Group: "etcd.database.coreos.com", Version: "v1alpha1"}, ec)

	owners, err := prepareOwnerReference(ec, scheme)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(owners) != 1 {
		t.Fatalf("expected 1 owner reference, got %d", len(owners))
	}

	if owners[0].Name != "test-etcd" || owners[0].Controller == nil || !*owners[0].Controller {
		t.Fatalf("owner reference properties not set correctly")
	}
}

func pointerToInt32(value int32) *int32 {
	return &value
}

func TestCreateOrPatchStatefulSet(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = ecv1alpha1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().Build()
	logger := log.FromContext(context.Background())

	ec := &ecv1alpha1.EtcdCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-etcd",
			Namespace: "default",
		},
		Spec: ecv1alpha1.EtcdClusterSpec{
			Size:    3,
			Version: "3.5.17",
		},
	}

	_, err := createOrPatchStatefulSet(context.Background(), logger, ec, fakeClient, 3, scheme)

	sts := &appsv1.StatefulSet{}
	err = fakeClient.Get(context.Background(), client.ObjectKey{Name: "test-etcd", Namespace: "default"}, sts)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if *sts.Spec.Replicas != 3 {
		t.Fatalf("expected 3 replicas, got %d", *sts.Spec.Replicas)
	}
}

func TestWaitForStatefulSetReady(t *testing.T) {
	// Create a scheme and register the necessary types
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = ecv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	tests := []struct {
		name           string
		statefulSet    *appsv1.StatefulSet
		expectedResult bool
		expectedError  error
	}{
		{
			name: "StatefulSet is ready",
			statefulSet: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: pointerToInt32(3),
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: 3,
				},
			},
			expectedResult: true,
			expectedError:  nil,
		},
		{
			name: "StatefulSet is not ready",
			statefulSet: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: pointerToInt32(3),
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: 2,
				},
			},
			expectedResult: false,
			expectedError:  errors.New("StatefulSet default/test-sts did not become ready after 5 attempts"),
		},
		{
			name:           "StatefulSet does not exist",
			statefulSet:    nil,
			expectedResult: false,
			expectedError:  errors.New("statefulsets.apps \"test-sts\" not found"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var clientBuilder *fake.ClientBuilder
			if tt.statefulSet != nil {
				clientBuilder = fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.statefulSet)
			} else {
				clientBuilder = fake.NewClientBuilder().WithScheme(scheme)
			}
			fakeClient := clientBuilder.Build()

			ctx := context.Background()
			logger := log.FromContext(ctx)

			err := waitForStatefulSetReady(ctx, logger, fakeClient, "test-sts", "default")
			if tt.expectedError != nil {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCreateHeadlessServiceIfNotExist(t *testing.T) {
	ctx := context.TODO()
	logger := log.FromContext(ctx)

	// Create a scheme and register the necessary types
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = ecv1alpha1.AddToScheme(scheme)

	// Create a fake client
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Create an EtcdCluster instance
	ec := &ecv1alpha1.EtcdCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-etcd",
			Namespace: "default",
		},
	}

	t.Run("creates headless service if it does not exist", func(t *testing.T) {
		err := createHeadlessServiceIfNotExist(ctx, logger, fakeClient, ec, scheme)
		assert.NoError(t, err)

		// Verify that the service was created
		service := &corev1.Service{}
		err = fakeClient.Get(ctx, client.ObjectKey{Name: "test-etcd", Namespace: "default"}, service)
		assert.NoError(t, err)
		assert.Equal(t, "None", service.Spec.ClusterIP)
		assert.Equal(t, map[string]string{
			"app":        "test-etcd",
			"controller": "test-etcd",
		}, service.Spec.Selector)
	})

	t.Run("does not create service if it already exists", func(t *testing.T) {
		// Service was already created in previous test. Call the function again to ensure no error
		err := createHeadlessServiceIfNotExist(ctx, logger, fakeClient, ec, scheme)
		assert.NoError(t, err)
	})
}

func TestClientEndpointForOrdinalIndex(t *testing.T) {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sts",
			Namespace: "default",
		},
	}

	tests := []struct {
		index          int
		expectedResult string
	}{
		{index: 0, expectedResult: "http://test-sts-0.test-sts.default.svc.cluster.local:2379"},
		{index: 1, expectedResult: "http://test-sts-1.test-sts.default.svc.cluster.local:2379"},
		{index: 2, expectedResult: "http://test-sts-2.test-sts.default.svc.cluster.local:2379"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("index %d", tt.index), func(t *testing.T) {
			result := clientEndpointForOrdinalIndex(sts, tt.index)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestIsLearnerReady(t *testing.T) {
	tests := []struct {
		name           string
		leaderStatus   *clientv3.StatusResponse
		learnerStatus  *clientv3.StatusResponse
		expectedResult bool
	}{
		{
			name: "Learner is ready",
			leaderStatus: &clientv3.StatusResponse{
				Header: &etcdserverpb.ResponseHeader{Revision: 100},
			},
			learnerStatus: &clientv3.StatusResponse{
				Header: &etcdserverpb.ResponseHeader{Revision: 95},
			},
			expectedResult: true,
		},
		{
			name: "Learner is not ready",
			leaderStatus: &clientv3.StatusResponse{
				Header: &etcdserverpb.ResponseHeader{Revision: 100},
			},
			learnerStatus: &clientv3.StatusResponse{
				Header: &etcdserverpb.ResponseHeader{Revision: 80},
			},
			expectedResult: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := etcdutils.IsLearnerReady(tt.leaderStatus, tt.learnerStatus)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestCheckStatefulSetControlledByEtcdOperator(t *testing.T) {
	tests := []struct {
		name          string
		ec            *ecv1alpha1.EtcdCluster
		sts           *appsv1.StatefulSet
		expectedError error
	}{
		{
			name: "StatefulSet controlled by EtcdCluster",
			ec: &ecv1alpha1.EtcdCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "etcd-cluster",
					Namespace: "default",
					UID:       "1234",
				},
			},
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "etcd-sts",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "ecv1alpha1/v1alpha1",
							Kind:       "EtcdCluster",
							Name:       "etcd-cluster",
							UID:        "1234",
							Controller: pointerToBool(true),
						},
					},
				},
			},
			expectedError: nil,
		},
		{
			name: "StatefulSet not controlled by EtcdCluster",
			ec: &ecv1alpha1.EtcdCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "etcd-cluster",
					Namespace: "default",
					UID:       "1234",
				},
			},
			sts: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "etcd-sts",
					Namespace: "default",
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion: "ecv1alpha1/v1alpha1",
							Kind:       "EtcdCluster",
							Name:       "other-etcd-cluster",
							UID:        "5678",
							Controller: pointerToBool(true),
						},
					},
				},
			},
			expectedError: fmt.Errorf("StatefulSet default/etcd-sts is not controlled by EtcdCluster default/etcd-cluster"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkStatefulSetControlledByEtcdOperator(tt.ec, tt.sts)

			if (err != nil) != (tt.expectedError != nil) {
				t.Errorf("expected error: %v, got: %v", tt.expectedError, err)
				return
			}

			if err != nil && err.Error() != tt.expectedError.Error() {
				t.Errorf("unexpected error: got %v, want %v", err, tt.expectedError)
			}
		})
	}
}

func pointerToBool(value bool) *bool {
	return &value
}

func TestClientEndpointsFromStatefulsets(t *testing.T) {
	tests := []struct {
		name           string
		statefulSet    *appsv1.StatefulSet
		expectedResult []string
	}{
		{
			name: "StatefulSet with 3 replicas",
			statefulSet: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: pointerToInt32(3),
				},
			},
			expectedResult: []string{
				"http://test-sts-0.test-sts.default.svc.cluster.local:2379",
				"http://test-sts-1.test-sts.default.svc.cluster.local:2379",
				"http://test-sts-2.test-sts.default.svc.cluster.local:2379",
			},
		},
		{
			name: "StatefulSet with 1 replica",
			statefulSet: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: pointerToInt32(1),
				},
			},
			expectedResult: []string{
				"http://test-sts-0.test-sts.default.svc.cluster.local:2379",
			},
		},
		{
			name: "StatefulSet with 0 replicas",
			statefulSet: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: pointerToInt32(0),
				},
			},
			expectedResult: []string(nil),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := clientEndpointsFromStatefulsets(tt.statefulSet)
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestAreAllMembersHealthy(t *testing.T) {
	tests := []struct {
		name           string
		statefulSet    *appsv1.StatefulSet
		healthInfos    []etcdutils.EpHealth
		expectedResult bool
		expectedError  error
	}{
		// TODO: Add test cases for healthy members and non healthy members
		{
			name: "Error during health check",
			statefulSet: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-sts",
					Namespace: "default",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: pointerToInt32(3),
				},
				Status: appsv1.StatefulSetStatus{
					ReadyReplicas: 3,
				},
			},
			healthInfos:    nil,
			expectedResult: false,
			expectedError:  errors.New("context deadline exceeded"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := logr.Discard() // Use a no-op logger for testing

			result, err := areAllMembersHealthy(tt.statefulSet, logger)
			assert.Equal(t, tt.expectedResult, result)
			if tt.expectedError != nil {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError.Error())
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
