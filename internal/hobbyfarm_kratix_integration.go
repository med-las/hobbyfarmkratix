// internal/hobbyfarm_kratix_integration.go - Complete updated file with SSH username fixes
package internal

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/dynamic"
)

type HobbyFarmKratixIntegration struct {
    client             dynamic.Interface
    processedSessions  map[string]bool
    updatedVMs         map[string]bool
    packageDetector    *PackageDetector
}

func NewHobbyFarmKratixIntegration(client dynamic.Interface) *HobbyFarmKratixIntegration {
    return &HobbyFarmKratixIntegration{
        client:            client,
        processedSessions: make(map[string]bool),
        updatedVMs:        make(map[string]bool),
        packageDetector:   NewPackageDetector(client),
    }
}

// Watch HobbyFarm sessions and create Kratix VMProvisioningRequests
func (hki *HobbyFarmKratixIntegration) WatchSessionsForKratix() {
    log.Println("🔗 Starting HobbyFarm → Kratix Integration Controller...")
    log.Println("🎯 Watching HobbyFarm Sessions → Creating Kratix VMProvisioningRequests")
    
    for {
        // Watch for new HobbyFarm sessions
        hki.processHobbyFarmSessions()
        
        // Update HobbyFarm VMs with Kratix results
        hki.updateHobbyFarmVMsFromKratix()
        
        // Cleanup processed sessions and updated VMs
        hki.cleanupProcessedSessions()
        hki.cleanupUpdatedVMs()
        
        time.Sleep(10 * time.Second)
    }
}

// Process HobbyFarm sessions and create corresponding Kratix VMProvisioningRequests
func (hki *HobbyFarmKratixIntegration) processHobbyFarmSessions() {
    sessions, err := hki.client.Resource(sessionGVR).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("⚠️ Could not list HobbyFarm Sessions: %v", err)
        return
    }

    if len(sessions.Items) > 0 {
        log.Printf("🔍 Found %d HobbyFarm Sessions", len(sessions.Items))
    }

    for _, session := range sessions.Items {
        sessionName := session.GetName()
        sessionKey := fmt.Sprintf("hobbyfarm-system/%s", sessionName)
        
        // Skip if already processed
        if hki.processedSessions[sessionKey] {
            continue
        }
        
        // Extract session details
        user, _, _ := unstructured.NestedString(session.Object, "spec", "user")
        scenario, _, _ := unstructured.NestedString(session.Object, "spec", "scenario")
        
        // Use defaults if not specified
        if user == "" {
            user = "student"
        }
        if scenario == "" {
            scenario = "hybrid-training"
        }
        
        log.Printf("🎯 NEW HOBBYFARM SESSION: %s → Creating Kratix VMProvisioningRequest", sessionName)
        
        // Create Kratix VMProvisioningRequest with smart package detection
        if err := hki.createKratixVMRequest(sessionName, user, scenario); err != nil {
            log.Printf("❌ Failed to create Kratix VMProvisioningRequest for session %s: %v", sessionName, err)
            continue
        }
        
        // Mark as processed
        hki.processedSessions[sessionKey] = true
        log.Printf("✅ Created Kratix VMProvisioningRequest for HobbyFarm session %s", sessionName)
    }
}

