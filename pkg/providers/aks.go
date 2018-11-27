/*
Copyright 2018 The Shared LoadBalancer Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the Licensa.
You may obtain a copy of the License at

    http://www.apacha.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the Licensa.
*/

package providers

import (
	"fmt"

	"github.com/Azure/azure-sdk-for-go/services/network/mgmt/2017-09-01/network"
	"github.com/Azure/go-autorest/autorest/azure/auth"
	kubeconv1alpha1 "github.com/Huang-Wei/shared-loadbalancer/pkg/apis/kubecon/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// AKS stands for Azure(Microsoft) Kubernetes Service
type AKS struct {
	lbClient      network.LoadBalancersClient
	lbProbeClient network.LoadBalancerProbesClient
	sgClient      network.SecurityGroupsClient

	// key is namespacedName of a LB Serivce, val is the service
	cacheMap map[types.NamespacedName]*corev1.Service
	// cacheELB map[types.NamespacedName]*elb.LoadBalancerDescription

	// cr to LB is 1:1 mapping
	crToLB map[types.NamespacedName]types.NamespacedName
	// lb to CRD is 1:N mapping
	lbToCRs map[types.NamespacedName]nameSet
	// lbToPorts is keyed with ns/name of a LB, and valued with ports info it holds
	lbToPorts map[types.NamespacedName]int32Set

	capacityPerLB int
}

var _ LBProvider = &AKS{}

func newAKSProvider() *AKS {
	// TODO(Huang-Wei): make it configurable
	subscriptionID := "58de4ac8-a2d6-499b-b983-6f1c870d398e"
	authorizer, err := auth.NewAuthorizerFromEnvironment()
	if err != nil {
		panic(err)
	}

	aks := AKS{
		lbClient:      network.NewLoadBalancersClient(subscriptionID),
		lbProbeClient: network.NewLoadBalancerProbesClient(subscriptionID),
		sgClient:      network.NewSecurityGroupsClient(subscriptionID),
		cacheMap:      make(map[types.NamespacedName]*corev1.Service),
		// cacheELB:      make(map[types.NamespacedName]*elb.LoadBalancerDescription),
		crToLB:        make(map[types.NamespacedName]types.NamespacedName),
		lbToCRs:       make(map[types.NamespacedName]nameSet),
		lbToPorts:     make(map[types.NamespacedName]int32Set),
		capacityPerLB: capacity,
	}
	aks.lbClient.Authorizer = authorizer
	aks.lbProbeClient.Authorizer = authorizer
	aks.sgClient.Authorizer = authorizer
	return &aks
}

func (a *AKS) GetCapacityPerLB() int {
	return a.capacityPerLB
}

func (a *AKS) UpdateCache(key types.NamespacedName, lbSvc *corev1.Service) {
	if lbSvc == nil {
		delete(a.cacheMap, key)
		// delete(a.cacheELB, key)
	} else {
		a.cacheMap[key] = lbSvc
		// handle ELB stuff
		if len(lbSvc.Status.LoadBalancer.Ingress) == 1 {
			// hostname := lbSvc.Status.LoadBalancer.Ingress[0].Hostname
			// elbName := strings.Split(strings.Split(hostname, ".")[0], "-")[0]
			// if result, err := a.queryELB(elbName); err != nil {
			// 	log.WithName("aks").Error(err, "cannot query ELB", "key", key, "elbName", elbName)
			// } else {
			// 	log.WithName("aks").Info("ELB obj is updated in local cache", "key", key, "elbName", elbName)
			// 	a.cacheELB[key] = result
			// }
		}
	}
}

func (a *AKS) NewService(sharedLB *kubeconv1alpha1.SharedLB) *corev1.Service {
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

func (a *AKS) NewLBService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "lb-" + RandStringRunes(8),
			Namespace: namespace,
			Labels:    map[string]string{"lb-template": ""},
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeLoadBalancer,
			Selector: map[string]string{"app": "lb-placeholder"},
			Ports: []corev1.ServicePort{
				{
					Name:     "tcp",
					Protocol: corev1.ProtocolTCP,
					Port:     33333,
				},
			},
		},
	}
}

