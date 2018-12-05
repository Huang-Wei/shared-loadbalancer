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
	"context"
	"errors"
	"fmt"
	"strings"

	kubeconv1alpha1 "github.com/Huang-Wei/shared-loadbalancer/pkg/apis/kubecon/v1alpha1"
	"golang.org/x/oauth2/google"
	compute "google.golang.org/api/compute/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const PORTRANGE = "30000-32767"

// GKE stands for Google Kubernetes Service
type GKE struct {
	client          *compute.Service
	project, region string
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

var _ LBProvider = &GKE{}
var ctx = context.Background()

func newGKEProvider() *GKE {
	gke := &GKE{
		cacheMap:      make(map[types.NamespacedName]*corev1.Service),
		crToLB:        make(map[types.NamespacedName]types.NamespacedName),
		lbToCRs:       make(map[types.NamespacedName]nameSet),
		lbToPorts:     make(map[types.NamespacedName]int32Set),
		capacityPerLB: capacity,
	}
	gke.client = initGCE()
	gke.project = GetEnvVal("GKE_PROJ", "kubecon-seattle-project")
	gke.region = GetEnvVal("GKE_REGION", "us-central1")
	return gke
}

func initGCE() *compute.Service {
	c, err := google.DefaultClient(ctx, compute.CloudPlatformScope)
	if err != nil {
		log.WithName("gke").Error(err, "Failed to get GCE client")
	}

	computeService, err := compute.New(c)
	if err != nil {
		log.WithName("gke").Error(err, "Failed to create compute service")
	}

	return computeService
}

func (i *GKE) GetCapacityPerLB() int {
	return i.capacityPerLB
}

func (i *GKE) UpdateCache(key types.NamespacedName, lbSvc *corev1.Service) {
	if lbSvc == nil {
		delete(i.cacheMap, key)
	} else {
		if len(lbSvc.Status.LoadBalancer.Ingress) > 0 {
			// Make this IP Static and then add a Forwarding rule
			i.createStaticIP(lbSvc.ObjectMeta.Name+"-ip", lbSvc.Status.LoadBalancer.Ingress[0].IP)
			// Let us also create a Forwarding rule to open up all ports.
			// We can also do each port the slb needs but for now this is good enough.
			fwRuleName := lbSvc.ObjectMeta.Name + "-rule"
			lbPool := i.getTargetPool_usingLBName(lbSvc.ObjectMeta.Name)
			if lbPool == nil {
				log.WithName("GKE").Info("Cannot find the TargetPool for the LoadBalancer")
			}
			i.forwardingRuleInsert(fwRuleName, lbSvc.Status.LoadBalancer.Ingress[0].IP, lbPool.SelfLink)
		}
		i.cacheMap[key] = lbSvc
	}
}

func (i *GKE) NewService(sharedLB *kubeconv1alpha1.SharedLB) *corev1.Service {
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

func (i *GKE) NewLBService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lb-" + RandStringRunes(8),
			Namespace: namespace,
			Labels:    map[string]string{"lb-template": ""},
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name:     "tcp",
					Protocol: corev1.ProtocolTCP,
					Port:     33333,
				},
				// TODO(Huang-Wei): handle UDP case
				// {
				// 	Name:     "UDP",
				// 	Protocol: corev1.ProtocolUDP,
				// 	Port:     33333,
				// },
			},
			//			ExternalIPs: []string {
			//				ipaddress,
			//			},
			Type: corev1.ServiceTypeLoadBalancer,
		},
	}
}

func (i *GKE) GetAvailabelLB(clusterSvc *corev1.Service) *corev1.Service {
	// we leverage the randomness of golang "for range" when iterating
OUTERLOOP:
	for lbKey, lbSvc := range i.cacheMap {
		if len(i.lbToCRs[lbKey]) >= i.capacityPerLB || len(lbSvc.Status.LoadBalancer.Ingress) == 0 {
			continue
		}
		// must satisfy that all svc ports are not occupied in lbSvc
		for _, svcPort := range clusterSvc.Spec.Ports {
			if i.lbToPorts[lbKey] == nil {
				i.lbToPorts[lbKey] = int32Set{}
			}
			if _, ok := i.lbToPorts[lbKey][svcPort.Port]; ok {
				log.WithName("GKE").Info(fmt.Sprintf("incoming service has port conflict with lbSvc %q on port %d", lbKey, svcPort.Port))
				continue OUTERLOOP
			}
		}
		return lbSvc
	}
	return nil
}

func (i *GKE) AssociateLB(crName, lbName types.NamespacedName, clusterSvc *corev1.Service) error {
	// Let us also create a Firewall Rule for this port.
	firewallName := fmt.Sprintf("%s-fwrule-%d", lbName.Name, clusterSvc.Spec.Ports[0].Port)
	if i.getFirewallRule(firewallName) == nil {
		i.firewallInsert(firewallName, fmt.Sprint(clusterSvc.Spec.Ports[0].Port))
	}
	if clusterSvc != nil {
		if lbSvc, ok := i.cacheMap[lbName]; !ok || len(lbSvc.Status.LoadBalancer.Ingress) == 0 {
			return errors.New("LoadBalancer service not exist yet")
		}
		// upon program starts, i.lbToPorts[lbName] can be nil
		if i.lbToPorts[lbName] == nil {
			i.lbToPorts[lbName] = int32Set{}
		}
		// update crToPorts
		for _, svcPort := range clusterSvc.Spec.Ports {
			i.lbToPorts[lbName][svcPort.Port] = struct{}{}
		}
	}

	// following code might be called multiple times, but shouldn't impact
	// performance a lot as all of them are O(1) operation
	_, ok := i.lbToCRs[lbName]
	if !ok {
		i.lbToCRs[lbName] = make(nameSet)
	}
	i.lbToCRs[lbName][crName] = struct{}{}
	i.crToLB[crName] = lbName
	log.WithName("GKE").Info("AssociateLB", "cr", crName, "lb", lbName)
	return nil
}

