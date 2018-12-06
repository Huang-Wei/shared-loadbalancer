#!/usr/bin/env bash

source $(dirname "$0")/../demoscript
source ~/.eks

comment "This is a demo on Elastic (Amazon) Kubernetes Service"

doit source ~/.eks
doit echo "\$KUBECONFIG"

doit kubectl version --short

doit kubectl get svc

comment We are going to create 4 TCP deployments

doit ls demos/eks/deployments
doit kubectl create -f demos/eks/deployments/

comment Custom Resource Definition has been pre-created
doit kubectl get crd
doit kubectl get slb

# comment To demo the case LB is out of capacity, we set CAPACITY to 2
doit kubectl create -f demos/eks/crs/cr-tcp1.yaml
doit kubectl get svc

comment EKS LB needs ~3 seconds to be created
# wait "kubectl get svc | grep -v pending &> /dev/null"
doit kubectl get svc
doit kubectl get slb

comment "Let's take a look at AWS console"

doit nc $(kubectl get slb | grep sharedlb-tcp | awk '{print $2}') $(kubectl get slb sharedlb-tcp | grep sharedlb-tcp | awk '{print $3}')

comment Now we create the second SharedLB CR

doit kubectl create -f demos/eks/crs/cr-tcp2.yaml
doit kubectl get svc
doit kubectl get slb

comment "As of now, we're out of capacity, so next CR creation is expected to trigger a new LB creation"

doit kubectl create -f demos/eks/crs/cr-tcp3.yaml
doit wait "kubectl get svc | grep -v pending"
doit kubectl get svc
doit kubectl get slb

# comment Cleanup

# doit kubectl delete deploy --all
# doit kubectl delete slb --all
# doit kubectl delete svc -l=lb-template=

comment ~End~