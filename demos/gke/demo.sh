#!/usr/bin/env bash

source $(dirname "$0")/../demoscript
source ~/.gke

comment This is a demo on Google Kubernetes Engine

doit source ~/.gke
doit echo "\$KUBECONFIG"

doit kubectl get node
doit kubectl version --short

doit kubectl get svc,pod

comment We are going to create some deployments

doit kubectl create -f demos/gke/deployments/

comment Custom Resource Definition has been pre-created
doit kubectl get crd
doit kubectl get slb

comment We are going to create 4 SharedLB CRs in parallel
doit kubectl create -f demos/gke/crs

comment So are you expected to see FOUR LBs being created, or just ONE?
doit kubectl get svc
doit kubectl get slb

comment GKE LB needs ~1min to be created
doit kubectl get svc
doit kubectl get slb

comment Use nc to try connecting
out=$(kubectl get slb)
doit nc -zv $(echo "$out" | grep sharedlb-tcp1 | awk '{print $2}') $(echo "$out" | grep sharedlb-tcp1 | awk '{print $3}')
doit nc -zv $(echo "$out" | grep sharedlb-tcp4 | awk '{print $2}') $(echo "$out" | grep sharedlb-tcp4 | awk '{print $3}')

# comment Cleanup

# doit kubectl delete deploy --all
# doit kubectl delete slb --all
# doit kubectl delete svc -l=lb-template=

comment ~End~