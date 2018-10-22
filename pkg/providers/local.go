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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type Local struct {
	// key is namespacedName of a LB Serivce, val is the service
	cacheMap map[types.NamespacedName]*corev1.Service
	// TODO(Huang-Wei): keyName => IaaS stuff
	// cacheIaaSMap

	// crd to LB is 1:1 mapping
	// crdToLB map[types.NamespacedName]types.NamespacedName
	crdToLB map[types.NamespacedName]types.NamespacedName
	// lb to CRD is 1:N mapping
	// lbToCRD     map[types.NamespacedName]nameSet
	lbToCRD map[types.NamespacedName]nameSet

	capacityPerLB int
	credentials   string
}

var _ LBProvider = &Local{}

func newLocalProvider() *Local {
	return &Local{
		cacheMap:      make(map[types.NamespacedName]*corev1.Service),
		crdToLB:       make(map[types.NamespacedName]types.NamespacedName),
		lbToCRD:       make(map[types.NamespacedName]nameSet),
		capacityPerLB: 3,
	}
}

func (l *Local) GetCapacityPerLB() int {
	return l.capacityPerLB
}

func (l *Local) UpdateCache(key types.NamespacedName, val *corev1.Service) {
	l.cacheMap[key] = val
}

func (l *Local) NewService(sharedLB *kubeconv1alpha1.SharedLB) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sharedLB.Name + "-service",
			Namespace: sharedLB.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports:    sharedLB.Spec.Ports,
			Selector: sharedLB.Spec.Selector,
		},
	}
}

func (l *Local) NewLBService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lb-" + RandStringRunes(8),
			Namespace: "default",
			Labels:    map[string]string{"lb-template": ""},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: 33333,
				},
			},
			Type: corev1.ServiceTypeLoadBalancer,
		},
	}
}

func (l *Local) GetAvailabelLB() *corev1.Service {
	for lbKey, lbSvc := range l.cacheMap {
		if len(l.lbToCRD[lbKey]) < l.capacityPerLB {
			return lbSvc
		}
	}

	return nil
}

func (l *Local) AssociateLB(crd, lb types.NamespacedName) error {
	log.WithName("local").Info("AssociateLB", "crd", crd, "lb", lb)
	// if lb exists
	if crds, ok := l.lbToCRD[lb]; ok {
		crds[crd] = struct{}{}
		l.crdToLB[crd] = lb
	} else {
		l.lbToCRD[lb] = make(nameSet)
		l.lbToCRD[lb][crd] = struct{}{}
		l.crdToLB[crd] = lb
	}
	// TODO(Huang-Wei): maybe change crd to service
	// and also do the real association logic
	return nil
}

func (l *Local) DeassociateLB(crd types.NamespacedName) error {
	// update cache
	if lb, ok := l.crdToLB[crd]; ok {
		delete(l.crdToLB, crd)
		delete(l.lbToCRD[lb], crd)
		log.WithName("local").Info("DeassociateLB", "crd", crd, "lb", lb)
	}
	return nil
}
