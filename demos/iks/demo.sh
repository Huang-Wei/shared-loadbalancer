#!/usr/bin/env bash

source $(dirname "$0")/../demoscript
source ~/.iks

comment This is a demo on IBM Kubernetes Service

doit source ~/.iks
doit echo "\$KUBECONFIG"

doit kubectl get node
doit kubectl version --short

doit kubectl get svc
doit kubectl get pod

comment We are going to create 2 deployments - one TCP and one UDP

doit cat demos/iks/deployments/tcp-deploy.yaml
doit cat demos/iks/deployments/udp-deploy.yaml
doit kubectl create -f demos/iks/deployments/
doit kubectl get pod

comment Custom Resource Definition has been pre-created
doit kubectl get crd
doit kubectl get slb

comment We are going to create 2 SharedLB CRs
doit cat demos/iks/crs/cr-tcp.yaml
doit cat demos/iks/crs/cr-udp.yaml

doit kubectl create -f demos/iks/crs/cr-tcp.yaml
doit kubectl get svc
doit kubectl get slb

comment By default CAPACITY is 5
doit kubectl create -f demos/iks/crs/cr-udp.yaml
doit kubectl get svc
doit kubectl get slb

comment Use nc to try connecting

doit "echo 'tcp-test $(date)'  | nc $(kubectl get slb | grep sharedlb-tcp | awk '{print $2}') $(kubectl get slb sharedlb-tcp | grep sharedlb-tcp | awk '{print $3}')"
doit "echo 'udp-test $(date)' | nc -w 1 -u $(kubectl get slb | grep sharedlb-udp | awk '{print $2}') $(kubectl get slb sharedlb-udp | grep sharedlb-udp | awk '{print $3}')"

comment Checkout pod logs

doit kubectl logs $(kubectl get po | grep tcp-deploy | awk '{print $1}')
doit kubectl logs $(kubectl get po | grep udp-deploy | awk '{print $1}')

comment What if I just want a random source port

doit cat demos/iks/crs/cr-tcp-random-port.yaml
doit kubectl create -f demos/iks/crs/cr-tcp-random-port.yaml
doit kubectl get svc
doit kubectl get slb

# comment Cleanup

# doit kubectl delete deploy --all
# doit kubectl delete slb --all
# doit kubectl delete svc -l=lb-template=

comment ~End~