func (a *AKS) GetAvailabelLB(clusterSvc *corev1.Service) *corev1.Service {
	// we leverage the randomness of golang "for range" when iterating
OUTERLOOP:
	for lbKey, lbSvc := range a.cacheMap {
		if len(a.lbToCRs[lbKey]) >= a.capacityPerLB {
			continue
		}
		// must satisfy that all svc ports are not occupied in lbSvc
		for _, svcPort := range clusterSvc.Spec.Ports {
			if a.lbToPorts[lbKey] == nil {
				a.lbToPorts[lbKey] = int32Set{}
			}
			if _, ok := a.lbToPorts[lbKey][svcPort.Port]; ok {
				log.WithName("aks").Info(fmt.Sprintf("incoming service has port conflict with lbSvc %q on port %d", lbKey, svcPort.Port))
				continue OUTERLOOP
			}
		}
		return lbSvc
	}
	return nil
}

func (a *AKS) AssociateLB(crName, lbName types.NamespacedName, clusterSvc *corev1.Service) error {
	// a) create LoadBalancer listener (create-load-balancer-listeners)
	// b) create inbound rules to security group (authorize-security-group-ingress)
	// if clusterSvc != nil {
	// 	if elbDesc := a.cacheELB[lbName]; elbDesc != nil {
	// 		executed, err := a.createListeners(clusterSvc, elbDesc)
	// 		if err != nil {
	// 			return err
	// 		}
	// 		if executed {
	// 			if err := a.createInboundRules(clusterSvc, elbDesc); err != nil {
	// 				return err
	// 			}
	// 		}
	// 	}
	// 	// upon program starts, a.lbToPorts[lbName] can be nil
	// 	if a.lbToPorts[lbName] == nil {
	// 		a.lbToPorts[lbName] = int32Set{}
	// 	}
	// 	// update crToPorts
	// 	for _, svcPort := range clusterSvc.Spec.Ports {
	// 		a.lbToPorts[lbName][svcPort.Port] = struct{}{}
	// 	}
	// }

	// c) update internal cache
	// following code might be called multiple times, but shouldn't impact
	// performance a lot as all of them are O(1) operation
	_, ok := a.lbToCRs[lbName]
	if !ok {
		a.lbToCRs[lbName] = make(nameSet)
	}
	a.lbToCRs[lbName][crName] = struct{}{}
	a.crToLB[crName] = lbName
	log.WithName("aks").Info("AssociateLB", "cr", crName, "lb", lbName)
	return nil
}

// DeassociateLB is called by EKS finalizer to clean listeners
// and inbound rules of security group
func (a *AKS) DeassociateLB(crName types.NamespacedName, clusterSvc *corev1.Service) error {
	lbName, ok := a.crToLB[crName]
	if !ok {
		return nil
	}

	// a) remove LoadBalancer listener (delete-load-balancer-listeners)
	// b) remove inbound rules from security group (revoke-security-group-ingress)
	// if elbDesc := a.cacheELB[lbName]; elbDesc != nil {
	// 	if err := a.removeListeners(clusterSvc, elbDesc); err != nil {
	// 		return err
	// 	}
	// 	if err := a.removeInboundRules(clusterSvc, elbDesc); err != nil {
	// 		return err
	// 	}
	// }

	// c) update internal cache
	delete(a.crToLB, crName)
	delete(a.lbToCRs[lbName], crName)
	for _, svcPort := range clusterSvc.Spec.Ports {
		delete(a.lbToPorts[lbName], svcPort.Port)
	}
	log.WithName("aks").Info("DeassociateLB", "cr", crName, "lb", lbName)
	return nil
}