// Create Kratix VMProvisioningRequest based on HobbyFarm session with smart detection
func (hki *HobbyFarmKratixIntegration) createKratixVMRequest(sessionName, user, scenario string) error {
    // Use smart package detection instead of manual scenario parsing
    log.Printf("🧠 Using smart package detection for session: %s", sessionName)
    
    config := hki.packageDetector.DetectPackagesFromSession(sessionName)
    if config == nil {
        log.Printf("⚠️ Smart detection failed for session %s, using defaults", sessionName)
        config = &ProvisioningConfig{
            Playbooks:    []string{"base.yaml", "dynamic.yaml"},
            Packages:     []string{},
            Requirements: []string{},
            Variables:    map[string]string{},
        }
    }
    
    log.Printf("✅ Smart detection result for session %s:", sessionName)
    log.Printf("  📦 Packages: %v", config.Packages)
    log.Printf("  🔧 Variables: %v", config.Variables)
    log.Printf("  🎭 Playbooks: %v", config.Playbooks)
    
    // Convert ProvisioningConfig to map format for Kratix
    provisioningConfig := map[string]interface{}{
        "playbooks":    config.Playbooks,
        "packages":     config.Packages,
        "requirements": config.Requirements,
        "variables":    config.Variables,
    }
    
    // Create VMProvisioningRequest
    kratixRequest := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "platform.kratix.io/v1alpha1",
            "kind":       "VMProvisioningRequest",
            "metadata": map[string]interface{}{
                "name":      sessionName,
                "namespace": "default",
                "labels": map[string]interface{}{
                    "hobbyfarm.io/session":   sessionName,
                    "hobbyfarm.io/user":      user,
                    "hobbyfarm.io/scenario":  scenario,
                    "source":                 "hobbyfarm-integration",
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
    
    _, err := hki.client.Resource(vmProvisioningRequestGVR).Namespace("default").Create(context.TODO(), kratixRequest, metav1.CreateOptions{})
    if err != nil {
        return fmt.Errorf("failed to create Kratix VMProvisioningRequest: %v", err)
    }
    
    log.Printf("✅ Created Kratix VMProvisioningRequest %s for HobbyFarm session", sessionName)
    return nil
}

// Update HobbyFarm VirtualMachines with results from Kratix VMProvisioningRequests
func (hki *HobbyFarmKratixIntegration) updateHobbyFarmVMsFromKratix() {
    // Get all ready Kratix VMProvisioningRequests
    requests, err := hki.client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }
    
    for _, request := range requests.Items {
        state, _, _ := unstructured.NestedString(request.Object, "status", "state")
        vmIP, _, _ := unstructured.NestedString(request.Object, "status", "vmIP")
        provisioned, _, _ := unstructured.NestedBool(request.Object, "status", "provisioned")
        
        // Only process ready and provisioned VMs
        if state != "ready" || !provisioned || vmIP == "" {
            continue
        }
        
        // Check if this request was created from HobbyFarm
        labels := request.GetLabels()
        if labels == nil || labels["source"] != "hobbyfarm-integration" {
            continue
        }
        
        sessionName := labels["hobbyfarm.io/session"]
        user := labels["hobbyfarm.io/user"]
        
        if sessionName == "" || user == "" {
            continue
        }
        
        // Check if we already updated this VM for this session
        updateKey := fmt.Sprintf("%s-%s", sessionName, vmIP)
        if hki.updatedVMs[updateKey] {
            continue // Already updated, skip to prevent loop
        }
        
        log.Printf("🔄 Updating HobbyFarm VirtualMachine for session %s with Kratix result (IP: %s)", sessionName, vmIP)
        
        // Find corresponding HobbyFarm VirtualMachine
        if err := hki.updateHobbyFarmVirtualMachine(sessionName, user, vmIP); err != nil {
            log.Printf("❌ Failed to update HobbyFarm VirtualMachine for session %s: %v", sessionName, err)
        } else {
            // Mark this VM as updated to prevent future update attempts
            hki.updatedVMs[updateKey] = true
            log.Printf("✅ Marked VM update as complete for session %s", sessionName)
        }
    }
}

// Update HobbyFarm VirtualMachine with Kratix results - UPDATED with SSH username fix
func (hki *HobbyFarmKratixIntegration) updateHobbyFarmVirtualMachine(sessionName, user, vmIP string) error {
    // Check if session still exists
    session, err := hki.client.Resource(sessionGVR).Namespace("hobbyfarm-system").Get(
        context.TODO(), sessionName, metav1.GetOptions{})
    if err != nil {
        log.Printf("⚠️ Session %s no longer exists, skipping VM update", sessionName)
        return nil // Don't treat as error - session was deleted, which is normal
    }
    
    sessionUser, _, _ := unstructured.NestedString(session.Object, "spec", "user")
    
    // Find VirtualMachine that matches this session's user
    virtualMachines, err := hki.client.Resource(virtualMachineGVR).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return err
    }
    
    for _, vm := range virtualMachines.Items {
        vmName := vm.GetName()
        vmUser, _, _ := unstructured.NestedString(vm.Object, "spec", "user")
        currentStatus, _, _ := unstructured.NestedString(vm.Object, "status", "status")
        currentPublicIP, _, _ := unstructured.NestedString(vm.Object, "status", "public_ip")
        
        // Match by user, and either needs provisioning OR is already ready but with different IP
        if vmUser == sessionUser {
            // Case 1: VM needs initial provisioning
            if currentStatus == "readyforprovisioning" && currentPublicIP == "" {
                log.Printf("🎯 Found HobbyFarm VirtualMachine %s needing initial provisioning", vmName)
                return hki.performVMUpdate(vmName, vm, vmIP)
            }
            
            // Case 2: VM is ready but has different IP (unusual but possible)
            if currentStatus == "ready" && currentPublicIP != vmIP {
                log.Printf("🎯 Found HobbyFarm VirtualMachine %s with different IP, updating", vmName)
                return hki.performVMUpdate(vmName, vm, vmIP)
            }
            
            // Case 3: VM is already correctly updated
            if currentStatus == "ready" && currentPublicIP == vmIP {
                log.Printf("✅ HobbyFarm VirtualMachine %s already correctly updated (status: ready, IP: %s)", vmName, vmIP)
                
                // CRITICAL: Check and fix SSH username if wrong
                currentSSHUser, _, _ := unstructured.NestedString(vm.Object, "spec", "ssh_username")
                correctSSHUser := getSSHUsername(vmIP)
                
                if currentSSHUser != correctSSHUser {
                    log.Printf("🔧 FIXING SSH username for existing VM %s: %s -> %s (%s VM)", 
                        vmName, currentSSHUser, correctSSHUser, getVMType(vmIP))
                    
                    specUpdate := map[string]interface{}{
                        "spec": map[string]interface{}{
                            "ssh_username": correctSSHUser,
                        },
                    }
                    
                    if err := hki.patchVirtualMachine(vmName, "", specUpdate); err != nil {
                        log.Printf("❌ Failed to fix SSH username: %v", err)
                    } else {
                        log.Printf("✅ FIXED SSH username for %s", vmName)
                    }
                }
                
                return nil // Already updated correctly, no action needed
            }
        }
    }
    
    log.Printf("⚠️ No matching HobbyFarm VirtualMachine found for session %s (user: %s)", sessionName, sessionUser)
    return nil
}

