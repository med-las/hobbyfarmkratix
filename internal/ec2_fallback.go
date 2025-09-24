// internal/ec2_fallback.go - UPDATED VERSION for Crossplane
package internal

import (
    "context"
    "fmt"
    "log"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/dynamic"
)

// Updated GVR for the new EC2TrainingVM
var (
    ec2TrainingVMGVR = schema.GroupVersionResource{
        Group:    "training.example.com",
        Version:  "v1",
        Resource: "ec2trainingvms",
    }
)

// In ec2_fallback.go - replace HandleEC2Fallback function
func HandleEC2Fallback(client dynamic.Interface, name string) {
    instanceName := "training-" + name
    
    // Check if Instance already exists
    instanceGVR := schema.GroupVersionResource{
        Group:    "ec2.aws.upbound.io",
        Version:  "v1beta1",
        Resource: "instances",
    }
    
    existingInstance, err := client.Resource(instanceGVR).Get(context.TODO(), instanceName, metav1.GetOptions{})
    if err == nil {
        // Instance exists, check its status
        publicIP, _, _ := unstructured.NestedString(existingInstance.Object, "status", "atProvider", "publicIp")
        instanceState, _, _ := unstructured.NestedString(existingInstance.Object, "status", "atProvider", "instanceState")
        
        log.Printf("✅ Instance %s exists: IP=%s, State=%s", instanceName, publicIP, instanceState)
        
        if publicIP != "" && instanceState == "running" {
            // Update TrainingVM with the IP
            updateTrainingVMWithEC2(client, name, publicIP, instanceName)
        }
        return
    }

    log.Printf("🚀 Creating direct EC2 Instance for %s", name)
    
    // Create Instance directly (like your working test)
    newInstance := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "ec2.aws.upbound.io/v1beta1",
            "kind":       "Instance",
            "metadata": map[string]interface{}{
                "name": instanceName,
                "labels": map[string]interface{}{
                    "session": name,
                    "type":    "training-vm",
                },
            },
            "spec": map[string]interface{}{
                "forProvider": map[string]interface{}{
                    "ami":                        "ami-0c02fb55956c7d316",
                    "instanceType":               "t3.micro",
                    "region":                     "us-east-1",
                    "subnetId":                   "subnet-09418e7f533840cde",
                    "vpcSecurityGroupIds":        []string{"sg-0bfde988b4d5f8110"},
                    "keyName":                    "hobbyfarm-keypair",
                    "associatePublicIpAddress":   true,
                    "tags": map[string]interface{}{
                        "Name":    "hobbyfarm-training-" + name,
                        "Session": name,
                        "Purpose": "training",
                    },
                },
                "providerConfigRef": map[string]interface{}{
                    "name": "default",
                },
            },
        },
    }
    
    _, err = client.Resource(instanceGVR).Create(context.TODO(), newInstance, metav1.CreateOptions{})
    if err != nil {
        log.Printf("❌ Failed to create Instance: %v", err)
    } else {
        log.Printf("✅ Created direct Instance %s", instanceName)
    }
}

func updateTrainingVMWithEC2(client dynamic.Interface, name, vmIP, instanceId string) {
    // Update TrainingVM with EC2 instance details
    patch := fmt.Sprintf(`{
      "status": {
        "vmIP": "%s",
        "state": "allocated",
        "allocatedAt": "%s",
        "vmType": "ec2",
        "instanceId": "%s"
      }
    }`, vmIP, time.Now().Format(time.RFC3339), instanceId)

    _, err := client.Resource(trainingVMGVR).Namespace("default").Patch(
        context.TODO(), name, types.MergePatchType,
        []byte(patch), metav1.PatchOptions{}, "status",
    )
    if err != nil {
        log.Printf("❌ Failed to patch TrainingVM %s: %v", name, err)
    } else {
        log.Printf("✅ EC2 Instance %s (%s) assigned to TrainingVM %s", instanceId, vmIP, name)
    }
}

// Helper function to check EC2 status and clean up failed instances
func CleanupFailedEC2Instances(client dynamic.Interface) {
    ec2vms, err := client.Resource(ec2TrainingVMGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }

    for _, ec2vm := range ec2vms.Items {
        name := ec2vm.GetName()
        state, _, _ := unstructured.NestedString(ec2vm.Object, "status", "state")
        creationTime := ec2vm.GetCreationTimestamp()
        
        // Clean up instances that have been in failed state for too long
        if (state == "terminated" || state == "failed") && time.Since(creationTime.Time) > 5*time.Minute {
            log.Printf("🧹 Cleaning up failed EC2TrainingVM %s (state: %s)", name, state)
            err := client.Resource(ec2TrainingVMGVR).Namespace("default").Delete(
                context.TODO(), name, metav1.DeleteOptions{})
            if err != nil {
                log.Printf("❌ Failed to delete failed EC2TrainingVM %s: %v", name, err)
            }
        }
        
        // Clean up instances that are taking too long to start
        if state == "pending" && time.Since(creationTime.Time) > 10*time.Minute {
            log.Printf("🧹 Cleaning up stuck EC2TrainingVM %s (pending too long)", name)
            err := client.Resource(ec2TrainingVMGVR).Namespace("default").Delete(
                context.TODO(), name, metav1.DeleteOptions{})
            if err != nil {
                log.Printf("❌ Failed to delete stuck EC2TrainingVM %s: %v", name, err)
            }
        }
    }
}
