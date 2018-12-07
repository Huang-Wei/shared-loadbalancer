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
	"google.golang.org/api/googleapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Ref:
// - gcloud auth application-default login
// - gcloud auth application-default print-access-token

const PORTRANGE = "30000-32767"

// GKE stands for Google Kubernetes Engine
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
		log.WithName("gke").Info("Failed to get GCE client")
		panic(err)
	}

	computeService, err := compute.New(c)
	if err != nil {
		log.WithName("gke").Info("Failed to create compute service")
		panic(err)
	}

	return computeService
}

func (g *GKE) GetCapacityPerLB() int {
	return g.capacityPerLB
}

func (g *GKE) UpdateCache(key types.NamespacedName, lbSvc *corev1.Service) {
	if lbSvc == nil {
		delete(g.cacheMap, key)
		return
	}
	if len(lbSvc.Status.LoadBalancer.Ingress) == 0 {
		return
	}

	// Make this IP Static and then add an Inbound Firewall Rule
	g.ensureStaticIP(lbSvc.ObjectMeta.Name+"-ip", lbSvc.Status.LoadBalancer.Ingress[0].IP)
	// Let us also create an Inbound Firewall Rule to open up all ports.
	// We can also do each port the slb needs but for now this is good enough.
	g.ensureFirewall(getLBFirewallRuleName(lbSvc.ObjectMeta.Name))
	g.cacheMap[key] = lbSvc
}

func getLBFirewallRuleName(lbName string) string {
	return fmt.Sprintf("k8s-fw-%s-autogen", lbName)
}

func getLBForwardRuleName(lbName string, port int32, proto corev1.Protocol) string {
	// note: proto must be lowered
	return fmt.Sprintf("%s-fwd-rule-%d-%v-autogen", lbName, port, strings.ToLower(string(proto)))
}

func (g *GKE) NewService(sharedLB *kubeconv1alpha1.SharedLB) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      sharedLB.Name + SvcPostfix,
			Namespace: sharedLB.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Ports:    sharedLB.Spec.Ports,
			Selector: sharedLB.Spec.Selector,
		},
	}
}

func (g *GKE) NewLBService() *corev1.Service {
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
			Type: corev1.ServiceTypeLoadBalancer,
		},
	}
}

func (g *GKE) GetAvailabelLB(clusterSvc *corev1.Service) *corev1.Service {
	// we leverage the randomness of golang "for range" when iterating
OUTERLOOP:
	for lbKey, lbSvc := range g.cacheMap {
		if len(g.lbToCRs[lbKey]) >= g.capacityPerLB || len(lbSvc.Status.LoadBalancer.Ingress) == 0 {
			continue
		}
		// must satisfy that all svc ports are not occupied in lbSvc
		for _, svcPort := range clusterSvc.Spec.Ports {
			if g.lbToPorts[lbKey] == nil {
				g.lbToPorts[lbKey] = int32Set{}
			}
			if _, ok := g.lbToPorts[lbKey][svcPort.Port]; ok {
				log.WithName("gke").Info(fmt.Sprintf("incoming service has port conflict with lbSvc %q on port %d", lbKey, svcPort.Port))
				continue OUTERLOOP
			}
		}
		return lbSvc
	}
	return nil
}

func (g *GKE) ensureForwardingRule(lbName, ip string, nodePort int32, proto corev1.Protocol) error {
	fwdRuleName := getLBForwardRuleName(lbName, nodePort, proto)
	if g.getForwardingRule(fwdRuleName) == nil {
		lbPool := g.getTargetPool_usingLBName(lbName)
		if lbPool == nil {
			return errors.New("cannot find the TargetPool for the LoadBalancer")
		}
		return g.insertForwardingRule(fwdRuleName, ip, nodePort, proto, lbPool.SelfLink)
	}
	return nil
}

func (g *GKE) AssociateLB(crName, lbName types.NamespacedName, clusterSvc *corev1.Service) error {
	// a) create GCP LoadBalancer Forwarding Rule
	if clusterSvc != nil {
		if lbSvc := g.cacheMap[lbName]; lbSvc != nil {
			for _, svcPort := range clusterSvc.Spec.Ports {
				if err := g.ensureForwardingRule(lbName.Name, lbSvc.Status.LoadBalancer.Ingress[0].IP, svcPort.NodePort, svcPort.Protocol); err != nil {
					return err
				}
			}
		}
		// upon program starts, g.lbToPorts[lbName] can be nil
		if g.lbToPorts[lbName] == nil {
			g.lbToPorts[lbName] = int32Set{}
		}
		// update crToPorts
		for _, svcPort := range clusterSvc.Spec.Ports {
			g.lbToPorts[lbName][svcPort.NodePort] = struct{}{}
		}
	}

	// c) update internal cache
	// following code might be called multiple times, but shouldn't impact
	// performance a lot as all of them are O(1) operation
	_, ok := g.lbToCRs[lbName]
	if !ok {
		g.lbToCRs[lbName] = make(nameSet)
	}
	g.lbToCRs[lbName][crName] = struct{}{}
	g.crToLB[crName] = lbName
	log.WithName("gke").Info("AssociateLB", "cr", crName, "lb", lbName)
	return nil
}

