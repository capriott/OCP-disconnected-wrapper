package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var agentStatus *InfraState
var infraDetailsStatus *InfraDetails
var agentAction *DeployDestroy

func init() {
	agentStatus = &InfraState{}
	infraDetailsStatus = &InfraDetails{}
	agentAction = &DeployDestroy{}
}

//==========================================================================================
// Client type of functions
//==========================================================================================

type DeployDestroy struct {
	ClusterVersion string
	Deploy         string
}

type InfraState struct {
	RegistryHealth string
	ClusterStatus  string
}

// Function to get the client status using HTTP. It expects a reply from the server-agent.
func ClientGetStatus(url string) bool {

	fmt.Println("Status Get Request...")

	resp, err := http.Get("http://" + url + ":8090/status")
	if err != nil {
		log.Println("Error making GET request:", err)
		fmt.Println("")
		agentIsDown(err)
		os.Exit(2)
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Println("Error reading response body:", err)
		time.Sleep(5 * time.Second)
		fmt.Println("")
		agentIsDown(err)
		os.Exit(2)
	}

	if err := json.Unmarshal(body, agentStatus); err != nil {
		fmt.Printf("error Unmarshaling JSON: %v\n", err)
		time.Sleep(5 * time.Second)
		fmt.Println("")
		agentIsDown(err)
		os.Exit(2)
	}

	//fmt.Println("InfraState: ", agentStatus)

	//Check the status of the deployment
	if agentStatus.RegistryHealth == "Healthy" && agentStatus.ClusterStatus == "DontExist" {
		fmt.Println("Registry is Healthy but cluster does not exist")
		return true
	} else if agentStatus.RegistryHealth == "Healthy" && agentStatus.ClusterStatus == "Exists" {
		fmt.Println("Registry is Healthy and there is a cluster installation in place.")
		return true
	} else if agentStatus.RegistryHealth == "Unhealthy" {
		fmt.Println("The mirror registry is not healthy")
		return true
	}

	return false
}

func agentIsDown(clientError error) {

	registryExists, clusterExists := checkDeploymentState()

	fmt.Printf("--> The agent is unavailable with error %v.\n", clientError)
	if registryExists && clusterExists {
		fmt.Println("--> Login to the EC2 instance and see if there is a cluster provisioned under /home/ec2-user/cluster install dir and destroy it manually, as agent is unavailable to avoid leaving orphan resources running on AWS")
		fmt.Println("--> To destroy the cluster run 'openshift-install destroy cluster --dir /home/ec2-user/cluster' command on the EC2 instance")
	} else if registryExists && !clusterExists {
		fmt.Println("--> There is a provisioned mirror-registry EC2 instance. Use --force flag along with the --destroy flag to destroy it along with the rest of the infrastructure")
	}
}

func sendInstallConfigToAgent(installconfig string, url string) {

	// Send the JSON data to the server
	fmt.Println("Sending the installConfig using Post request")
	resp, err := http.Post("http://"+url+":8090/data", "application/json", bytes.NewBuffer([]byte(installconfig)))
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	defer resp.Body.Close()

	// Print response from server
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	log.Println("Response from server:", string(body))
}

func sendActionAndVersionToAgent(url string) {

	actionForAgent, err := json.Marshal(agentAction)
	if err != nil {
		// If there is an error in marshaling print the error
		fmt.Println("Error marshaling actionForAgent to JSON:", err)
		return
	}
	// Send the JSON data to the server
	fmt.Println("Sending actionForAgent using Post request")
	req, err := http.NewRequest("POST", "http://"+url+":8090/action", bytes.NewBuffer(actionForAgent))
	if err != nil {
		fmt.Println("Error creating actionForAgent request:", err)
		return
	}
	// Send the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("Error sending request actionForAgent:", err)
		return
	}
	defer resp.Body.Close()

	// Check the response
	if resp.StatusCode == http.StatusOK {
		fmt.Println("ActionForAgent Data sent successfully!")
	} else {
		fmt.Println("Failed to send data actionForAgent. Status code:", resp.StatusCode)
	}
}

func populateActionAndVersion(action bool, version string) {

	if action && len(version) > 0 {
		agentAction.Deploy = "Install"
		agentAction.ClusterVersion = version
	} else if !action && len(version) == 0 {
		agentAction.Deploy = "Destroy"
		agentAction.ClusterVersion = "N/A"
	} else {
		fmt.Println("Invalid input. Do nothing.")
		os.Exit(4)
	}

}

//======================================================================================
//Check the status of the deployment
//======================================================================================