// DeassociateLB is called by GKE finalizer to clean internal cache
// no IaaS things should be done for GKE
func (i *GKE) DeassociateLB(crName types.NamespacedName, clusterSvc *corev1.Service) error {
	var lbName string
	// update internal cache
	if lb, ok := i.crToLB[crName]; ok {
		lbName = lb.Name
		delete(i.crToLB, crName)
		delete(i.lbToCRs[lb], crName)
		for _, svcPort := range clusterSvc.Spec.Ports {
			delete(i.lbToPorts[lb], svcPort.Port)
		}
		log.WithName("GKE").Info("DeassociateLB", "cr", crName, "lb", lb)
	}
    firewallName := fmt.Sprintf("%s-fwrule-%d", lbName, clusterSvc.Spec.Ports[0].Port)
	i.firewallDelete(firewallName)
	return nil
}

func (i *GKE) UpdateService(svc, lb *corev1.Service) (bool, bool) {
	lbName := types.NamespacedName{Name: lb.Name, Namespace: lb.Namespace}
	occupiedPorts := i.lbToPorts[lbName]
	if len(occupiedPorts) == 0 {
		occupiedPorts = int32Set{}
	}
	portUpdated := updatePort(svc, lb, occupiedPorts)
	return portUpdated, true
}

func (g *GKE) createStaticIP(name, ip string) {
	addr := &compute.Address{
		Name:    name,
		Address: ip,
		Region:  g.region,
	}
	addrInsertCall := g.client.Addresses.Insert(g.project, g.region, addr)
	_, err := addrInsertCall.Do()

	if err != nil {
		log.WithName("gke").Error(err, "Faied to secure a Static IP")
	}
}

func (g *GKE) getTargetPool_usingLBName(lb string) *compute.TargetPool {
	resp, err := g.client.TargetPools.List(g.project, g.region).Context(ctx).Do()
	if err != nil {
		log.WithName("gke").Error(err, "Failed to find the loadbalancer "+lb)
	}
	for _, item := range resp.Items {
		if strings.Contains(item.Description, lb) {
			return item
		}
	}
	return nil
}

func (g *GKE) getForwardingRule(name string) *compute.ForwardingRule {
	resp, err := g.client.ForwardingRules.List(g.project, g.region).Context(ctx).Do()
	if err != nil {
		if strings.Contains(err.Error(), "Error 404") {
			return nil
		}
		log.WithName("GKE").Error(err, "Failed to get Forwarding rules")
	}
	for _, item := range resp.Items {
		if strings.Compare(item.Name, name) == 0 {
			return item
		}
	}
	return nil
}

func (g *GKE) forwardingRuleInsert(name, ip, item string) {
	fw := &compute.ForwardingRule{
		IPAddress:           ip,
		IPProtocol:          "TCP",
		Description:         "KubeConDemo, forwarding rule to reach service through port ",
		LoadBalancingScheme: "EXTERNAL",
		Target:              item,
		Name:                name,
		PortRange:           PORTRANGE,
	}
	fwInsertCall := g.client.ForwardingRules.Insert(g.project, g.region, fw)
	_, err := fwInsertCall.Do()

	if err != nil {
		log.WithName("gke").Error(err, "Failed to create the Forwarding Rule for the item "+item)
	}
}

func (g *GKE) forwardingRuleDelete(name string) {
	fwDeleteCall := g.client.ForwardingRules.Delete(g.project, g.region, name)
	_, err := fwDeleteCall.Do()

	if err != nil {
		log.WithName("gke").Error(err, "Failed to delete the firewall rule "+name)
	}
}

func (g *GKE) getFirewallRule(name string) *compute.Firewall {
	resp, err := g.client.Firewalls.Get(g.project, name).Context(ctx).Do()
	if err != nil {
		if strings.Contains(err.Error(), "Error 404") {
			return nil
		}
		log.WithName("GKE").Error(err, "Failed to get Firewalls rules")
	}
	return resp
}

func (g *GKE) firewallInsert(name, port string) error {
	fw := &compute.Firewall{
		Name: name,
		Allowed: []*compute.FirewallAllowed{
			{
				IPProtocol: "TCP",
				Ports:      []string{port},
			},
		},
	}
	fwInsertCall := g.client.Firewalls.Insert(g.project, fw)
	_, err := fwInsertCall.Do()

	if err != nil {
		log.WithName("gke").Error(err, "Unable to add a firewall rule")
	}
	return err
}

func (g *GKE) firewallDelete(name string) error {
	fwDeleteCall := g.client.Firewalls.Delete(g.project, name)
	_, err := fwDeleteCall.Do()

	if err != nil {
		log.WithName("gke").Error(err, "Failed to delete the firewall rule "+name)
	}
	return err
}
