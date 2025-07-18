// internal/kratix_controller.go - Kratix Promise integration controller
package internal

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "os"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/dynamic"
)

var (
    // Kratix Promise VMProvisioningRequest GVR
    vmProvisioningRequestGVR = schema.GroupVersionResource{
        Group:    "platform.kratix.io",
        Version:  "v1alpha1",
        Resource: "vm-provisioning-requests",
    }
)

type KratixController struct {
    client                   dynamic.Interface
    ansibleRunner           *AnsibleRunner
    processedRequests       map[string]bool
    staticVMPool           []string
    usedIPs                map[string]bool
}

func NewKratixController(client dynamic.Interface) *KratixController {
    return &KratixController{
        client:            client,
        ansibleRunner:     NewAnsibleRunner(client),
        processedRequests: make(map[string]bool),
        staticVMPool:      []string{"192.168.2.37", "192.168.2.38"},
        usedIPs:          make(map[string]bool),
    }
}

// Main controller loop for Kratix Promise VMProvisioningRequests
func (kc *KratixController) WatchVMProvisioningRequests() {
    log.Println("🎯 Starting Kratix Promise VM Provisioning Controller...")
    log.Println("🔄 Watching for VMProvisioningRequests")
    
    for {
        // Watch for new VMProvisioningRequests
        kc.processVMProvisioningRequests()
        
        // Allocate VMs for pending requests
        kc.allocateVMs()
        
        // Update status for provisioned VMs
        kc.updateVMStatus()
        
        // Cleanup expired allocations
        kc.cleanupExpiredAllocations()
        
        time.Sleep(10 * time.Second)
    }
}

// Process new VMProvisioningRequests
func (kc *KratixController) processVMProvisioningRequests() {
    requests, err := kc.client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("⚠️ Could not list VMProvisioningRequests: %v", err)
        return
    }

    if len(requests.Items) > 0 {
        log.Printf("🔍 Found %d VMProvisioningRequests", len(requests.Items))
    }

    for _, request := range requests.Items {
        requestName := request.GetName()
        
        // Skip if already processed
        if kc.processedRequests[requestName] {
            continue
        }
        
        // Get request details
        user, _, _ := unstructured.NestedString(request.Object, "spec", "user")
        session, _, _ := unstructured.NestedString(request.Object, "spec", "session")
        scenario, _, _ := unstructured.NestedString(request.Object, "spec", "scenario")
        state, _, _ := unstructured.NestedString(request.Object, "status", "state")
        
        log.Printf("🎯 Processing VMProvisioningRequest: %s (user: %s, session: %s, scenario: %s, state: %s)", 
            requestName, user, session, scenario, state)
        
        // Initialize status if not set
        if state == "" {
            if err := kc.updateRequestStatus(requestName, "pending", "", "", false); err != nil {
                log.Printf("❌ Failed to initialize request status: %v", err)
                continue
            }
        }
        
        // Mark as processed
        kc.processedRequests[requestName] = true
        log.Printf("✅ VMProvisioningRequest %s processed", requestName)
    }
}

// Allocate VMs for pending requests
func (kc *KratixController) allocateVMs() {
    // Refresh used IPs
    kc.refreshUsedIPs()
    
    requests, err := kc.client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }

    for _, request := range requests.Items {
        requestName := request.GetName()
        state, _, _ := unstructured.NestedString(request.Object, "status", "state")
        vmIP, _, _ := unstructured.NestedString(request.Object, "status", "vmIP")
        
        // Skip if not pending or already has IP
        if state != "pending" || vmIP != "" {
            continue
        }
        
        log.Printf("🔄 Allocating VM for request: %s", requestName)
        
        // Try to allocate from static pool first
        if selectedIP := kc.findAvailableStaticVM(); selectedIP != "" {
            log.Printf("✅ Allocating static VM %s to request %s", selectedIP, requestName)
            
            if err := kc.updateRequestStatus(requestName, "allocated", selectedIP, "static", false); err != nil {
                log.Printf("❌ Failed to allocate static VM: %v", err)
                continue
            }
            
            kc.usedIPs[selectedIP] = true
            
            // Set allocated timestamp
            kc.setAllocatedAt(requestName)
            
        } else {
            // Check if cloud fallback is enabled
            fallbackEnabled, _, _ := unstructured.NestedBool(request.Object, "spec", "cloudFallback", "enabled")
            
            if fallbackEnabled {
                log.Printf("🚀 No static VMs available, trying cloud fallback for %s", requestName)
                if err := kc.handleCloudFallback(requestName, &request); err != nil {
                    log.Printf("❌ Cloud fallback failed for %s: %v", requestName, err)
                    kc.updateRequestStatus(requestName, "failed", "", "", false)
                }
            } else {
                log.Printf("⚠️ No VMs available for %s and cloud fallback disabled", requestName)
            }
        }
    }
}