func (a *AKS) UpdateService(svc, lb *corev1.Service) (bool, bool) {
	lbName := types.NamespacedName{Name: lb.Name, Namespace: lb.Namespace}
	occupiedPorts := a.lbToPorts[lbName]
	if len(occupiedPorts) == 0 {
		occupiedPorts = int32Set{}
	}
	portUpdated := updatePort(svc, lb, occupiedPorts)
	// don't need to update externalIP
	return portUpdated, false
}

// func (a *AKS) queryELB(elbName string) (*elb.LoadBalancerDescription, error) {
// 	if elbName == "" {
// 		return nil, errors.New("elbName cannot be empty")
// 	}
// 	input := &elb.DescribeLoadBalancersInput{
// 		LoadBalancerNames: []*string{
// 			aws.String(elbName),
// 		},
// 	}
// 	result, err := a.elbClient.DescribeLoadBalancers(input)
// 	if err != nil {
// 		return nil, err
// 	}
// 	if len(result.LoadBalancerDescriptions) != 1 {
// 		return nil, fmt.Errorf("got %d elb.LoadBalancerDescription, but expected 1", len(result.LoadBalancerDescriptions))
// 	}
// 	return result.LoadBalancerDescriptions[0], nil
// }

// 1st return value means if it's executed
// 2nd return value returns error if it's executed
// func (a *AKS) createListeners(clusterSvc *corev1.Service, elbDesc *elb.LoadBalancerDescription) (bool, error) {
// 	if clusterSvc == nil || elbDesc == nil {
// 		return false, errors.New("clusterSvc or elbDesc is nil")
// 	}
// 	listeners := make([]*elb.Listener, 0)
// 	for _, p := range clusterSvc.Spec.Ports {
// 		lowercasedProtocol := strings.ToLower(string(p.Protocol))
// 		listener := elb.Listener{
// 			InstancePort:     aws.Int64(int64(p.NodePort)),
// 			InstanceProtocol: aws.String(lowercasedProtocol),
// 			LoadBalancerPort: aws.Int64(int64(p.Port)),
// 			Protocol:         aws.String(lowercasedProtocol),
// 			// TODO(Huang-Wei): InstanceProtocol vs. Protocol?
// 		}
// 		// check if it exists in elbDesc
// 		if isListenerExisted(listener, elbDesc.ListenerDescriptions) {
// 			continue
// 		}
// 		listeners = append(listeners, &listener)
// 	}
// 	if len(listeners) == 0 {
// 		return false, nil
// 	}

// 	input := &elb.CreateLoadBalancerListenersInput{
// 		Listeners:        listeners,
// 		LoadBalancerName: elbDesc.LoadBalancerName,
// 	}
// 	_, err := a.elbClient.CreateLoadBalancerListeners(input)

// 	// tolerate if the listener exists in server side
// 	if aerr, ok := err.(awserr.Error); ok && aerr.Code() == elb.ErrCodeDuplicateListenerException {
// 		log.WithName("aks").Info("awserr", "code", aerr.Code())
// 		return true, nil
// 	}

// 	return true, err
// }

// func isListenerExisted(l elb.Listener, listenerDescs []*elb.ListenerDescription) bool {
// 	for _, desc := range listenerDescs {
// 		e := desc.Listener
// 		if *l.InstancePort == *a.InstancePort && *l.InstanceProtocol == *a.InstanceProtocol &&
// 			*l.LoadBalancerPort == *a.LoadBalancerPort && *l.Protocol == *a.Protocol {
// 			return true
// 		}
// 	}
// 	return false
// }

// func (a *AKS) createInboundRules(clusterSvc *corev1.Service, elbDesc *elb.LoadBalancerDescription) error {
// 	if clusterSvc == nil || elbDesc == nil {
// 		return errors.New("clusterSvc or elbDesc is nil")
// 	}

// 	sgStrs := elbDesc.SecurityGroups
// 	if len(sgStrs) == 0 {
// 		return errors.New("no security group is attached to the ELB")
// 	}