// Perform the actual VM update - UPDATED with SSH username fix
func (hki *HobbyFarmKratixIntegration) performVMUpdate(vmName string, vm unstructured.Unstructured, vmIP string) error {
    // Get current status and update only necessary fields
    currentStatusObj, exists := vm.Object["status"]
    if !exists {
        log.Printf("❌ No status found in VirtualMachine %s", vmName)
        return fmt.Errorf("no status found in VirtualMachine %s", vmName)
    }
    
    statusMap, ok := currentStatusObj.(map[string]interface{})
    if !ok {
        log.Printf("❌ Status is not a map in VirtualMachine %s", vmName)
        return fmt.Errorf("status is not a map in VirtualMachine %s", vmName)
    }
    
    // Update only the fields we need to change, keep all existing fields
    statusMap["status"] = "ready"
    statusMap["public_ip"] = vmIP
    statusMap["private_ip"] = vmIP
    statusMap["hostname"] = vmIP
    // All other fields (allocated, environment_id, tainted, ws_endpoint) remain unchanged
    
    statusUpdate := map[string]interface{}{
        "status": statusMap,
    }
    
    // CRITICAL: Get correct SSH username based on VM IP type
    correctSSHUser := getSSHUsername(vmIP)
    log.Printf("🔧 Using correct SSH username: %s for %s VM (IP: %s)", correctSSHUser, getVMType(vmIP), vmIP)
    
    // Update spec with SSH credentials and CORRECT SSH username
    specUpdate := map[string]interface{}{
        "spec": map[string]interface{}{
            "secret_name":  "hobbyfarm-vm-ssh-key",
            "ssh_username": correctSSHUser, // Use utility function for correct username
        },
    }
    
    // Update ready label with VM type
    labelUpdate := map[string]interface{}{
        "metadata": map[string]interface{}{
            "labels": map[string]interface{}{
                "ready":   "true",
                "vm-type": getVMType(vmIP),
            },
        },
    }
    
    // Apply updates with proper error handling
    if err := hki.patchVirtualMachine(vmName, "", specUpdate); err != nil {
        log.Printf("⚠️ Failed to update VM spec: %v", err)
    } else {
        log.Printf("✅ Updated VM spec with correct SSH credentials: ssh_username=%s", correctSSHUser)
    }
    
    if err := hki.patchVirtualMachine(vmName, "status", statusUpdate); err != nil {
        log.Printf("❌ Failed to update VM status: %v", err)
        // Try alternative approach - patch the whole object
        wholeUpdate := map[string]interface{}{
            "spec": map[string]interface{}{
                "secret_name":  "hobbyfarm-vm-ssh-key",
                "ssh_username": correctSSHUser,
            },
            "status": statusMap,
        }
        
        if err2 := hki.patchVirtualMachine(vmName, "", wholeUpdate); err2 != nil {
            log.Printf("❌ Failed whole VM update: %v", err2)
            return fmt.Errorf("failed to update VM: %v", err)
        } else {
            log.Printf("✅ Updated VM with alternative method")
        }
    } else {
        log.Printf("✅ Updated VM status: ready, IP=%s", vmIP)
    }
    
    if err := hki.patchVirtualMachine(vmName, "", labelUpdate); err != nil {
        log.Printf("⚠️ Failed to update VM labels: %v", err)
    } else {
        log.Printf("✅ Updated VM labels: ready=true, vm-type=%s", getVMType(vmIP))
    }
    
    log.Printf("✅ Updated HobbyFarm VirtualMachine %s with Kratix result: IP=%s, SSH user=%s (%s VM)", 
        vmName, vmIP, correctSSHUser, getVMType(vmIP))
    return nil
}

