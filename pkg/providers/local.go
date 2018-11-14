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
	"fmt"

	kubeconv1alpha1 "github.com/Huang-Wei/shared-loadbalancer/pkg/apis/kubecon/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type Local struct {
	// key is namespacedName of a LB Serivce, val is the service
	cacheMap map[types.NamespacedName]*corev1.Service

	// cr to LB is 1:1 mapping
	crToLB map[types.NamespacedName]types.NamespacedName
	// lb to CRD is 1:N mapping
	lbToCRs map[types.NamespacedName]nameSet
	// lbToPorts is keyed with ns/name of a LB, and valued with ports info it holds
	lbToPorts map[types.NamespacedName]int32Set

	capacityPerLB int
}

var _ LBProvider = &Local{}

func newLocalProvider() *Local {
	return &Local{
		cacheMap:      make(map[types.NamespacedName]*corev1.Service),
		crToLB:        make(map[types.NamespacedName]types.NamespacedName),
		lbToCRs:       make(map[types.NamespacedName]nameSet),
		lbToPorts:     make(map[types.NamespacedName]int32Set),
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

func (l *Local) GetAvailabelLB(clusterSvc *corev1.Service) *corev1.Service {
	// we leverage the randomness of golang "for range" when iterating
OUTERLOOP:
	for lbKey, lbSvc := range l.cacheMap {
		if len(l.lbToCRs[lbKey]) >= l.capacityPerLB {
			continue
		}
		// must satisfy that all svc ports are not occupied in lbSvc
		for _, svcPort := range clusterSvc.Spec.Ports {
			if l.lbToPorts[lbKey] == nil {
				l.lbToPorts[lbKey] = int32Set{}
			}
			if _, ok := l.lbToPorts[lbKey][svcPort.Port]; ok {
				log.WithName("local").Info(fmt.Sprintf("incoming service has port conflict with lbSvc %q on port %d", lbKey, svcPort.Port))
				continue OUTERLOOP
			}
		}
		return lbSvc
	}
	return nil
}

func (l *Local) AssociateLB(crName, lbName types.NamespacedName, clusterSvc *corev1.Service) error {
	if clusterSvc != nil {
		// for local provider, it's more for testing purpose, so not required to
		// have real loadbalancer ip present in lbSvc
		if _, ok := l.cacheMap[lbName]; !ok {
			return errors.New("LoadBalancer service not exist yet")
		}
		// update crToPorts
		if l.lbToPorts[lbName] == nil {
			log.WithName("local").Info("[DEBUG] lbToPorts can be nil")
			l.lbToPorts[lbName] = int32Set{}
		}
		for _, svcPort := range clusterSvc.Spec.Ports {
			l.lbToPorts[lbName][svcPort.Port] = struct{}{}
		}
		// log.WithName("local").Info("[DEBUG] lbToPorts", "lbToPorts", fmt.Sprintf("%v", l.lbToPorts))
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
