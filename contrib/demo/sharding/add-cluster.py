#!/usr/bin/env python

import sys
import yaml

if len(sys.argv) != 3:
    print("Usage: {} <donor> <recipient>".format(sys.argv[0]))
    sys.exit(1)

print("Extracting cluster from {}".format(sys.argv[1]))
with open(sys.argv[1]) as raw_donor:
    donor = yaml.load(raw_donor, yaml.FullLoader)

foundDonor = False
for cluster in donor["clusters"]:
    if cluster["name"] == "user":
        foundDonor = True
        clusterName = cluster["cluster"]["server"]
        clusterName = clusterName[clusterName.index("clusters/") + 9:]
if not foundDonor:
    print("Did not find cluster 'user' in donor")
    sys.exit(1)

print("Donating cluster to {}".format(sys.argv[2]))
with open(sys.argv[2], "r") as raw_recipient:
    recipient = yaml.load(raw_recipient, yaml.FullLoader)
    foundCluster = False
    for cluster in recipient["clusters"]:
        if cluster["name"] == "user":
            foundCluster = True
            copiedCluster = yaml.load(yaml.dump(cluster), yaml.FullLoader)
            copiedServer = copiedCluster["cluster"]["server"]
            copiedServer = copiedServer[:copiedServer.index("clusters/") + 9] + clusterName
            copiedCluster["cluster"]["server"] = copiedServer
            copiedCluster["name"] = "other"
            recipient["clusters"].append(copiedCluster)
    if not foundCluster:
        print("Did not find cluster 'user' in recipient")
        sys.exit(1)

    foundContext = False
    for context in recipient["contexts"]:
        if context["name"] == "user":
            foundContext = True
            copiedContext = yaml.load(yaml.dump(context), yaml.FullLoader)
            copiedContext["name"] = "other"
            copiedContext["context"]["cluster"] = "other"
            recipient["contexts"].append(copiedContext)
    if not foundContext:
        print("Did not find context 'user' in recipient")
        sys.exit(1)

with open(sys.argv[2], "w") as raw_recipient:
    yaml.dump(recipient, raw_recipient)
