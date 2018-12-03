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
	"strconv"
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
	loadBalancerMinimumPriority = 500
	loadBalancerMaximumPriority = 4096

	frontendIPConfigIDTemplate = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/frontendIPConfigurations/%s"
	backendPoolIDTemplate      = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/loadBalancers/%s/backendAddressPools/%s"
)

var (
	azureDefaultLBName = "kubernetes"
)

// AKS stands for Azure(Microsoft) Kubernetes Service
type AKS struct {
	subscriptionID string
	resGrpName     string
	sgName         string
	lbClient       network.LoadBalancersClient
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
	// TODO(Huang-Wei): auto configure when running inside AKS cluster
	// for local testing, make sure following env variables are properly set:
	// AZURE_TENANT_ID, AZURE_CLIENT_ID, AZURE_CLIENT_SECRET, AZURE_SUBSCRIPTION_ID
	subscriptionID := GetEnvVal("AZURE_SUBSCRIPTION_ID", "58de4ac8-a2d6-499b-b983-6f1c870d398e")
	authorizer, err := auth.NewAuthorizerFromEnvironment()
	if err != nil {
		panic(err)
	}

	aks := AKS{
		subscriptionID: subscriptionID,
		// TODO(Huang-Wei): get it from node label kubernetes.azure.com/cluster
		resGrpName: GetEnvVal("RES_GRP_NAME", "MC_res-grp-1_wei-aks_eastus"),
		// TODO(Huang-Wei): read it from?
		sgName:        GetEnvVal("SG_NAME", "aks-agentpool-37988249-nsg"),
		lbClient:      network.NewLoadBalancersClient(subscriptionID),
		pipClient:     network.NewPublicIPAddressesClient(subscriptionID),
		sgClient:      network.NewSecurityGroupsClient(subscriptionID),
		cacheMap:      make(map[types.NamespacedName]*corev1.Service),
		cachePIPMap:   make(map[types.NamespacedName]*network.PublicIPAddress),
		crToLB:        make(map[types.NamespacedName]types.NamespacedName),
		lbToCRs:       make(map[types.NamespacedName]nameSet),
		lbToPorts:     make(map[types.NamespacedName]int32Set),
		capacityPerLB: capacity,
	}
	aks.lbClient.Authorizer = authorizer
	aks.sgClient.Authorizer = authorizer
	aks.pipClient.Authorizer = authorizer

	return &aks
}

func (a *AKS) GetCapacityPerLB() int {
	return a.capacityPerLB
}

func (a *AKS) UpdateCache(key types.NamespacedName, lbSvc *corev1.Service) {
	if lbSvc == nil {
		delete(a.cacheMap, key)
		delete(a.cachePIPMap, key)
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
			executed, err := a.reconcileLBRules(clusterSvc, lbSvc, true /* create */)
			if err != nil {
				return err
			}
			if executed {
				if err := a.reconcileSGRules(clusterSvc, lbSvc, true /* create */); err != nil {
					return err
				}
			}
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
			executed, err := a.reconcileLBRules(clusterSvc, lbSvc, false /* delete */)
			if err != nil {
				return err
			}
			if executed {
				if err := a.reconcileSGRules(clusterSvc, lbSvc, false /* delete */); err != nil {
					return err
				}
			}
		}
	}

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