// 	ipPermissions := make([]*ec2.IpPermission, 0)
// 	for _, p := range clusterSvc.Spec.Ports {
// 		lowercasedProtocol := strings.ToLower(string(p.Protocol))
// 		permission := ec2.IpPermission{
// 			FromPort:   aws.Int64(int64(p.Port)),
// 			IpProtocol: aws.String(lowercasedProtocol),
// 			IpRanges: []*ec2.IpRange{
// 				{
// 					CidrIp:      aws.String("0.0.0.0/0"),
// 					Description: aws.String("Generated by shared-loadblancer"),
// 				},
// 			},
// 			ToPort: aws.Int64(int64(p.Port)),
// 		}
// 		ipPermissions = append(ipPermissions, &permission)
// 	}
// 	if len(ipPermissions) == 0 {
// 		return nil
// 	}

// 	input := &ec2.AuthorizeSecurityGroupIngressInput{
// 		// pick up the first security group
// 		// TODO(Huang-Wei): what if multiple security groups are found
// 		GroupId:       sgStrs[0],
// 		IpPermissions: ipPermissions,
// 	}
// 	_, err := a.ec2Client.AuthorizeSecurityGroupIngress(input)
// 	// tolerate if the rules exist in server side
// 	if aerr, ok := err.(awserr.Error); ok && aerr.Code() == "InvalidPermission.Duplicate" {
// 		return nil
// 	}
// 	return err
// }

// func (a *AKS) removeListeners(clusterSvc *corev1.Service, elbDesc *elb.LoadBalancerDescription) error {
// 	if clusterSvc == nil || elbDesc == nil {
// 		return errors.New("clusterSvc or elbDesc is nil")
// 	}
// 	ports := make([]*int64, len(clusterSvc.Spec.Ports))
// 	for i, p := range clusterSvc.Spec.Ports {
// 		ports[i] = aws.Int64(int64(p.Port))
// 	}
// 	input := &elb.DeleteLoadBalancerListenersInput{
// 		LoadBalancerName:  elbDesc.LoadBalancerName,
// 		LoadBalancerPorts: ports,
// 	}
// 	_, err := a.elbClient.DeleteLoadBalancerListeners(input)
// 	return err
// }

// func (a *AKS) removeInboundRules(clusterSvc *corev1.Service, elbDesc *elb.LoadBalancerDescription) error {
// 	if clusterSvc == nil || elbDesc == nil {
// 		return errors.New("clusterSvc or elbDesc is nil")
// 	}

// 	sgStrs := elbDesc.SecurityGroups
// 	if len(sgStrs) == 0 {
// 		return errors.New("no security group is attached to the ELB")
// 	}

// 	ipPermissions := make([]*ec2.IpPermission, 0)
// 	for _, p := range clusterSvc.Spec.Ports {
// 		lowercasedProtocol := strings.ToLower(string(p.Protocol))
// 		permission := ec2.IpPermission{
// 			FromPort:   aws.Int64(int64(p.Port)),
// 			IpProtocol: aws.String(lowercasedProtocol),
// 			IpRanges: []*ec2.IpRange{
// 				{
// 					CidrIp:      aws.String("0.0.0.0/0"),
// 					Description: aws.String("Generated by shared-loadblancer"),
// 				},
// 			},
// 			ToPort: aws.Int64(int64(p.Port)),
// 		}
// 		ipPermissions = append(ipPermissions, &permission)
// 	}
// 	if len(ipPermissions) == 0 {
// 		return nil
// 	}

// 	input := &ec2.RevokeSecurityGroupIngressInput{
// 		// pick up the first security group
// 		// TODO(Huang-Wei): what if multiple security groups are found
// 		GroupId:       sgStrs[0],
// 		IpPermissions: ipPermissions,
// 	}
// 	_, err := a.ec2Client.RevokeSecurityGroupIngress(input)
// 	return err
// }
