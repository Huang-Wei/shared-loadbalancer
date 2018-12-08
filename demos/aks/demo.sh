#!/usr/bin/env bash

source $(dirname "$0")/../demoscript
source ~/.aks

comment This is a demo on Azure Kubernetes Service

doit source ~/.aks
doit echo "\$KUBECONFIG"

doit kubectl get node
doit kubectl version --short

doit kubectl get svc,pod

comment We are going to create some deployments
doit kubectl create -f demos/aks/deployments/

comment Custom Resource Definition has been pre-created
doit kubectl get crd
doit kubectl get slb

comment We are going to create some SharedLB CRs
doit cat demos/aks/crs/cr-tcp1-4000.yaml
doit kubectl create -f demos/aks/crs/cr-tcp1-4000.yaml

doit kubectl get svc
doit kubectl get slb

comment AKS LB needs ~1 minute to be created

doit kubectl get svc
doit kubectl get slb

comment What if another ShareLB CR also requests port 4000
doit cat demos/aks/crs/cr-tcp2-4000.yaml
doit kubectl create -f demos/aks/crs/cr-tcp2-4000.yaml

doit kubectl get svc
doit kubectl get slb

comment AKS LB needs another ~1 minute to be created
doit kubectl get svc
doit kubectl get slb

comment Use nc to try connecting
out=$(kubectl get slb)
doit nc -zv $(echo "$out" | grep sharedlb-tcp1 | awk '{print $2}') $(echo "$out" | grep sharedlb-tcp1 | awk '{print $3}')
doit nc -zv $(echo "$out" | grep sharedlb-tcp2 | awk '{print $2}') $(echo "$out" | grep sharedlb-tcp2 | awk '{print $3}')

comment Right now both LBs have spare capacity
doit kubectl create -f demos/aks/crs/cr-tcp-random.yaml
doit kubectl get svc
doit kubectl get slb

# comment Cleanup

# doit kubectl delete deploy --all
# doit kubectl delete slb --all
# doit kubectl delete svc -l=lb-template=

comment ~End~