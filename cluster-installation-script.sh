#!/bin/bash 

#==============================
echo "Starting Cluster Installation script"
#==============================
# Creating some variables
#=================================

homedir=/home/ec2-user

hostname=$(hostname)

CLUSTER_VERSION=$PICK_A_VERSION$

RELEASE_CHANNEL=$PICK_A_CHANNEL$

RANDOM_VALUE=$RANDOM

export AWS_SHARED_CREDENTIALS_FILE=$homedir/.aws/credentials

Cluster_VPC_id=${cluster_VPC_id}

#===============================================================
# Creating/Building imageset-config.yaml and install-config.yaml
#===============================================================


echo "Create the imageset-config.yaml and install-config.yaml files"

cat <<EOF > "$homedir/mirroring-workspace/imageset-config.yaml"
apiVersion: mirror.openshift.io/v1alpha2
kind: ImageSetConfiguration
storageConfig:
  local:
    path: $homedir/oc-mirror-metadata
mirror:
  platform:
    channels:
      - name: $RELEASE_CHANNEL
        minVersion: $CLUSTER_VERSION
        maxVersion: $CLUSTER_VERSION
EOF

cat <<EOF > "$homedir/cluster/install-config.yaml"
apiVersion: v1
baseDomain: emea.aws.cee.support
credentialsMode: Passthrough
compute:
- architecture: amd64
  hyperthreading: Enabled
  name: worker
  platform: {}
  replicas: 3
controlPlane:
  architecture: amd64
  hyperthreading: Enabled
  name: master
  platform: {}
  replicas: 3
metadata:
  creationTimestamp: null
  name: disconnected-$RANDOM_VALUE
networking:
  clusterNetwork:
  - cidr: 10.128.0.0/14
    hostPrefix: 23
  machineNetwork:
  - cidr: 10.0.0.32/27
  - cidr: 10.0.0.64/27
  - cidr: 10.0.0.96/27
  networkType: $CNI
  serviceNetwork:
  - 172.30.0.0/16
platform:
  aws:
    region: ${region}
    subnets:
    - ${private_subnet_1}
    - ${private_subnet_2}
    - ${private_subnet_3}
publish: Internal
imageContentSources:
  - mirrors:
    - $hostname:8443/openshift/release
    source: quay.io/openshift-release-dev/ocp-v4.0-art-dev
  - mirrors:
    - $hostname:8443/openshift/release-images
    source: quay.io/openshift-release-dev/ocp-release
EOF

echo "Fixing directory permissions"

chown -R ec2-user:ec2-user $homedir/mirroring-workspace/
chown -R ec2-user:ec2-user $homedir/cluster

#=================================
# Creating the install-config.yaml
#=================================

echo "Adding the CA to the install-config.yaml"

echo "additionalTrustBundle:  |" >> $homedir/cluster/install-config.yaml
echo "$(cat $homedir/registry-stuff/quay-rootCA/rootCA.pem)" | sed 's/^/      /' >> $homedir/cluster/install-config.yaml

echo "Creating and adding the public-key to the install-config.yaml"

runuser -u ec2-user -- ssh-keygen -f $homedir/.ssh/cluster_key -t rsa -q -N ""

echo "sshKey: $(cat $homedir/.ssh/cluster_key.pub)" >> $homedir/cluster/install-config.yaml

echo "Adding pull-secret to the install-config.yaml"

echo "pullSecret: '$(cat $homedir/.docker/config.json | jq -c )'" >> $homedir/cluster/install-config.yaml

echo "Cleanup the workspace"

rm $homedir/mirroring-workspace/oc-mirror.tar.gz
rm $homedir/pull-secret.template

#===========================================================
# Let the user know that the mirror registry is ready to use
#===========================================================

echo "Registry is ready to mirror"

cat <<EOF > "$homedir/READY"
The registry was initialized successfully!
EOF

#===========================================================================================================
# Downloading, unpacking installer, oc client and make changes to the manifest prior installing the cluster
#===========================================================================================================

   echo "Starting Cluster deployment preparations"

   echo "Mirroring release images for version $CLUSTER_VERSION"
   cd $homedir/mirroring-workspace/
   runuser -u ec2-user -- ./oc-mirror --config imageset-config.yaml docker://$hostname:8443

   cd $homedir/cluster
   echo "Downloading openshift-installer for version $CLUSTER_VERSION"
   wget https://mirror.openshift.com/pub/openshift-v4/clients/ocp/$CLUSTER_VERSION/openshift-install-linux.tar.gz

   echo "Unpacking openshift-installer"
   tar -xf openshift-install-linux.tar.gz
   rm openshift-install-linux.tar.gz
   mv ./openshift-install /usr/local/bin

   echo "Downloading openshift-client"
   wget https://mirror.openshift.com/pub/openshift-v4/clients/ocp/$CLUSTER_VERSION/openshift-client-linux.tar.gz

   echo "Unpacking openshift-client"
   tar -xf openshift-client-linux.tar.gz
   rm openshift-client-linux.tar.gz
   mv ./oc /usr/local/bin

   echo "Creating manifests"
   cp install-config.yaml install-config.yaml.bak
   runuser -u ec2-user -- openshift-install create manifests --dir ./

   echo "Changing the DNS cluster manifest to avoid ingress operator try to add "*.apps" domain"
   cd $homedir/cluster/manifests
   sed '/baseDomain:/q' cluster-dns-02-config.yml > new-cluster-dns-02-config.yml && mv new-cluster-dns-02-config.yml cluster-dns-02-config.yml
   chown -R ec2-user:ec2-user $homedir/cluster