func (a *AKS) getDefaultAzureLB() (*network.LoadBalancer, error) {
	azureLB, err := a.lbClient.Get(context.TODO(), a.resGrpName, azureDefaultLBName, "")
	if err != nil {
		return nil, err
	}
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

// 1st return value means if it's executed
// 2nd return value returns error if it's executed
func (a *AKS) reconcileLBRules(clusterSvc, lbSvc *corev1.Service, wantCreate bool) (bool, error) {
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

	// reconcile loadbalancing rules
	var updatedLBRules []network.LoadBalancingRule
	lbRules := make([]network.LoadBalancingRule, 0)
	for _, p := range clusterSvc.Spec.Ports {
		transportProto, _, _, err := getProtocolsFromKubernetesProtocol(p.Protocol)
		if err != nil {
			return false, err
		}
		lbRule := network.LoadBalancingRule{
			// it's required to be consistent with AKS cloud provider naming pattern
			Name: to.StringPtr(fmt.Sprintf("%s-%s-%d", lbFrontendIPConfigName, p.Protocol, p.Port)),
			LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
				Protocol:             *transportProto,
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
				// Probe is unnecessary
			},
		}
		lbRules = append(lbRules, lbRule)
	}

	var needUpdate bool
	if wantCreate {
		// log.WithName("aks").Info("[DEBUG] before unionLBRules", "existing", len(*azureLB.LoadBalancingRules), "incoming", len(lbRules))
		needUpdate, updatedLBRules = unionLBRules(*azureLB.LoadBalancingRules, lbRules)
		// log.WithName("aks").Info("[DEBUG] after unionLBRules", "existing", len(*azureLB.LoadBalancingRules), "updatedLBRules", len(updatedLBRules))
	} else {
		// log.WithName("aks").Info("[DEBUG] before subtractLBRules", "existing", len(*azureLB.LoadBalancingRules), "incoming", len(lbRules))
		needUpdate, updatedLBRules = subtractLBRules(*azureLB.LoadBalancingRules, lbRules)
		// log.WithName("aks").Info("[DEBUG] after subtractLBRules", "existing", len(*azureLB.LoadBalancingRules), "updatedLBRules", len(updatedLBRules))
	}

	if !needUpdate {
		log.WithName("aks").Info("No need to reconcile LB rules")
		return false, nil
	}
	// create or update LB
	azureLB.LoadBalancingRules = &updatedLBRules
	_, err = a.lbClient.CreateOrUpdate(context.TODO(), a.resGrpName, *azureLB.Name, *azureLB)
	return true, err
}

func (a *AKS) reconcileSGRules(clusterSvc, lbSvc *corev1.Service, wantCreate bool) error {
	sg, err := a.sgClient.Get(context.TODO(), a.resGrpName, a.sgName, "")
	if err != nil {
		return err
	}
	lbFrontendIPConfigName := cloudprovider.DefaultLoadBalancerName(lbSvc)

	// reconcile securitygroup rules (only care about inbound rules)
	var updatedSGRules []network.SecurityRule
	sgRules := make([]network.SecurityRule, 0)
	for _, p := range clusterSvc.Spec.Ports {
		_, securityProto, _, err := getProtocolsFromKubernetesProtocol(p.Protocol)
		if err != nil {
			return err
		}
		if err != nil {
			return err
		}
		sgRule := network.SecurityRule{
			Name: to.StringPtr(fmt.Sprintf("%s-%s-%d-Internet", lbFrontendIPConfigName, p.Protocol, p.Port)),
			SecurityRulePropertiesFormat: &network.SecurityRulePropertiesFormat{
				Protocol: *securityProto,
				// Priority:                 to.Int32Ptr(nextPriority),
				SourceAddressPrefix:      to.StringPtr("Internet"),
				SourcePortRange:          to.StringPtr("*"),
				DestinationAddressPrefix: to.StringPtr("*"),
				DestinationPortRange:     to.StringPtr(strconv.Itoa(int(p.NodePort))),
				Access:                   network.SecurityRuleAccessAllow,
				Direction:                network.SecurityRuleDirectionInbound,
			},
		}
		sgRules = append(sgRules, sgRule)
	}
	var needUpdate bool
	if wantCreate {
		// log.WithName("aks").Info("[DEBUG] before unionSGRules", "existing", len(*sg.SecurityRules), "incoming", len(sgRules))
		needUpdate, updatedSGRules = unionSGRules(*sg.SecurityRules, sgRules)
		// log.WithName("aks").Info("[DEBUG] after unionSGRules", "existing", len(*sg.SecurityRules), "updatedSGRules", len(updatedSGRules))
	} else {
		// log.WithName("aks").Info("[DEBUG] before subtractSGRules", "existing", len(*sg.SecurityRules), "incoming", len(sgRules))
		needUpdate, updatedSGRules = subtractSGRules(*sg.SecurityRules, sgRules)
		// log.WithName("aks").Info("[DEBUG] after subtractSGRules", "existing", len(*sg.SecurityRules), "updatedSGRules", len(updatedSGRules))
	}

	if !needUpdate {
		log.WithName("aks").Info("No need to reconcile SG inbound rules")
		return nil
	}
	for i := range updatedSGRules {
		rule := updatedSGRules[i]
		if rule.Priority != nil {
			continue
		}
		nextPriority, err := getNextAvailablePriority(updatedSGRules)
		if err != nil {
			return err
		}
		rule.Priority = to.Int32Ptr(nextPriority)
	}
	// create or update SG
	sg.SecurityRules = &updatedSGRules
	_, err = a.sgClient.CreateOrUpdate(context.TODO(), a.resGrpName, a.sgName, sg)
	return err
}

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

