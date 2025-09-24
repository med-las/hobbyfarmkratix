// internal/kratix_controller.go - COMPLETE VERSION with smart package detection
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
    packageDetector        *PackageDetector
}

func NewKratixController(client dynamic.Interface) *KratixController {
    return &KratixController{
        client:            client,
        ansibleRunner:     NewAnsibleRunner(client),
        processedRequests: make(map[string]bool),
        staticVMPool:      []string{"192.168.2.37", "192.168.2.38"},
        usedIPs:          make(map[string]bool),
        packageDetector:   NewPackageDetector(client),
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
        
        // Run provisioning with smart package detection
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

// Run Ansible provisioning based on request configuration with smart detection
func (kc *KratixController) runProvisioning(vmIP, session, scenario string, request *unstructured.Unstructured) error {
    // Use smart package detection instead of manual config extraction
    log.Printf("🧠 Using smart package detection for Kratix provisioning (session: %s)", session)
    
    config := kc.packageDetector.DetectPackagesFromSession(session)
    if config == nil {
        log.Printf("⚠️ Smart detection failed, using default config")
        config = &ProvisioningConfig{
            Playbooks:    []string{"base.yaml", "dynamic.yaml"},
            Packages:     []string{},
            Requirements: []string{},
            Variables:    map[string]string{},
        }
    }
    
    log.Printf("🎯 Smart detection result for Kratix: playbooks=%v, packages=%v", config.Playbooks, config.Packages)
    
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
    session, _, _ := unstructured.NestedString(request.Object, "spec", "session")
    
    log.Printf("🚀 Creating direct Instance for request %s", requestName)

    // Create Instance directly (not XEC2TrainingVM)
    instanceName := "training-" + session
    instanceGVR := schema.GroupVersionResource{
        Group:    "ec2.aws.upbound.io",
        Version:  "v1beta1",
        Resource: "instances",
    }
    
    // Check if Instance already exists
    existingInstance, err := kc.client.Resource(instanceGVR).Get(context.TODO(), instanceName, metav1.GetOptions{})
    if err == nil {
        publicIP, _, _ := unstructured.NestedString(existingInstance.Object, "status", "atProvider", "publicIp")
        instanceState, _, _ := unstructured.NestedString(existingInstance.Object, "status", "atProvider", "instanceState")
        
        if publicIP != "" && instanceState == "running" {
            log.Printf("✅ Instance %s ready: IP=%s", instanceName, publicIP)
            kc.updateRequestStatus(requestName, "allocated", publicIP, "ec2", false)
            return nil
        }
        return nil // Still starting
    }
    
    // Create new Instance directly
    newInstance := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "ec2.aws.upbound.io/v1beta1",
            "kind":       "Instance",
            "metadata": map[string]interface{}{
                "name": instanceName,
                "labels": map[string]interface{}{
                    "kratix-request": requestName,
                    "session":        session,
                },
            },
            "spec": map[string]interface{}{
                "forProvider": map[string]interface{}{
                    "ami":                        "ami-0360c520857e3138f",
                    "instanceType":               "t3.micro",
                    "region":                     "us-east-1",
                    "subnetId":                   "subnet-09418e7f533840cde",
                    "vpcSecurityGroupIds":        []string{"sg-0bfde988b4d5f8110"},
                    "keyName":                    "my-hobbyfarm-key",
                    "associatePublicIpAddress":   true,
                    "tags": map[string]interface{}{
                        "Name":    "hobbyfarm-" + session,
                        "Session": session,
                    },
                },
                "providerConfigRef": map[string]interface{}{
                    "name": "default",
                },
            },
        },
    }
    
    _, err = kc.client.Resource(instanceGVR).Create(context.TODO(), newInstance, metav1.CreateOptions{})
    if err != nil {
        return fmt.Errorf("failed to create Instance: %v", err)
    }
    
    log.Printf("✅ Created direct Instance %s", instanceName)
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