// Update VM status and run provisioning
func (kc *KratixController) updateVMStatus() {
    requests, err := kc.client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }

    for _, request := range requests.Items {
        requestName := request.GetName()
        state, _, _ := unstructured.NestedString(request.Object, "status", "state")
        vmIP, _, _ := unstructured.NestedString(request.Object, "status", "vmIP")
        provisioned, _, _ := unstructured.NestedBool(request.Object, "status", "provisioned")
        
        // Skip if not allocated or already provisioned
        if state != "allocated" || vmIP == "" || provisioned {
            continue
        }
        
        // Check if VM is reachable
        if !isVMReachable(vmIP) {
            log.Printf("⚠️ VM %s not reachable, will retry", vmIP)
            continue
        }
        
        // Check boot wait time
        allocatedAt, _, _ := unstructured.NestedString(request.Object, "status", "allocatedAt")
        if allocatedAt != "" {
            if t, err := time.Parse(time.RFC3339, allocatedAt); err == nil {
                bootWaitTime := getBootWaitTime(vmIP)
                if time.Since(t) < bootWaitTime {
                    log.Printf("⏳ Waiting for VM %s to boot (%v remaining)", vmIP, bootWaitTime-time.Since(t))
                    continue
                }
            }
        }
        
        // Update status to provisioning
        kc.updateRequestStatus(requestName, "provisioning", vmIP, "", false)
        
        // Run Ansible provisioning
        session, _, _ := unstructured.NestedString(request.Object, "spec", "session")
        scenario, _, _ := unstructured.NestedString(request.Object, "spec", "scenario")
        
        log.Printf("🎭 Starting provisioning for VM %s (request: %s)", vmIP, requestName)
        
        // Wait for SSH
        sshTimeout := getSSHTimeout(vmIP)
        if err := kc.ansibleRunner.WaitForSSH(vmIP, sshTimeout); err != nil {
            log.Printf("❌ SSH not ready for VM %s: %v", vmIP, err)
            kc.updateRequestStatus(requestName, "failed", vmIP, "", false)
            continue
        }
        
        // Run provisioning
        if err := kc.runProvisioning(vmIP, session, scenario, &request); err != nil {
            log.Printf("❌ Provisioning failed for VM %s: %v", vmIP, err)
            kc.updateRequestStatus(requestName, "failed", vmIP, "", false)
            continue
        }
        
        // Mark as ready
        kc.updateRequestStatus(requestName, "ready", vmIP, "", true)
        kc.setReadyAt(requestName)
        
        log.Printf("✅ VM %s provisioned successfully for request %s", vmIP, requestName)
    }
}

// Run Ansible provisioning based on request configuration
func (kc *KratixController) runProvisioning(vmIP, session, scenario string, request *unstructured.Unstructured) error {
    // Get provisioning config from request
    playbooks, _, _ := unstructured.NestedStringSlice(request.Object, "spec", "provisioning", "playbooks")
    packages, _, _ := unstructured.NestedStringSlice(request.Object, "spec", "provisioning", "packages")
    requirements, _, _ := unstructured.NestedStringSlice(request.Object, "spec", "provisioning", "requirements")
    variables, _, _ := unstructured.NestedStringMap(request.Object, "spec", "provisioning", "variables")
    
    // Default playbooks if not specified
    if len(playbooks) == 0 {
        playbooks = []string{"base.yaml", "dynamic.yaml"}
    }
    
    log.Printf("🎯 Provisioning config: playbooks=%v, packages=%v, requirements=%v", playbooks, packages, requirements)
    
    // Create provisioning config
    config := &ProvisioningConfig{
        Playbooks:    playbooks,
        Packages:     packages,
        Requirements: requirements,
        Variables:    variables,
    }
    
    // Detect SSH user
    sshUser, err := kc.ansibleRunner.detectSSHUser(vmIP)
    if err != nil {
        return fmt.Errorf("failed to detect SSH user: %v", err)
    }
    
    // Build inventory
    inventoryContent := kc.ansibleRunner.buildInventory(vmIP, sshUser, session, config)
    
    // Write temporary inventory
    tmpInventory := fmt.Sprintf("/tmp/kratix_inventory_%s", session)
    if err := kc.writeFile(tmpInventory, inventoryContent); err != nil {
        return fmt.Errorf("failed to write inventory: %v", err)
    }
    defer kc.removeFile(tmpInventory)
    
    // Run playbooks
    for _, playbook := range config.Playbooks {
        log.Printf("🎭 Running playbook %s for session %s", playbook, session)
        if err := kc.ansibleRunner.runSinglePlaybook(tmpInventory, playbook, session, config); err != nil {
            return fmt.Errorf("playbook %s failed: %v", playbook, err)
        }
    }
    
    return nil
}

