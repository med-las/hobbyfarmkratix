// internal/kratix_helpers.go - Helper functions for Kratix integration
package internal

import (
    "context"
    "fmt"
    "log"
    "os"
    "strings"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/client-go/dynamic"
)

// List VMProvisioningRequests
func ListVMProvisioningRequests(client dynamic.Interface) []unstructured.Unstructured {
    requests, err := client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("⚠️ Could not list VMProvisioningRequests: %v", err)
        return nil
    }
    
    if len(requests.Items) > 0 {
        log.Printf("🔍 Found %d VMProvisioningRequests", len(requests.Items))
        for _, req := range requests.Items {
            user, _, _ := unstructured.NestedString(req.Object, "spec", "user")
            session, _, _ := unstructured.NestedString(req.Object, "spec", "session")
            state, _, _ := unstructured.NestedString(req.Object, "status", "state")
            log.Printf("  📋 VMProvisioningRequest: %s, User: %s, Session: %s, State: %s", 
                req.GetName(), user, session, state)
        }
    }
    
    return requests.Items
}

// List Kratix Promises
func ListKratixPromises(client dynamic.Interface) []unstructured.Unstructured {
    kratixPromiseGVR := GetKratixPromiseGVR()
    
    promises, err := client.Resource(kratixPromiseGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("⚠️ Could not list Kratix Promises: %v", err)
        return nil
    }
    
    if len(promises.Items) > 0 {
        log.Printf("🔍 Found %d Kratix Promises", len(promises.Items))
        for _, promise := range promises.Items {
            log.Printf("  📋 Promise: %s", promise.GetName())
        }
    }
    
    return promises.Items
}

// Check if Kratix is available in the cluster
func checkKratixAvailability(client dynamic.Interface) bool {
    // Try to list Kratix Promises to check if Kratix is installed
    kratixPromiseGVR := GetKratixPromiseGVR()
    
    _, err := client.Resource(kratixPromiseGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{
        Limit: 1,
    })
    
    if err != nil {
        if strings.Contains(err.Error(), "could not find the requested resource") ||
           strings.Contains(err.Error(), "the server doesn't have a resource type") {
            log.Printf("⚠️ Kratix not available: %v", err)
            return false
        }
        log.Printf("⚠️ Error checking Kratix availability: %v", err)
        return false
    }
    
    log.Printf("✅ Kratix is available in the cluster")
    return true
}

// Get integration mode from environment
func getIntegrationMode() string {
    mode := os.Getenv("INTEGRATION_MODE")
    if mode == "" {
        mode = "hybrid"
    }
    
    validModes := []string{"hobbyfarm-only", "kratix-only", "hybrid"}
    for _, validMode := range validModes {
        if mode == validMode {
            return mode
        }
    }
    
    log.Printf("⚠️ Invalid integration mode: %s, defaulting to hybrid", mode)
    return "hybrid"
}

// Check if a VMProvisioningRequest is created from HobbyFarm
func IsHobbyFarmRequest(request *unstructured.Unstructured) bool {
    labels := request.GetLabels()
    if labels == nil {
        return false
    }
    
    return labels["source"] == "hobbyfarm-integration"
}

// Get HobbyFarm session name from VMProvisioningRequest
func GetHobbyFarmSessionFromRequest(request *unstructured.Unstructured) string {
    labels := request.GetLabels()
    if labels == nil {
        return ""
    }
    
    return labels["hobbyfarm.io/session"]
}

// Get HobbyFarm user from VMProvisioningRequest
func GetHobbyFarmUserFromRequest(request *unstructured.Unstructured) string {
    labels := request.GetLabels()
    if labels == nil {
        return ""
    }
    
    return labels["hobbyfarm.io/user"]
}

// Get HobbyFarm scenario from VMProvisioningRequest
func GetHobbyFarmScenarioFromRequest(request *unstructured.Unstructured) string {
    labels := request.GetLabels()
    if labels == nil {
        return ""
    }
    
    return labels["hobbyfarm.io/scenario"]
}

