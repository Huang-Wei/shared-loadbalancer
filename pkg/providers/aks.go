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
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Azure/azure-sdk-for-go/services/network/mgmt/2017-09-01/network"
	"github.com/Azure/go-autorest/autorest/azure/auth"
	"github.com/Azure/go-autorest/autorest/to"
	kubeconv1alpha1 "github.com/Huang-Wei/shared-loadbalancer/pkg/apis/kubecon/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	cloudprovider "k8s.io/cloud-provider"
)

// Refs:
// * https://github.com/Azure/azure-sdk-for-go
// * https://github.com/Azure-Samples/azure-sdk-for-go-samples
// * https://docs.microsoft.com/en-us/azure/azure-stack/user/azure-stack-version-profiles-go
// * https://docs.microsoft.com/en-us/azure/aks/kubernetes-service-principal

const (
	frontendIPConfigIDTemplate = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/frontendIPConfigurations/%s"
	backendPoolIDTemplate      = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/backendAddressPools/%s"
)

var (
	azureDefaultLBName = "kubernetes"
	azureLbIdPrefix    string
)

// AKS stands for Azure(Microsoft) Kubernetes Service
type AKS struct {
	resGrpName     string
	subscriptionID string
	lbClient       network.LoadBalancersClient
	lbFIPClient    network.LoadBalancerFrontendIPConfigurationsClient
	lbRuleClient   network.LoadBalancerLoadBalancingRulesClient
	lbProbeClient  network.LoadBalancerProbesClient
	sgClient       network.SecurityGroupsClient
	pipClient      network.PublicIPAddressesClient

	// key is namespacedName of a LB Serivce, val is the service
	cacheMap            map[types.NamespacedName]*corev1.Service
	cachePIPMap         map[types.NamespacedName]*network.PublicIPAddress
	cacheAzureDefaultLB *network.LoadBalancer

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
	// don't need to if running this program in an Azure vm
	authorizer, err := auth.NewAuthorizerFromEnvironment()
	if err != nil {
		panic(err)
	}

	aks := AKS{
		// TODO(Huang-Wei): get it from node label kubernetes.azure.com/cluster
		resGrpName:     "MC_res-grp-1_wei-aks_eastus",
		subscriptionID: subscriptionID,
		lbClient:       network.NewLoadBalancersClient(subscriptionID),
		lbFIPClient:    network.NewLoadBalancerFrontendIPConfigurationsClient(subscriptionID),
		lbRuleClient:   network.NewLoadBalancerLoadBalancingRulesClient(subscriptionID),
		lbProbeClient:  network.NewLoadBalancerProbesClient(subscriptionID),
		pipClient:      network.NewPublicIPAddressesClient(subscriptionID),
		sgClient:       network.NewSecurityGroupsClient(subscriptionID),
		cacheMap:       make(map[types.NamespacedName]*corev1.Service),
		cachePIPMap:    make(map[types.NamespacedName]*network.PublicIPAddress),
		crToLB:         make(map[types.NamespacedName]types.NamespacedName),
		lbToCRs:        make(map[types.NamespacedName]nameSet),
		lbToPorts:      make(map[types.NamespacedName]int32Set),
		capacityPerLB:  capacity,
	}
	aks.lbClient.Authorizer = authorizer
	aks.lbFIPClient.Authorizer = authorizer
	aks.lbProbeClient.Authorizer = authorizer
	aks.sgClient.Authorizer = authorizer
	aks.pipClient.Authorizer = authorizer

	azureLbIdPrefix = fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers", subscriptionID, aks.resGrpName)
	return &aks
}

func (a *AKS) GetCapacityPerLB() int {
	return a.capacityPerLB
}