// Helper functions
func (kc *KratixController) findAvailableStaticVM() string {
    for _, ip := range kc.staticVMPool {
        if !kc.usedIPs[ip] && isVMReachable(ip) {
            return ip
        }
    }
    return ""
}

func (kc *KratixController) refreshUsedIPs() {
    kc.usedIPs = make(map[string]bool)
    
    requests, err := kc.client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }
    
    for _, request := range requests.Items {
        vmIP, _, _ := unstructured.NestedString(request.Object, "status", "vmIP")
        state, _, _ := unstructured.NestedString(request.Object, "status", "state")
        
        if vmIP != "" && (state == "allocated" || state == "provisioning" || state == "ready") {
            kc.usedIPs[vmIP] = true
        }
    }
}

func (kc *KratixController) updateRequestStatus(requestName, state, vmIP, vmType string, provisioned bool) error {
    status := map[string]interface{}{
        "state": state,
        "provisioned": provisioned,
    }
    
    if vmIP != "" {
        status["vmIP"] = vmIP
    }
    
    if vmType != "" {
        status["vmType"] = vmType
    }
    
    patch := map[string]interface{}{
        "status": status,
    }
    
    patchBytes, err := json.Marshal(patch)
    if err != nil {
        return err
    }
    
    _, err = kc.client.Resource(vmProvisioningRequestGVR).Namespace("default").Patch(
        context.TODO(), requestName, types.MergePatchType,
        patchBytes, metav1.PatchOptions{}, "status")
    
    return err
}

func (kc *KratixController) setAllocatedAt(requestName string) {
    patch := map[string]interface{}{
        "status": map[string]interface{}{
            "allocatedAt": time.Now().Format(time.RFC3339),
        },
    }
    
    patchBytes, _ := json.Marshal(patch)
    kc.client.Resource(vmProvisioningRequestGVR).Namespace("default").Patch(
        context.TODO(), requestName, types.MergePatchType,
        patchBytes, metav1.PatchOptions{}, "status")
}

func (kc *KratixController) setReadyAt(requestName string) {
    patch := map[string]interface{}{
        "status": map[string]interface{}{
            "readyAt": time.Now().Format(time.RFC3339),
        },
    }
    
    patchBytes, _ := json.Marshal(patch)
    kc.client.Resource(vmProvisioningRequestGVR).Namespace("default").Patch(
        context.TODO(), requestName, types.MergePatchType,
        patchBytes, metav1.PatchOptions{}, "status")
}

func (kc *KratixController) handleCloudFallback(requestName string, request *unstructured.Unstructured) error {
    // Extract cloud config
    provider, _, _ := unstructured.NestedString(request.Object, "spec", "cloudFallback", "provider")
    instanceType, _, _ := unstructured.NestedString(request.Object, "spec", "cloudFallback", "instanceType")
    region, _, _ := unstructured.NestedString(request.Object, "spec", "cloudFallback", "region")
    
    // Default values
    if provider == "" {
        provider = "aws"
    }
    if instanceType == "" {
        instanceType = "t3.micro"
    }
    if region == "" {
        region = "us-east-1"
    }
    
    log.Printf("🚀 Creating cloud instance: provider=%s, type=%s, region=%s", provider, instanceType, region)
    
    // Create cloud instance (reuse existing EC2 fallback logic)
    user, _, _ := unstructured.NestedString(request.Object, "spec", "user")
    session, _, _ := unstructured.NestedString(request.Object, "spec", "session")
    
    // Create EC2TrainingVM for cloud fallback
    return kc.createCloudInstance(requestName, user, session, provider, instanceType, region)
}

