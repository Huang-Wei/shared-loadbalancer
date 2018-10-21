package providers

import (
	kubeconv1alpha1 "github.com/Huang-Wei/shared-loadbalancer/pkg/apis/kubecon/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

// LBProvider defines methods that a loadbalancer provider should implement
type LBProvider interface {
	BuildService(sharedLB *kubeconv1alpha1.SharedLB) *corev1.Service
	CreateLB(sharedLB *kubeconv1alpha1.SharedLB) (*corev1.Service, error)
	AssociateLB(sharedLB *kubeconv1alpha1.SharedLB, lb *corev1.Service) error
	DeassociateLB(sharedLB *kubeconv1alpha1.SharedLB, lb *corev1.Service) error
}