#============================
# Launch cluster installation
#============================
   echo "Launch cluster installation"
   cd $homedir/cluster
   runuser -u ec2-user -- openshift-install create cluster --dir ./ --log-level=info &

#=============================================
# Adding manually *apps. domain using aws CLI
#=============================================

  echo "Waiting for the LB and the cluster zone to be created so to apply the wildcard "apps." record"

# I found that this is a way to check when the hosted zone is created but i know it is not the best way. I need to improve that in the future.
  while ! grep -q "Waiting up to 40m0s" $homedir/cluster/.openshift_install.log ; do

        echo "LB and zone are not ready yet"
        sleep 120

  done

    echo "LB and zone are ready. Adding manually *apps. domain using aws CLI"

    DOMAIN=disconnected-$RANDOM_VALUE.emea.aws.cee.support
    HOSTED_ZONE_ID=$(aws route53 list-hosted-zones-by-name --dns-name $DOMAIN | jq -r '.HostedZones[0].Id')
    for ((i=0; i<=100; i++)); do
        VPC_id=$(aws elb describe-load-balancers --region ${region} --load-balancer-names | jq -r ".LoadBalancerDescriptions[$i].VPCId")
        if [[ "$VPC_id" == "$Cluster_VPC_id" ]]; then
        ELB_ALIAS_TARGET=$(aws elb describe-load-balancers --region ${region}  --load-balancer-names | jq -r ".LoadBalancerDescriptions[$i].CanonicalHostedZoneNameID")
        ELB_DNS_NAME=$(aws elb describe-load-balancers --region ${region}  --load-balancer-names | jq -r ".LoadBalancerDescriptions[$i].DNSName")
        echo "The domain of the zone is:" $DOMAIN
        echo "The elb hosted zone ID is: $ELB_ALIAS_TARGET" 
        echo "The elb DNS name is: "$ELB_DNS_NAME
        break
        else 
        echo "No LB found in VPC with ID $Cluster_VPC_id"
        fi
    done
    
    aws route53 change-resource-record-sets \
    --hosted-zone-id "$HOSTED_ZONE_ID" \
    --change-batch '{
      "Changes": [
        {
          "Action": "CREATE",
          "ResourceRecordSet": {
            "Name": "*.apps.'$DOMAIN'",
            "Type": "A",
            "AliasTarget": {
              "HostedZoneId": "'$ELB_ALIAS_TARGET'",
              "DNSName": "'$ELB_DNS_NAME'",
              "EvaluateTargetHealth": false
            }
          }
        }
      ]
    }'

#============================================================
# Allow in node SG Groups access from the registry using SSH
#============================================================

echo "Allow SSH access from registry to the cluster nodes"

VPC_ID=$(jq -r '.outputs.vpc_id.value' terraform.cluster.tfstate)

# Master-nodes

SG_GROUP_NAME=$(jq -r '.cluster_id + "-master-sg"' terraform.tfvars.json)
SG_GROUP_ID=$(aws ec2 describe-security-groups --filters Name=vpc-id,Values=$VPC_ID --filters Name=tag:Name,Values=$SG_GROUP_NAME | jq -r '.SecurityGroups[0].GroupId')
aws ec2 authorize-security-group-ingress --group-id $SG_GROUP_ID --protocol tcp --port 22 --cidr 0.0.0.0/0

# Worker_nodes

SG_GROUP_NAME=$(jq -r '.cluster_id + "-worker-sg"' terraform.tfvars.json)
SG_GROUP_ID=$(aws ec2 describe-security-groups --filters Name=vpc-id,Values=$VPC_ID --filters Name=tag:Name,Values=$SG_GROUP_NAME | jq -r '.SecurityGroups[0].GroupId')
aws ec2 authorize-security-group-ingress --group-id $SG_GROUP_ID --protocol tcp --port 22 --cidr 0.0.0.0/0

fi

#=================================
# Disable default Catalog Sources
#=================================

export KUBECONFIG=$homedir/cluster/auth/kubeconfig

oc patch OperatorHub cluster --type json \
    -p '[{"op": "add", "path": "/spec/disableAllDefaultSources", "value": true}]'