func applyTerraformConfig() {

	//======================================================================================
	//The below is the deployment of the Terraform part of the infrastructure.
	//======================================================================================

	// Read the contents of the Terraform template file
	fmt.Println("Updating .tfvars file with cluster flag")
	templateContent, err := os.ReadFile("terraform.tfvars")
	if err != nil {
		fmt.Println("Cannot read template file")
		return
	}

	// Set the cluster flag to true and create the terraform.tfvars file
	replacedClusterFlag := strings.ReplaceAll(string(templateContent), "false", "true")
	err = os.WriteFile("terraform.tfvars", []byte(replacedClusterFlag), 0644)
	if err != nil {
		fmt.Println("Cannot write the Terraform config file")
		return
	}

	cmd := exec.Command("terraform", "apply", "-target=module.Cluster_Dependencies", "-auto-approve")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	error := cmd.Run()
	if error != nil {
		fmt.Printf("Terraform apply failed with: %v", error)
	}

	// Wait some seconds for terraform to get applied
	time.Sleep(5 * time.Second)

}

func populateInstallConfigValues(sdnFlag bool, installConfigFlag bool) string {

	fmt.Println("Populating the install-config.yaml with the required infrastructure details")
	var installConfig string
	var cni string

	// If the installConfig flag is appended then read the custom-install-config.yaml file provided by the user and populate this instead the default.
	if installConfigFlag {
		fmt.Println("Custom install-config.yaml detected. Populating it with the required infrastructure details")
		customInstallconfig, err := os.ReadFile("./install-config.yaml")
		if err != nil {
			println("Cannot read the pull-secret")
		}

		var data map[string]interface{}
		err = yaml.Unmarshal(customInstallconfig, &data)
		if err != nil {
			log.Fatalf("Error unmarshaling YAML: %v", err)
		}

		// Marshal the map into JSON
		installConfigJson, err := json.Marshal(data)
		if err != nil {
			log.Fatalf("Error marshaling to JSON: %v", err)
		}

		// Step 4: Convert JSON bytes to string
		installConfig = string(installConfigJson)

	} else if !installConfigFlag {
		installConfig = `{"apiVersion":"v1","baseDomain":"emea.aws.cee.support","credentialsMode":"Passthrough","compute":[{"architecture":"amd64","hyperthreading":"Enabled","name":"worker","platform":{},"replicas":0}],"controlPlane":{"architecture":"amd64","hyperthreading":"Enabled","name":"master","platform":{},"replicas":1},"metadata":{"name":"disconnected-$RANDOM_VALUE"},"networking":{"clusterNetwork":[{"cidr":"10.128.0.0/14","hostPrefix":23}],"machineNetwork":[{"cidr":"10.0.0.32/27"},{"cidr":"10.0.0.64/27"},{"cidr":"10.0.0.96/27"}],"networkType":"$CNI","serviceNetwork":["172.30.0.0/16"]},"platform":{"aws":{"region":"${region}","subnets":["${private_subnet_1}","${private_subnet_2}","${private_subnet_3}"]}},"publish":"Internal","imageContentSources":[{"mirrors":["$hostname:8443/openshift/release"],"source":"quay.io/openshift-release-dev/ocp-v4.0-art-dev"},{"mirrors":["$hostname:8443/openshift/release-images"],"source":"quay.io/openshift-release-dev/ocp-release"}]}`
	}
	randomValue := rand.Intn(99999-10000+1) + 10000

	randomValueStr := fmt.Sprintf("%d", randomValue)

	if sdnFlag {
		cni = "OpenShiftSDN"
	} else {
		cni = "OVNKubernetes"
	}

	randomName := strings.ReplaceAll(installConfig, "$RANDOM_VALUE", randomValueStr)
	region := strings.ReplaceAll(randomName, "${region}", infraDetailsStatus.AWSRegion)
	subnet_1 := strings.ReplaceAll(region, "${private_subnet_1}", infraDetailsStatus.PrivateSubnet1)
	subnet_2 := strings.ReplaceAll(subnet_1, "${private_subnet_2}", infraDetailsStatus.PrivateSubnet2)
	subnet_3 := strings.ReplaceAll(subnet_2, "${private_subnet_3}", infraDetailsStatus.PrivateSubnet3)
	changeCNI := strings.ReplaceAll(subnet_3, "$CNI", cni)
	changePrivateHostname := strings.ReplaceAll(changeCNI, "$hostname", infraDetailsStatus.PrivateDNS)

	fmt.Println("Install-config populated")

	return changePrivateHostname

}
