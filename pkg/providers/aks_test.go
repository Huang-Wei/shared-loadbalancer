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
	"reflect"
	"testing"

	"github.com/Azure/go-autorest/autorest/to"

	"github.com/Azure/azure-sdk-for-go/services/network/mgmt/2017-09-01/network"
)

func Test_union(t *testing.T) {
	tests := []struct {
		name          string
		existingRules []network.LoadBalancingRule
		expectedRules []network.LoadBalancingRule
		want          []network.LoadBalancingRule
	}{
		{
			name: "union simple case",
			existingRules: []network.LoadBalancingRule{
				{
					Name: to.StringPtr("rule1"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(81),
					},
				},
				{
					Name: to.StringPtr("rule2"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(82),
					},
				},
			},
			expectedRules: []network.LoadBalancingRule{
				{
					Name: to.StringPtr("rule3"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(83),
					},
				},
				{
					Name: to.StringPtr("rule4"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(84),
					},
				},
			},
			want: []network.LoadBalancingRule{
				{
					Name: to.StringPtr("rule1"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(81),
					},
				},
				{
					Name: to.StringPtr("rule2"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(82),
					},
				},
				{
					Name: to.StringPtr("rule3"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(83),
					},
				},
				{
					Name: to.StringPtr("rule4"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(84),
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := union(tt.existingRules, tt.expectedRules); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("union() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_subtract(t *testing.T) {
	tests := []struct {
		name            string
		existingRules   []network.LoadBalancingRule
		unexpectedRules []network.LoadBalancingRule
		want            []network.LoadBalancingRule
	}{
		{
			name: "subtract simple case",
			existingRules: []network.LoadBalancingRule{
				{
					Name: to.StringPtr("rule1"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(81),
					},
				},
				{
					Name: to.StringPtr("rule2"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(82),
					},
				},
				{
					Name: to.StringPtr("rule3"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(83),
					},
				},
				{
					Name: to.StringPtr("rule4"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(84),
					},
				},
			},
			unexpectedRules: []network.LoadBalancingRule{
				{
					Name: to.StringPtr("rule1"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(81),
					},
				},
				{
					Name: to.StringPtr("rule3"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(83),
					},
				},
			},
			want: []network.LoadBalancingRule{
				{
					Name: to.StringPtr("rule2"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(82),
					},
				},
				{
					Name: to.StringPtr("rule4"),
					LoadBalancingRulePropertiesFormat: &network.LoadBalancingRulePropertiesFormat{
						FrontendPort: to.Int32Ptr(84),
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := subtract(tt.existingRules, tt.unexpectedRules); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("subtract() = %v, want %v", got, tt.want)
			}
		})
	}
}