// Helper function to patch VirtualMachine
func (hki *HobbyFarmKratixIntegration) patchVirtualMachine(vmName, subresource string, update map[string]interface{}) error {
    patchBytes, err := json.Marshal(update)
    if err != nil {
        return err
    }
    
    var patchOptions metav1.PatchOptions
    if subresource != "" {
        _, err = hki.client.Resource(virtualMachineGVR).Namespace("hobbyfarm-system").Patch(
            context.TODO(), vmName, types.MergePatchType,
            patchBytes, patchOptions, subresource)
    } else {
        _, err = hki.client.Resource(virtualMachineGVR).Namespace("hobbyfarm-system").Patch(
            context.TODO(), vmName, types.MergePatchType,
            patchBytes, patchOptions)
    }
    
    return err
}

// Cleanup processed sessions
func (hki *HobbyFarmKratixIntegration) cleanupProcessedSessions() {
    // Get active sessions
    activeSessions := make(map[string]bool)
    
    sessions, err := hki.client.Resource(sessionGVR).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err == nil {
        for _, session := range sessions.Items {
            sessionKey := fmt.Sprintf("hobbyfarm-system/%s", session.GetName())
            activeSessions[sessionKey] = true
        }
    }
    
    // Remove processed sessions that no longer exist
    for sessionKey := range hki.processedSessions {
        if !activeSessions[sessionKey] {
            delete(hki.processedSessions, sessionKey)
        }
    }
}

// Cleanup updated VMs tracker
func (hki *HobbyFarmKratixIntegration) cleanupUpdatedVMs() {
    // Get active VMProvisioningRequests
    activeRequests := make(map[string]bool)
    
    requests, err := hki.client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err == nil {
        for _, request := range requests.Items {
            requestName := request.GetName()
            vmIP, _, _ := unstructured.NestedString(request.Object, "status", "vmIP")
            if vmIP != "" {
                updateKey := fmt.Sprintf("%s-%s", requestName, vmIP)
                activeRequests[updateKey] = true
            }
        }
    }
    
    // Remove tracked updates for requests that no longer exist
    for updateKey := range hki.updatedVMs {
        if !activeRequests[updateKey] {
            delete(hki.updatedVMs, updateKey)
        }
    }
}

// Additional helper functions
func (hki *HobbyFarmKratixIntegration) GetProcessedSessionsCount() int {
    return len(hki.processedSessions)
}

func (hki *HobbyFarmKratixIntegration) IsSessionProcessed(sessionName string) bool {
    sessionKey := fmt.Sprintf("hobbyfarm-system/%s", sessionName)
    return hki.processedSessions[sessionKey]
}

func (hki *HobbyFarmKratixIntegration) GetUpdatedVMsCount() int {
    return len(hki.updatedVMs)
}
