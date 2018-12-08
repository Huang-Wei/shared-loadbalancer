#!/usr/bin/env bash

source $(dirname "$0")/../demoscript
source ~/.eks

comment "This is a demo on Elastic (Amazon) Kubernetes Service"

doit source ~/.eks
doit echo "\$KUBECONFIG"

doit kubectl version --short

doit kubectl get svc

comment "We are going to create some TCP deployments"

doit ls demos/eks/deployments
doit kubectl create -f demos/eks/deployments/

comment Custom Resource Definition has been pre-created
doit kubectl get crd
doit kubectl get slb

comment To demo the case LB resource is out of capacity, CAPACITY is configured to 2
doit cat demos/eks/crs/cr-tcp1.yaml
doit kubectl create -f demos/eks/crs/cr-tcp1.yaml
# doit kubectl get svc

comment EKS LB needs ~3 seconds to be created
doit kubectl get svc
doit kubectl get slb
doit kubectl get slb -o custom-columns="NAME:metadata.name,EXTERNAL-IP:.status.loadBalancer.ingress[*].*,PORT:.spec.ports[*].port"

comment "Let's take a look at AWS console"

doit kubectl create -f demos/eks/crs/cr-tcp2.yaml
doit kubectl get svc
doit kubectl get slb -o custom-columns="NAME:metadata.name,EXTERNAL-IP:.status.loadBalancer.ingress[*].*,PORT:.spec.ports[*].port"

comment "As of now, we're out of capacity, so next CR creation is expected to trigger a new LB creation"

doit kubectl create -f demos/eks/crs/cr-tcp3.yaml
comment Wait for another 3 seconds
doit kubectl get svc
doit kubectl get slb -o custom-columns="NAME:metadata.name,EXTERNAL-IP:.status.loadBalancer.ingress[*].*,PORT:.spec.ports[*].port"

comment "Finally let's check connectivity"
out=$(kubectl get slb -o custom-columns="NAME:metadata.name,EXTERNAL-IP:.status.loadBalancer.ingress[*].*,PORT:.spec.ports[*].port")
doit nslookup $(echo "$out" | grep sharedlb-tcp1 | awk '{print $2}')
doit nc -zv $(echo "$out" | grep sharedlb-tcp1 | awk '{print $2}') $(echo "$out" | grep sharedlb-tcp1 | awk '{print $3}')

comment "Let's delete the 1st SharedLB CR"
doit kubectl delete slb sharedlb-tcp1
doit kubectl get svc
doit kubectl get slb
comment "Checkout AWS console again"

# comment Cleanup

# doit kubectl delete deploy --all
# doit kubectl delete slb --all
# doit kubectl delete svc -l=lb-template=

comment ~End~