func (kc *KratixController) createCloudInstance(requestName, user, session, provider, instanceType, region string) error {
    // For now, only support AWS via existing EC2 fallback
    if provider != "aws" {
        return fmt.Errorf("unsupported cloud provider: %s", provider)
    }
    
    // Create EC2TrainingVM
    reqName := "kratix-" + requestName
    newEC2VM := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "training.example.com/v1",
            "kind":       "EC2TrainingVM",
            "metadata": map[string]interface{}{
                "name":      reqName,
                "namespace": "default",
                "labels": map[string]interface{}{
                    "kratix-request": requestName,
                    "session":        session,
                    "type":           "kratix-cloud-fallback",
                },
            },
            "spec": map[string]interface{}{
                "user":         user,
                "session":      session,
                "instanceType": instanceType,
                "region":       region,
            },
        },
    }
    
    _, err := kc.client.Resource(ec2TrainingVMGVR).Namespace("default").Create(context.TODO(), newEC2VM, metav1.CreateOptions{})
    if err != nil {
        return fmt.Errorf("failed to create EC2TrainingVM: %v", err)
    }
    
    log.Printf("✅ Created EC2TrainingVM %s for Kratix request %s", reqName, requestName)
    return nil
}

func (kc *KratixController) cleanupExpiredAllocations() {
    requests, err := kc.client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }
    
    for _, request := range requests.Items {
        requestName := request.GetName()
        state, _, _ := unstructured.NestedString(request.Object, "status", "state")
        allocatedAt, _, _ := unstructured.NestedString(request.Object, "status", "allocatedAt")
        
        // Clean up expired allocations
        if state == "allocated" && allocatedAt != "" {
            if t, err := time.Parse(time.RFC3339, allocatedAt); err == nil {
                if time.Since(t) > 1*time.Hour {
                    log.Printf("🧹 Cleaning up expired allocation for request %s", requestName)
                    kc.updateRequestStatus(requestName, "failed", "", "", false)
                }
            }
        }
        
        // Clean up processed requests that no longer exist
        if state == "failed" || state == "released" {
            if t, err := time.Parse(time.RFC3339, allocatedAt); err == nil {
                if time.Since(t) > 24*time.Hour {
                    delete(kc.processedRequests, requestName)
                }
            }
        }
    }
}

// File operations helpers
func (kc *KratixController) writeFile(path, content string) error {
    return os.WriteFile(path, []byte(content), 0644)
}

func (kc *KratixController) removeFile(path string) {
    os.Remove(path)
}

// Monitor cloud instances and update request status
func (kc *KratixController) monitorCloudInstances() {
    ec2vms, err := kc.client.Resource(ec2TrainingVMGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }
    
    for _, ec2vm := range ec2vms.Items {
        labels := ec2vm.GetLabels()
        if labels == nil {
            continue
        }
        
        kratixRequest := labels["kratix-request"]
        if kratixRequest == "" {
            continue
        }
        
        vmIP, _, _ := unstructured.NestedString(ec2vm.Object, "status", "vmIP")
        state, _, _ := unstructured.NestedString(ec2vm.Object, "status", "state")
        ready, _, _ := unstructured.NestedBool(ec2vm.Object, "status", "ready")
        instanceId, _, _ := unstructured.NestedString(ec2vm.Object, "status", "instanceId")
        
        // If EC2 instance is ready, update the VMProvisioningRequest
        if vmIP != "" && (state == "running" || ready) {
            log.Printf("✅ EC2 instance %s ready for Kratix request %s", vmIP, kratixRequest)
            kc.updateRequestStatus(kratixRequest, "allocated", vmIP, "ec2", false)
            
            // Update instance ID in status
            patch := map[string]interface{}{
                "status": map[string]interface{}{
                    "instanceId": instanceId,
                },
            }
            patchBytes, _ := json.Marshal(patch)
            kc.client.Resource(vmProvisioningRequestGVR).Namespace("default").Patch(
                context.TODO(), kratixRequest, types.MergePatchType,
                patchBytes, metav1.PatchOptions{}, "status")
        }
    }
}

// Add cloud monitoring to the main loop
func (kc *KratixController) WatchVMProvisioningRequestsWithCloudMonitoring() {
    log.Println("🎯 Starting Kratix Promise VM Provisioning Controller with Cloud Monitoring...")
    
    for {
        kc.processVMProvisioningRequests()
        kc.allocateVMs()
        kc.monitorCloudInstances()  // Monitor cloud instances
        kc.updateVMStatus()
        kc.cleanupExpiredAllocations()
        
        time.Sleep(10 * time.Second)
    }
}
