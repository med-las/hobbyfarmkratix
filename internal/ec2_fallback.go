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

func HandleEC2Fallback(client dynamic.Interface, name string) {
    reqName := "ec2-" + name
    
    // Check if EC2TrainingVM already exists
    ec2vm, err := client.Resource(ec2TrainingVMGVR).Namespace("default").Get(context.TODO(), reqName, metav1.GetOptions{})
    if err != nil {
        log.Printf("🚀 Creating EC2TrainingVM for %s", name)
        
        // Create new EC2TrainingVM
        newEC2VM := &unstructured.Unstructured{
            Object: map[string]interface{}{
                "apiVersion": "training.example.com/v1",
                "kind":       "EC2TrainingVM",
                "metadata": map[string]interface{}{
                    "name":      reqName,
                    "namespace": "default",
                    "labels": map[string]interface{}{
                        "session": name,
                        "type":    "ec2-fallback",
                    },
                },
                "spec": map[string]interface{}{
                    "user":         name,
                    "session":      name,
                    "instanceType": "t3.micro",
                    "region":       "us-east-1",
                },
            },
        }
        
        _, err = client.Resource(ec2TrainingVMGVR).Namespace("default").Create(context.TODO(), newEC2VM, metav1.CreateOptions{})
        if err != nil {
            log.Printf("❌ Failed to create EC2TrainingVM: %v", err)
        } else {
            log.Printf("✅ Created EC2TrainingVM %s", reqName)
        }
        return
    }

    // Check status of existing EC2TrainingVM
    vmIP, _, _ := unstructured.NestedString(ec2vm.Object, "status", "vmIP")
    state, _, _ := unstructured.NestedString(ec2vm.Object, "status", "state")
    ready, _, _ := unstructured.NestedBool(ec2vm.Object, "status", "ready")
    instanceId, _, _ := unstructured.NestedString(ec2vm.Object, "status", "instanceId")

    log.Printf("🔍 EC2TrainingVM %s status: state=%s, ip=%s, ready=%v, instanceId=%s", reqName, state, vmIP, ready, instanceId)

    // If VM is ready and has IP, update the TrainingVM
    if vmIP != "" && (state == "running" || ready) {
        log.Printf("✅ EC2 VM %s is ready, updating TrainingVM %s", vmIP, name)
        
        // Ensure TrainingVM exists before patching
        _, err := client.Resource(trainingVMGVR).Namespace("default").Get(context.TODO(), name, metav1.GetOptions{})
        if err != nil {
            log.Printf("📦 Creating missing TrainingVM for %s before patching", name)
            newVM := &unstructured.Unstructured{
                Object: map[string]interface{}{
                    "apiVersion": "training.example.com/v1",
                    "kind":       "TrainingVM",
                    "metadata": map[string]interface{}{
                        "name":      name,
                        "namespace": "default",
                        "labels": map[string]interface{}{
                            "vm-type": "ec2",
                        },
                    },
                    "spec": map[string]interface{}{
                        "user":    name,
                        "session": name,
                    },
                },
            }
            _, err = client.Resource(trainingVMGVR).Namespace("default").Create(context.TODO(), newVM, metav1.CreateOptions{})
            if err != nil {
                log.Printf("❌ Failed to create TrainingVM for %s: %v", name, err)
                return
            }
        }

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

        _, err = client.Resource(trainingVMGVR).Namespace("default").Patch(
            context.TODO(), name, types.MergePatchType,
            []byte(patch), metav1.PatchOptions{}, "status",
        )
        if err == nil {
            log.Printf("✅ EC2 VM %s assigned to TrainingVM %s", vmIP, name)
        } else {
            log.Printf("❌ Failed to patch TrainingVM %s: %v", name, err)
        }
    } else {
        log.Printf("⏳ Waiting for EC2 instance for %s (state=%s, ip=%s, ready=%v)", name, state, vmIP, ready)
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