// Create VMProvisioningRequest from HobbyFarm session
func CreateVMProvisioningRequestFromSession(client dynamic.Interface, session *unstructured.Unstructured) error {
    sessionName := session.GetName()
    user, _, _ := unstructured.NestedString(session.Object, "spec", "user")
    scenario, _, _ := unstructured.NestedString(session.Object, "spec", "scenario")
    
    // Default values
    if user == "" {
        user = "student"
    }
    if scenario == "" {
        scenario = "hybrid-training"
    }
    
    // Get scenario provisioning configuration
    provisioningConfig := getDefaultProvisioningConfig()
    
    // Create VMProvisioningRequest
    kratixRequest := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "platform.kratix.io/v1alpha1",
            "kind":       "VMProvisioningRequest",
            "metadata": map[string]interface{}{
                "name":      sessionName,
                "namespace": "default",
                "labels": map[string]interface{}{
                    "hobbyfarm.io/session":  sessionName,
                    "hobbyfarm.io/user":     user,
                    "hobbyfarm.io/scenario": scenario,
                    "source":                "hobbyfarm-integration",
                },
                "annotations": map[string]interface{}{
                    "hobbyfarm.io/integration": "kratix-promise",
                    "hobbyfarm.io/source":      "session-controller",
                },
            },
            "spec": map[string]interface{}{
                "user":           user,
                "session":        sessionName,
                "scenario":       scenario,
                "vmTemplate":     "hybrid-ubuntu-template",
                "timeout":        600,
                "preferStaticVM": true,
                "provisioning":   provisioningConfig,
                "cloudFallback": map[string]interface{}{
                    "enabled":      true,
                    "provider":     "aws",
                    "instanceType": "t3.micro",
                    "region":       "us-east-1",
                },
            },
        },
    }
    
    _, err := client.Resource(vmProvisioningRequestGVR).Namespace("default").Create(context.TODO(), kratixRequest, metav1.CreateOptions{})
    if err != nil {
        return err
    }
    
    log.Printf("✅ Created VMProvisioningRequest %s from HobbyFarm session", sessionName)
    return nil
}

// Get default provisioning configuration
func getDefaultProvisioningConfig() map[string]interface{} {
    return map[string]interface{}{
        "playbooks":    []string{"base.yaml", "dynamic.yaml"},
        "packages":     []string{},
        "requirements": []string{},
        "variables":    map[string]string{},
    }
}

// Get VMProvisioningRequest status summary
func GetVMProvisioningRequestStatusSummary(client dynamic.Interface) map[string]int {
    requests, err := client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return map[string]int{}
    }
    
    summary := map[string]int{
        "pending":      0,
        "allocated":    0,
        "provisioning": 0,
        "ready":        0,
        "failed":       0,
    }
    
    for _, req := range requests.Items {
        state, _, _ := unstructured.NestedString(req.Object, "status", "state")
        if state == "" {
            state = "pending"
        }
        summary[state]++
    }
    
    return summary
}

// Get cloud provider configuration
func getCloudProviderConfig(provider string) map[string]interface{} {
    switch provider {
    case "aws":
        return map[string]interface{}{
            "instanceType": "t3.micro",
            "region":       "us-east-1",
            "ami":          "ami-0c02fb55956c7d316", // Ubuntu 20.04
        }
    case "azure":
        return map[string]interface{}{
            "vmSize":       "Standard_B1s",
            "location":     "eastus",
            "image":        "ubuntu-20.04",
        }
    case "gcp":
        return map[string]interface{}{
            "machineType": "e2-micro",
            "zone":        "us-central1-a",
            "image":       "ubuntu-2004-lts",
        }
    default:
        return map[string]interface{}{
            "instanceType": "t3.micro",
            "region":       "us-east-1",
        }
    }
}

// Validate VMProvisioningRequest spec
func ValidateVMProvisioningRequest(request *unstructured.Unstructured) error {
    // Check required fields
    user, _, _ := unstructured.NestedString(request.Object, "spec", "user")
    session, _, _ := unstructured.NestedString(request.Object, "spec", "session")
    
    if user == "" {
        return fmt.Errorf("spec.user is required")
    }
    
    if session == "" {
        return fmt.Errorf("spec.session is required")
    }
    
    return nil
}


// Check if IP is in static VM pool
func IsStaticVMIP(ip string) bool {
    for _, staticIP := range vmPool {
        if ip == staticIP {
            return true
        }
    }
    return false
}

// Get available static VMs
func GetAvailableStaticVMs(client dynamic.Interface) []string {
    usedIPs := make(map[string]bool)
    
    // Check TrainingVMs
    trainingVMs, err := client.Resource(trainingVMGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err == nil {
        for _, tvm := range trainingVMs.Items {
            vmIP, _, _ := unstructured.NestedString(tvm.Object, "status", "vmIP")
            state, _, _ := unstructured.NestedString(tvm.Object, "status", "state")
            
            if vmIP != "" && (state == "allocated" || state == "provisioning") {
                usedIPs[vmIP] = true
            }
        }
    }
    
    // Check VMProvisioningRequests
    requests, err := client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err == nil {
        for _, req := range requests.Items {
            vmIP, _, _ := unstructured.NestedString(req.Object, "status", "vmIP")
            state, _, _ := unstructured.NestedString(req.Object, "status", "state")
            
            if vmIP != "" && (state == "allocated" || state == "provisioning" || state == "ready") {
                usedIPs[vmIP] = true
            }
        }
    }
    
    // Find available VMs
    var availableVMs []string
    for _, ip := range vmPool {
        if !usedIPs[ip] && isVMReachable(ip) {
            availableVMs = append(availableVMs, ip)
        }
    }
    
    return availableVMs
}