func unionLBRules(existingRules, expectedRules []network.LoadBalancingRule) (bool, []network.LoadBalancingRule) {
	var needUpdate bool
	toAddRules := make([]network.LoadBalancingRule, 0)
	for _, expected := range expectedRules {
		if !findRule(existingRules, expected) {
			needUpdate = true
			toAddRules = append(toAddRules, expected)
		}
	}
	return needUpdate, append(existingRules, toAddRules...)
}

func subtractLBRules(existingRules, unexpectedRules []network.LoadBalancingRule) (bool, []network.LoadBalancingRule) {
	var needUpdate bool
	for i := len(existingRules) - 1; i >= 0; i-- {
		if findRule(unexpectedRules, existingRules[i]) {
			needUpdate = true
			existingRules = append(existingRules[:i], existingRules[i+1:]...)
		}
	}
	return needUpdate, existingRules
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

func unionSGRules(existingRules, expectedRules []network.SecurityRule) (bool, []network.SecurityRule) {
	var needUpdate bool
	toAddRules := make([]network.SecurityRule, 0)
	for _, expected := range expectedRules {
		if !findSecurityRule(existingRules, expected) {
			needUpdate = true
			toAddRules = append(toAddRules, expected)
		}
	}
	return needUpdate, append(existingRules, toAddRules...)
}

func subtractSGRules(existingRules, unexpectedRules []network.SecurityRule) (bool, []network.SecurityRule) {
	var needUpdate bool
	for i := len(existingRules) - 1; i >= 0; i-- {
		if findSecurityRule(unexpectedRules, existingRules[i]) {
			needUpdate = true
			existingRules = append(existingRules[:i], existingRules[i+1:]...)
		}
	}
	return needUpdate, existingRules
}

// returns the equivalent LoadBalancerRule, SecurityRule and LoadBalancerProbe
// protocol types for the given Kubernetes protocol type.
func getProtocolsFromKubernetesProtocol(protocol corev1.Protocol) (*network.TransportProtocol, *network.SecurityRuleProtocol, *network.ProbeProtocol, error) {
	var transportProto network.TransportProtocol
	var securityProto network.SecurityRuleProtocol
	var probeProto network.ProbeProtocol

	switch protocol {
	case corev1.ProtocolTCP:
		transportProto = network.TransportProtocolTCP
		securityProto = network.SecurityRuleProtocolTCP
		probeProto = network.ProbeProtocolTCP
		return &transportProto, &securityProto, &probeProto, nil
	case corev1.ProtocolUDP:
		transportProto = network.TransportProtocolUDP
		securityProto = network.SecurityRuleProtocolUDP
		return &transportProto, &securityProto, nil, nil
	default:
		return &transportProto, &securityProto, &probeProto, fmt.Errorf("only TCP and UDP are supported for Azure LoadBalancers")
	}
}

// This compares rule's Name, Protocol, SourcePortRange, DestinationPortRange, SourceAddressPrefix, Access, and Direction.
func findSecurityRule(rules []network.SecurityRule, rule network.SecurityRule) bool {
	for _, existingRule := range rules {
		if !strings.EqualFold(to.String(existingRule.Name), to.String(rule.Name)) {
			continue
		}
		if existingRule.Protocol != rule.Protocol {
			continue
		}
		if !strings.EqualFold(to.String(existingRule.SourcePortRange), to.String(rule.SourcePortRange)) {
			continue
		}
		if !strings.EqualFold(to.String(existingRule.DestinationPortRange), to.String(rule.DestinationPortRange)) {
			continue
		}
		if !strings.EqualFold(to.String(existingRule.SourceAddressPrefix), to.String(rule.SourceAddressPrefix)) {
			continue
		}
		if existingRule.Access != rule.Access {
			continue
		}
		if existingRule.Direction != rule.Direction {
			continue
		}
		return true
	}
	return false
}

// This returns the next available rule priority level for a given set of security rules.
func getNextAvailablePriority(rules []network.SecurityRule) (int32, error) {
	var smallest int32 = loadBalancerMinimumPriority
	var spread int32 = 1

outer:
	for smallest < loadBalancerMaximumPriority {
		for _, rule := range rules {
			if rule.Priority == nil {
				continue
			}
			if *rule.Priority == smallest {
				smallest += spread
				continue outer
			}
		}
		// no one else had it
		return smallest, nil
	}

	return -1, fmt.Errorf("securityGroup priorities are exhausted")
}
