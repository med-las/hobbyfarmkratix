// internal/exports.go - UPDATED VERSION with Kratix Promise GVRs
package internal

import (
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/client-go/dynamic"
)

// Original HobbyFarm GVRs
func GetTrainingVMGVR() schema.GroupVersionResource {
    return trainingVMGVR
}

func GetSessionGVR() schema.GroupVersionResource {
    return sessionGVR
}

func GetVirtualMachineClaimGVR() schema.GroupVersionResource {
    return schema.GroupVersionResource{
        Group:    "hobbyfarm.io",
        Version:  "v1",
        Resource: "virtualmachineclaims",
    }
}

func GetVirtualMachineGVR() schema.GroupVersionResource {
    return schema.GroupVersionResource{
        Group:    "hobbyfarm.io",
        Version:  "v1",
        Resource: "virtualmachines",
    }
}

// NEW: Kratix Promise GVRs
func GetVMProvisioningRequestGVR() schema.GroupVersionResource {
    return schema.GroupVersionResource{
        Group:    "platform.kratix.io",
        Version:  "v1alpha1",
        Resource: "vm-provisioning-requests",
    }
}

func GetKratixPromiseGVR() schema.GroupVersionResource {
    return schema.GroupVersionResource{
        Group:    "platform.kratix.io",
        Version:  "v1alpha1",
        Resource: "promises",
    }
}

// VM Pool and infrastructure
func GetVMPool() []string {
    return vmPool
}

func IsVMReachable(ip string) bool {
    return isVMReachable(ip)
}

// Session and VM management
func ListSessionsExport(client dynamic.Interface) []unstructured.Unstructured {
    return ListSessions(client)
}

func GetExistingTrainingVMsExport(client dynamic.Interface) map[string]bool {
    return GetExistingTrainingVMs(client)
}

// NEW: Kratix-specific exports
func GetVMProvisioningRequests(client dynamic.Interface) []unstructured.Unstructured {
    return ListVMProvisioningRequests(client)
}

func GetKratixPromises(client dynamic.Interface) []unstructured.Unstructured {
    return ListKratixPromises(client)
}

// Helper functions for Kratix integration
func IsKratixAvailable(client dynamic.Interface) bool {
    return checkKratixAvailability(client)
}

func GetIntegrationMode() string {
    return getIntegrationMode()
}

// NEW: Cloud provider GVRs
func GetEC2TrainingVMGVR() schema.GroupVersionResource {
    return ec2TrainingVMGVR
}

func GetAzureTrainingVMGVR() schema.GroupVersionResource {
    return schema.GroupVersionResource{
        Group:    "training.example.com",
        Version:  "v1",
        Resource: "azuretrainingvms",
    }
}

func GetGCPTrainingVMGVR() schema.GroupVersionResource {
    return schema.GroupVersionResource{
        Group:    "training.example.com",
        Version:  "v1",
        Resource: "gcptrainingvms",
    }
}
