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
	"os"
	"testing"
)

func TestGetEnvValInt(t *testing.T) {
	tests := []struct {
		name       string
		envKey     string
		defaultVal int
		want       int
		preSetup   func()
	}{
		{
			name:       "env variable not exist",
			envKey:     "CAPACITYTEST",
			defaultVal: 2,
			want:       2,
			preSetup:   func() {},
		},
		{
			name:       "env variable exists but with a non-int value",
			envKey:     "CAPACITYTEST",
			defaultVal: 2,
			want:       2,
			preSetup: func() {
				os.Setenv("CAPACITYTEST", "1a2b")
			},
		},
		{
			name:       "env variable exists and with a int value",
			envKey:     "CAPACITYTEST",
			defaultVal: 2,
			want:       10,
			preSetup: func() {
				os.Setenv("CAPACITYTEST", "10")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.preSetup()
			if got := GetEnvValInt(tt.envKey, tt.defaultVal); got != tt.want {
				t.Errorf("GetEnvValInt() = %v, want %v", got, tt.want)
			}
		})
	}
}
