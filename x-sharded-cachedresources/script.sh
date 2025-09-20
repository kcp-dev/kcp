#!/bin/bash

kubectl create -f ./x-sharded-cachedresources/01-ws-provider-rw.yaml
sleep 1
kubectl ws provider-rw
sleep 1
kubectl create -f ./x-sharded-cachedresources/02-apiresourceschema.yaml
sleep 1
kubectl create -f ./x-sharded-cachedresources/03-apiexport.yaml
sleep 1

kubectl create -f ./x-sharded-cachedresources/04-ws-consumer-rw.yaml
sleep 1
kubectl ws consumer-rw
sleep 1
kubectl create -f ./x-sharded-cachedresources/05-apibinding.yaml
sleep 7
kubectl create -f ./x-sharded-cachedresources/06-cowboy.yaml
sleep 1
kubectl create -f ./x-sharded-cachedresources/07-cr-identity.yaml
sleep 1
kubectl create -f ./x-sharded-cachedresources/07-cr-cowboy.yaml
sleep 1
kubectl create -f ./x-sharded-cachedresources/02-apiresourceschema.yaml
sleep 1
kubectl create -f ./x-sharded-cachedresources/08-apiexport.yaml
sleep 10

kubectl create -f ./x-sharded-cachedresources/vr-consumer-ro-root/10-ws-consumer-ro.yaml
sleep 1
kubectl ws consumer-ro-root
sleep 1
kubectl create -f ./x-sharded-cachedresources/vr-consumer-ro-root/11-apibinding.yaml


# kubectl create -f ./x-sharded-cachedresources/10-ws-consumer-ro.yaml
# sleep 1
# kubectl ws consumer-ro
# sleep 1
# kubectl create -f ./x-sharded-cachedresources/11-apibinding.yaml
