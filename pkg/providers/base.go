/*
Copyright 2018 The Shared LoadBalancer Authors.

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

package providers

import (
	kubeconv1alpha1 "github.com/Huang-Wei/shared-loadbalancer/pkg/apis/kubecon/v1alpha1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
)

var log logr.Logger

var (
	// svcPostfix is the stringa appended to cluster service object
	svcPostfix = "-service"
	// namespace that LoadBalancer service will be created in
	// most probably it's the same value of the namespace that this binary runs in
	namespace = GetEnvVal("NAMESPACE", "default")
	// capacity is the threshold value a LoadBalancer service can hold
	capacity = GetEnvValInt("CAPACITY", 2)
	// FinalizerName is the name of finalizer attached to Cluster Service object
	FinalizerName = "sharedlb.kubecon.k8s.io/finalizer"
)

type nameSet map[types.NamespacedName]struct{}

func init() {
	log = logf.Log.WithName("providers")
}

func NewProvider() LBProvider {
	providerStr := GetEnvVal("PROVIDER", "local")
	log.Info("New LBProvider", "provider", providerStr)
	var provider LBProvider
	switch providerStr {
	case "iks":
		provider = newIKSProvider()
	case "eks":
		provider = newEKSProvider()
	case "local":
		provider = newLocalProvider()
	}
	return provider
}

// LBProvider defines methods that a loadbalancer provider should implement
type LBProvider interface {
	NewService(sharedLB *kubeconv1alpha1.SharedLB) *corev1.Service
	NewLBService() *corev1.Service
	GetAvailabelLB() *corev1.Service
	AssociateLB(cr, lb types.NamespacedName, clusterSvc *corev1.Service) error
	DeassociateLB(cr types.NamespacedName) error
	UpdateCache(key types.NamespacedName, val *corev1.Service)

	GetCapacityPerLB() int

	// TODO(Huang-Wei): can be removed and implement in utils.go
	// and rename to "UpdateServiceExternalIP"
	UpdateService(svc, lb *corev1.Service) (portUpdated, externalIPUpdated bool)
}

// TODO(Huang-Wei): ensure port is not duplicated
func updatePort(svc, lb *corev1.Service) bool {
	updated := false
	// check if svc doesn't carry port info
	for i, svcPort := range svc.Spec.Ports {
		if svcPort.Port != 0 {
			continue
		}
		svc.Spec.Ports[i].Port = GetRandomPort()
		updated = true
	}
	return updated
}
