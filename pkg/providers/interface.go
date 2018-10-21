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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// LBProvider defines methods that a loadbalancer provider should implement
type LBProvider interface {
	NewService(sharedLB *kubeconv1alpha1.SharedLB) *corev1.Service
	NewLBService() *corev1.Service
	GetAvailabelLB() *corev1.Service
	AssociateLB(crd, lb types.NamespacedName) error
	DeassociateLB(crd, lb types.NamespacedName) error
	UpdateCache(key types.NamespacedName, val *corev1.Service)
}
