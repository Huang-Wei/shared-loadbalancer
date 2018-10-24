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
	"math/rand"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

var letterRunes = []rune("0123456789abcdefghijklmnopqrstuvwxyz")

func RandStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func GetEnvVal(envKey, defVal string) string {
	if val := os.Getenv(envKey); val != "" {
		return val
	}
	return defVal
}

func GetNamespacedName(svc *corev1.Service) types.NamespacedName {
	if svc == nil {
		return types.NamespacedName{}
	}
	return types.NamespacedName{Name: svc.Name, Namespace: svc.Namespace}
}

// GetRandomInt returns an integer in range [min, max)
func GetRandomInt(min, max int) int {
	return rand.Intn(max-min) + min
}

func GetRandomPort() int32 {
	// TODO(Huang-Wei): change to [1000, 65535)?
	return int32(GetRandomInt(1000, 10000))
}