// DeassociateLB is called by GKE finalizer to clean internal cache
// no IaaS things should be done for GKE
func (g *GKE) DeassociateLB(crName types.NamespacedName, clusterSvc *corev1.Service) error {
	lbName, ok := g.crToLB[crName]
	if !ok {
		return nil
	}

	// a) delete GCP LoadBalancer Forwarding rule
	if lbSvc := g.cacheMap[lbName]; lbSvc != nil {
		for _, svcPort := range clusterSvc.Spec.Ports {
			fwdRuleName := getLBForwardRuleName(lbName.Name, svcPort.NodePort, svcPort.Protocol)
			if err := g.deleteForwardingRule(fwdRuleName); err != nil {
				return err
			}
		}
	}

	// b) update internal cache
	delete(g.crToLB, crName)
	delete(g.lbToCRs[lbName], crName)
	for _, svcPort := range clusterSvc.Spec.Ports {
		delete(g.lbToPorts[lbName], svcPort.Port)
	}
	log.WithName("gke").Info("DeassociateLB", "cr", crName, "lb", lbName)
	return nil
}

func (g *GKE) UpdateService(svc, lb *corev1.Service) (bool, bool) {
	lbName := types.NamespacedName{Name: lb.Name, Namespace: lb.Namespace}
	occupiedPorts := g.lbToPorts[lbName]
	if len(occupiedPorts) == 0 {
		occupiedPorts = int32Set{}
	}
	portUpdated := updatePort(svc, lb, occupiedPorts)
	return portUpdated, true
}

func isAlreadyExist(err error) bool {
	apiErr, ok := err.(*googleapi.Error)
	return ok && (apiErr.Code == 409 || strings.Contains(apiErr.Message, "alreadyExists"))
}

func (g *GKE) ensureStaticIP(name, ip string) {
	addr := &compute.Address{
		Name:    name,
		Address: ip,
		Region:  g.region,
	}

	addrInsertCall := g.client.Addresses.Insert(g.project, g.region, addr)
	_, err := addrInsertCall.Do()

	if err != nil && !isAlreadyExist(err) {
		log.WithName("gke").Error(err, "Faied to secure a Static IP")
	}
}

func (g *GKE) getTargetPool_usingLBName(lb string) *compute.TargetPool {
	resp, err := g.client.TargetPools.List(g.project, g.region).Context(ctx).Do()
	if err != nil {
		log.WithName("gke").Error(err, "Failed to find the loadbalancer "+lb)
	}
	for _, item := range resp.Items {
		// TODO(Huang-Wei): check "name" for exact match
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
		log.WithName("gke").Error(err, "Failed to get Forwarding rules")
	}
	for _, item := range resp.Items {
		if strings.Compare(item.Name, name) == 0 {
			return item
		}
	}
	return nil
}

func (g *GKE) insertForwardingRule(name, ip string, port int32, proto corev1.Protocol, item string) error {
	fwd := &compute.ForwardingRule{
		IPAddress:           ip,
		IPProtocol:          strings.ToUpper(string(proto)),
		Description:         "forwarding rule to reach service through nodePort, generated by SharedLB",
		LoadBalancingScheme: "EXTERNAL",
		Target:              item,
		Name:                name,
		PortRange:           fmt.Sprintf("%d-%d", port, port),
	}
	fwdInsertCall := g.client.ForwardingRules.Insert(g.project, g.region, fwd)
	_, err := fwdInsertCall.Do()

	if err != nil {
		return fmt.Errorf("failed to create the forwarding rule for the item %v: %v", item, err)
	}
	return nil
}

func (g *GKE) deleteForwardingRule(name string) error {
	fwdDeleteCall := g.client.ForwardingRules.Delete(g.project, g.region, name)
	_, err := fwdDeleteCall.Do()

	if err != nil {
		return fmt.Errorf("failed to delete the forwarding rule %q", name)
	}
	return nil
}

func (g *GKE) ensureFirewall(name string) error {
	fw := &compute.Firewall{
		Name: name,
		Allowed: []*compute.FirewallAllowed{
			{
				IPProtocol: "TCP",
				Ports:      []string{PORTRANGE},
			},
			{
				IPProtocol: "UDP",
				Ports:      []string{PORTRANGE},
			},
		},
	}
	fwInsertCall := g.client.Firewalls.Insert(g.project, fw)
	_, err := fwInsertCall.Do()

	if err != nil && !isAlreadyExist(err) {
		log.WithName("gke").Error(err, "Unable to add a firewall rule")
	}
	return err
}

// not used yet
func (g *GKE) getFirewallRule(name string) *compute.Firewall {
	resp, err := g.client.Firewalls.Get(g.project, name).Context(ctx).Do()
	if err != nil {
		if strings.Contains(err.Error(), "Error 404") {
			return nil
		}
		log.WithName("gke").Error(err, "Failed to get Firewalls rules")
	}
	return resp
}

// not used yet
func (g *GKE) firewallDelete(name string) error {
	fwDeleteCall := g.client.Firewalls.Delete(g.project, name)
	_, err := fwDeleteCall.Do()

	if err != nil {
		log.WithName("gke").Error(err, "Failed to delete the firewall rule "+name)
	}
	return err
}