func (a *AKS) UpdateCache(key types.NamespacedName, lbSvc *corev1.Service) {
	if lbSvc == nil {
		delete(a.cacheMap, key)
		delete(a.cachePIPMap, key)
		// TODO(Huang-Wei): remove cacheAzureDefaultLB when necessary, and ensure it's in sync
		// with Azure server side
	} else {
		a.cacheMap[key] = lbSvc
		// handle azure public/frontend ip
		if len(lbSvc.Status.LoadBalancer.Ingress) == 1 {
			pip := lbSvc.Status.LoadBalancer.Ingress[0].IP
			if result, err := a.queryPublicIP(pip, lbSvc); err != nil {
				log.WithName("aks").Error(err, "cannot query public ip", "pip", pip)
			} else {
				log.WithName("aks").Info("pip object is updated in local cache", "key", key, "pip", pip)
				a.cachePIPMap[key] = result
			}
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
	// a) create Azure LoadBalancer FrontendIP rule (az network lb rule create)
	// b) create Azure Network Security Group rule (az network nsg rule create)
	if clusterSvc != nil {
		pip, lbSvc := a.cachePIPMap[lbName], a.cacheMap[lbName]
		if pip != nil && lbSvc != nil {
			_, err := a.reconcileLBRules(clusterSvc, lbSvc, true /* create */)
			if err != nil {
				return err
			}
			// TODO(Huang-Wei): create nag rules
		}
		// upon program starts, a.lbToPorts[lbName] can be nil
		if a.lbToPorts[lbName] == nil {
			a.lbToPorts[lbName] = int32Set{}
		}
		// update crToPorts
		for _, svcPort := range clusterSvc.Spec.Ports {
			a.lbToPorts[lbName][svcPort.Port] = struct{}{}
		}
	}
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

// DeassociateLB is called by AKS finalizer to clean frontend ip rules
// and network security group rules
func (a *AKS) DeassociateLB(crName types.NamespacedName, clusterSvc *corev1.Service) error {
	lbName, ok := a.crToLB[crName]
	if !ok {
		return nil
	}

	// a) delete Azure LoadBalancer FrontendIP rule (az network lb rule delete)
	// b) delete Azure Network Security Group rule (az network nsg rule delete)
	if pip := a.cachePIPMap[lbName]; pip != nil {
		pip, lbSvc := a.cachePIPMap[lbName], a.cacheMap[lbName]
		if pip != nil && lbSvc != nil {
			_, err := a.reconcileLBRules(clusterSvc, lbSvc, false /* delete */)
			if err != nil {
				return err
			}
		}
		// TODO(Huang-Wei): remove nsg rules
	}
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

// TODO(Huang-Wei): might need to query it everytime, instead of of maintaining a cache
func (a *AKS) ensureDefaultAzureLB() error {
	if a.cacheAzureDefaultLB != nil {
		return nil
	}
	lb, err := a.lbClient.Get(context.TODO(), a.resGrpName, azureDefaultLBName, "")
	if err != nil {
		return err
	}
	a.cacheAzureDefaultLB = &lb
	return nil
}

func (a *AKS) getDefaultAzureLB() (*network.LoadBalancer, error) {
	azureLB, err := a.lbClient.Get(context.TODO(), a.resGrpName, azureDefaultLBName, "")
	if err != nil {
		return nil, err
	}
	// a.cacheAzureDefaultLB = &azureLB // maybe don't cache defaultLB
	return &azureLB, nil
}

func (a *AKS) queryPublicIP(pipName string, lbSvc *corev1.Service) (*network.PublicIPAddress, error) {
	if pipName == "" {
		return nil, errors.New("public ip cannot be empty")
	}

	lbFrontendIPConfigName := cloudprovider.DefaultLoadBalancerName(lbSvc)
	publicIP, err := a.pipClient.Get(context.TODO(), a.resGrpName, fmt.Sprintf("%s-%s", azureDefaultLBName, lbFrontendIPConfigName), "")
	return &publicIP, err
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
func (a *AKS) reconcileLBRules(clusterSvc, lbSvc *corev1.Service, wantCreate bool /*, pip *network.PublicIPAddress */) (bool, error) {
	azureLB, err := a.getDefaultAzureLB()
	if err != nil {
		// we don't create Azure LoadBalancer from scratch - it's owned by AKS cloud provider
		return false, err
	}
	lbFrontendIPConfigName := cloudprovider.DefaultLoadBalancerName(lbSvc)
	lbFrontendIPConfigID := a.getFrontendIPConfigID(*azureLB.Name, lbFrontendIPConfigName)
	// TODO(Huang-Wei): in AKS cloud provider code, it's using `clusterName`
	lbBackendPoolName := azureDefaultLBName
	lbBackendPoolID := a.getBackendPoolID(*azureLB.Name, lbBackendPoolName)

	// reconcile rules
	lbRules := make([]network.LoadBalancingRule, 0)
	var updatedLBRules []network.LoadBalancingRule
	for _, p := range clusterSvc.Spec.Ports {
		lbRule := network.LoadBalancingRule{
			// it's required to be consistent with AKS cloud provider naming pattern
			Name: to.StringPtr(fmt.Sprintf("%s-%s-%d", lbFrontendIPConfigName, p.Protocol, p.Port)),
			LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
				// TODO(Huang-Wei): change to p.Protocol
				Protocol:             network.TransportProtocolTCP,
				FrontendPort:         to.Int32Ptr(p.Port),
				BackendPort:          to.Int32Ptr(p.NodePort),
				IdleTimeoutInMinutes: to.Int32Ptr(4),
				EnableFloatingIP:     to.BoolPtr(false),
				LoadDistribution:     network.Default,
				FrontendIPConfiguration: &network.SubResource{
					ID: &lbFrontendIPConfigID,
				},
				BackendAddressPool: &network.SubResource{
					ID: &lbBackendPoolID,
				},
				// Probe: &network.SubResource{
				// 	ID: to.StringPtr(fmt.Sprintf("/%s/%s/probes/%s", idPrefix, lbName, probeName)),
				// },
			},
		}
		lbRules = append(lbRules, lbRule)
	}

	if wantCreate {
		log.WithName("aks").Info("[DEBUG] before union", "existing", len(*azureLB.LoadBalancingRules), "incoming", len(lbRules))
		updatedLBRules = union(*azureLB.LoadBalancingRules, lbRules)
		log.WithName("aks").Info("[DEBUG] after union", "existing", len(*azureLB.LoadBalancingRules), "updatedLBRules", len(updatedLBRules))
	} else {
		log.WithName("aks").Info("[DEBUG] before subtract", "existing", len(*azureLB.LoadBalancingRules), "incoming", len(lbRules))
		updatedLBRules = subtract(*azureLB.LoadBalancingRules, lbRules)
		log.WithName("aks").Info("[DEBUG] after subtract", "existing", len(*azureLB.LoadBalancingRules), "updatedLBRules", len(updatedLBRules))
	}

	if len(updatedLBRules) == len(lbRules) {
		log.WithName("aks").Info("No need to reconcile LB rules")
		return false, nil
	}
	azureLB.LoadBalancingRules = &updatedLBRules
	_, err = a.lbClient.CreateOrUpdate(context.TODO(), a.resGrpName, *azureLB.Name, *azureLB)
	return true, err
}

func isLBRuleExisted(lbRule network.LoadBalancingRule, existingRules []network.LoadBalancingRule) bool {
	if existingRules == nil {
		return false
	}
	for _, existing := range existingRules {
		if *lbRule.Name == *existing.Name {
			return true
		}
	}
	return false
}

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

func (a *AKS) getFrontendIPConfigID(lbName, backendPoolName string) string {
	return fmt.Sprintf(
		frontendIPConfigIDTemplate,
		a.subscriptionID,
		a.resGrpName,
		lbName,
		backendPoolName)
}

func (a *AKS) getBackendPoolID(lbName, backendPoolName string) string {
	return fmt.Sprintf(
		backendPoolIDTemplate,
		a.subscriptionID,
		a.resGrpName,
		lbName,
		backendPoolName)
}

func union(existingRules, expectedRules []network.LoadBalancingRule) []network.LoadBalancingRule {
	toAddRules := make([]network.LoadBalancingRule, 0)
	for _, expected := range expectedRules {
		if !findRule(existingRules, expected) {
			toAddRules = append(toAddRules, expected)
		}
	}
	return append(existingRules, toAddRules...)
}

func subtract(existingRules, unexpectedRules []network.LoadBalancingRule) []network.LoadBalancingRule {
	for i := len(existingRules) - 1; i >= 0; i-- {
		if findRule(unexpectedRules, existingRules[i]) {
			existingRules = append(existingRules[:i], existingRules[i+1:]...)
		}
	}
	return existingRules
}

func findRule(rules []network.LoadBalancingRule, rule network.LoadBalancingRule) bool {
	for _, existingRule := range rules {
		if strings.EqualFold(to.String(existingRule.Name), to.String(rule.Name)) &&
			equalLoadBalancingRulePropertiesFormat(existingRule.LoadBalancingRulePropertiesFormat, rule.LoadBalancingRulePropertiesFormat) {
			return true
		}
	}
	return false
}

// equalLoadBalancingRulePropertiesFormat checks whether the provided LoadBalancingRulePropertiesFormat are equal.
// Note: only fields used in reconcileLoadBalancer are considered.
func equalLoadBalancingRulePropertiesFormat(s, t *network.LoadBalancingRulePropertiesFormat) bool {
	if s == nil || t == nil {
		return false
	}

	return reflect.DeepEqual(s.Protocol, t.Protocol) &&
		reflect.DeepEqual(s.FrontendIPConfiguration, t.FrontendIPConfiguration) &&
		reflect.DeepEqual(s.BackendAddressPool, t.BackendAddressPool) &&
		reflect.DeepEqual(s.LoadDistribution, t.LoadDistribution) &&
		reflect.DeepEqual(s.FrontendPort, t.FrontendPort) &&
		reflect.DeepEqual(s.BackendPort, t.BackendPort) &&
		reflect.DeepEqual(s.EnableFloatingIP, t.EnableFloatingIP) &&
		reflect.DeepEqual(s.IdleTimeoutInMinutes, t.IdleTimeoutInMinutes)
}
