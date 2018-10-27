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
	"errors"

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

	// cr to LB is 1:1 mapping
	crToLB map[types.NamespacedName]types.NamespacedName
	// lb to CRD is 1:N mapping
	lbToCRs map[types.NamespacedName]nameSet

	capacityPerLB int
	credentials   string
}

var _ LBProvider = &Local{}

func newLocalProvider() *Local {
	return &Local{
		cacheMap:      make(map[types.NamespacedName]*corev1.Service),
		crToLB:        make(map[types.NamespacedName]types.NamespacedName),
		lbToCRs:       make(map[types.NamespacedName]nameSet),
		capacityPerLB: capacity,
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
			Name:      sharedLB.Name + SvcPostfix,
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
			Namespace: namespace,
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
		if len(l.lbToCRs[lbKey]) < l.capacityPerLB {
			return lbSvc
		}
	}

	return nil
}

func (l *Local) AssociateLB(crName, lbName types.NamespacedName, _ *corev1.Service) error {
	if _, ok := l.cacheMap[lbName]; !ok {
		return errors.New("LoadBalancer service not exist yet")
	}

	// following code might be called multiple times, but shouldn't impact
	// performance a lot as all of them are O(1) operation
	_, ok := l.lbToCRs[lbName]
	if !ok {
		l.lbToCRs[lbName] = make(nameSet)
	}
	l.lbToCRs[lbName][crName] = struct{}{}
	l.crToLB[crName] = lbName
	log.WithName("local").Info("AssociateLB", "cr", crName, "lb", lbName)
	return nil
}

func (l *Local) DeassociateLB(crd types.NamespacedName, _ *corev1.Service) error {
	// update cache
	if lb, ok := l.crToLB[crd]; ok {
		delete(l.crToLB, crd)
		delete(l.lbToCRs[lb], crd)
		log.WithName("local").Info("DeassociateLB", "crd", crd, "lb", lb)
	}
	return nil
}

func (l *Local) UpdateService(svc, lb *corev1.Service) (bool, bool) {
	// nothing to do with local provider here
	return false, false
}
