// internal/hobbyfarm_controller.go - COMPLETE VERSION with SSH credentials fix
package internal

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "strings"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/dynamic"
)

var (
    // HobbyFarm VirtualMachine GVR - Created by HobbyFarm's VMClaim controller
    virtualMachineGVR = schema.GroupVersionResource{
        Group:    "hobbyfarm.io",
        Version:  "v1",
        Resource: "virtualmachines",
    }
    
    // HobbyFarm VirtualMachineClaim GVR - Created by Session controller
    virtualMachineClaimGVR = schema.GroupVersionResource{
        Group:    "hobbyfarm.io",
        Version:  "v1",
        Resource: "virtualmachineclaims",
    }
)

type HobbyFarmController struct {
    client        dynamic.Interface
    ansibleRunner *AnsibleRunner
    
    // Track sessions we've already processed
    processedSessions map[string]bool
}

func NewHobbyFarmController(client dynamic.Interface) *HobbyFarmController {
    return &HobbyFarmController{
        client:            client,
        ansibleRunner:     NewAnsibleRunner(client),
        processedSessions: make(map[string]bool),
    }
}

// MAIN ENTRY POINT: Watch for Sessions (what HobbyFarm actually creates)
func (hfc *HobbyFarmController) WatchHobbyFarmVMs() {
    log.Println("🎓 Starting HobbyFarm Session-based Controller...")
    log.Println("🎯 PRIMARY: Watching for new Sessions in hobbyfarm-system namespace")
    log.Println("🎯 INTEGRATION: Creating TrainingVMs for provisioning")
    log.Println("🎯 STATUS: Updating HobbyFarm VirtualMachine status")
    log.Println("🚫 DISABLED: Dual session creation prevention active")
    
    for {
        // PRIMARY: Watch for new Sessions (what triggers everything)
        hfc.watchSessions()
        
        // STATUS UPDATE: Update HobbyFarm VirtualMachine status when TrainingVMs are ready
        hfc.updateHobbyFarmVMStatus()
        
        time.Sleep(10 * time.Second)
    }
}

// PRIMARY: Watch for NEW Sessions being created - FIXED to prevent dual sessions
func (hfc *HobbyFarmController) watchSessions() {
    // ONLY watch hobbyfarm-system namespace to prevent dual session creation
    sessions, err := hfc.client.Resource(sessionGVR).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("⚠️ Could not list Sessions in namespace hobbyfarm-system: %v", err)
        return
    }

    if len(sessions.Items) > 0 {
        log.Printf("🔍 Found %d Sessions in namespace hobbyfarm-system", len(sessions.Items))
    }

    newSessions := 0
    for _, session := range sessions.Items {
        sessionName := session.GetName()
        sessionKey := fmt.Sprintf("hobbyfarm-system/%s", sessionName)
        
        // Skip if we've already processed this session
        if hfc.processedSessions[sessionKey] {
            continue
        }
        
        // Process new session
        if err := hfc.processNewSession(&session, "hobbyfarm-system"); err != nil {
            log.Printf("❌ Failed to process new Session %s in hobbyfarm-system: %v", sessionName, err)
        } else {
            // Mark as processed
            hfc.processedSessions[sessionKey] = true
            newSessions++
        }
    }
    
    if newSessions > 0 {
        log.Printf("🎉 Processed %d new Sessions", newSessions)
    }
}

// Process a NEW Session from HobbyFarm - ONLY creates TrainingVMs, no duplicate sessions
func (hfc *HobbyFarmController) processNewSession(session *unstructured.Unstructured, sessionNamespace string) error {
    sessionName := session.GetName()
    
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
    
    log.Printf("🎯 NEW SESSION DETECTED: %s (namespace: %s, user: %s, scenario: %s)", sessionName, sessionNamespace, user, scenario)
    
    // ONLY create TrainingVM - DO NOT create duplicate sessions
    log.Printf("📝 HobbyFarm session detected - creating TrainingVM directly without duplicating session")
    
    // Create TrainingVM for this session (always in default namespace)
    trainingVMName := sessionName
    if err := hfc.ensureTrainingVMExists(trainingVMName, user, sessionName, scenario); err != nil {
        return fmt.Errorf("failed to create TrainingVM: %v", err)
    }
    
    log.Printf("✅ HobbyFarm session %s is now ready for VM provisioning", sessionName)
    return nil
}

// NEW: Update HobbyFarm VirtualMachine status when TrainingVM is ready
func (hfc *HobbyFarmController) updateHobbyFarmVMStatus() {
    // Get all TrainingVMs
    trainingVMs, err := hfc.client.Resource(trainingVMGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }
    
    // Check each TrainingVM
    for _, tvm := range trainingVMs.Items {
        tvmName := tvm.GetName()
        tvmIP, _, _ := unstructured.NestedString(tvm.Object, "status", "vmIP")
        tvmState, _, _ := unstructured.NestedString(tvm.Object, "status", "state")
        tvmProvisioned, _, _ := unstructured.NestedBool(tvm.Object, "status", "provisioned")
        
        // Only update if TrainingVM is allocated and provisioned
        if tvmState == "allocated" && tvmProvisioned && tvmIP != "" {
            log.Printf("🔄 TrainingVM %s is ready (IP: %s), updating HobbyFarm VirtualMachine...", tvmName, tvmIP)
            
            // Find corresponding HobbyFarm VirtualMachine
            err = hfc.updateCorrespondingVirtualMachine(tvmName, tvmIP)
            if err != nil {
                log.Printf("❌ Failed to update VirtualMachine for %s: %v", tvmName, err)
            }
        }
    }
}

// Update the corresponding HobbyFarm VirtualMachine - ENHANCED with SSH credentials
func (hfc *HobbyFarmController) updateCorrespondingVirtualMachine(sessionName, vmIP string) error {
    // Get the session to extract user information
    session, err := hfc.client.Resource(sessionGVR).Namespace("hobbyfarm-system").Get(
        context.TODO(), sessionName, metav1.GetOptions{})
    if err != nil {
        log.Printf("❌ Failed to get session %s: %v", sessionName, err)
        return err
    }
    
    sessionUser, _, _ := unstructured.NestedString(session.Object, "spec", "user")
    log.Printf("🔍 Looking for VirtualMachine for session %s (user: %s)", sessionName, sessionUser)
    
    // Try to find VirtualMachine that matches this session's user
    virtualMachines, err := hfc.client.Resource(virtualMachineGVR).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return err
    }
    
    for _, vm := range virtualMachines.Items {
        vmName := vm.GetName()
        
        // Check VirtualMachine user
        vmUser, _, _ := unstructured.NestedString(vm.Object, "spec", "user")
        currentStatus, _, _ := unstructured.NestedString(vm.Object, "status", "status")
        currentPublicIP, _, _ := unstructured.NestedString(vm.Object, "status", "public_ip")
        
        log.Printf("🔍 Checking VirtualMachine %s: user=%s, status=%s, IP=%s", vmName, vmUser, currentStatus, currentPublicIP)
        
        // Match by user AND status (must be readyforprovisioning and no IP assigned)
        if vmUser == sessionUser && currentStatus == "readyforprovisioning" && currentPublicIP == "" {
            log.Printf("🎯 Found matching VirtualMachine %s for session %s (user: %s)", vmName, sessionName, sessionUser)
            
            log.Printf("🔄 Updating VirtualMachine %s with IP %s", vmName, vmIP)
            
            // ENHANCED: Update status with proper ws_endpoint
            statusUpdate := map[string]interface{}{
                "status":      "ready",
                "public_ip":   vmIP,
                "private_ip":  vmIP,
                "hostname":    vmIP,
                "allocated":   true,
                "ws_endpoint": "ws://shell.192.168.2.47.nip.io", // Force ws:// not wss://
            }
            
            // ENHANCED: Update spec with SSH credentials
            specUpdate := map[string]interface{}{
                "secret_name":  "hobbyfarm-vm-ssh-key",
                "ssh_username": "kube",
            }
            
            // Update ready label to true
            labelUpdate := map[string]interface{}{
                "metadata": map[string]interface{}{
                    "labels": map[string]interface{}{
                        "ready": "true",
                    },
                },
            }
            
            // 1. Update spec with SSH credentials
            specBytes, err := json.Marshal(map[string]interface{}{"spec": specUpdate})
            if err == nil {
                _, err = hfc.client.Resource(virtualMachineGVR).Namespace("hobbyfarm-system").Patch(
                    context.TODO(), vmName, types.MergePatchType,
                    specBytes, metav1.PatchOptions{},
                )
                if err != nil {
                    log.Printf("⚠️ Failed to update VM spec with SSH credentials: %v", err)
                } else {
                    log.Printf("✅ Updated VM spec with SSH credentials")
                }
            }
            
            // 2. Update status
            statusBytes, err := json.Marshal(map[string]interface{}{"status": statusUpdate})
            if err != nil {
                return err
            }
            
            _, err = hfc.client.Resource(virtualMachineGVR).Namespace("hobbyfarm-system").Patch(
                context.TODO(), vmName, types.MergePatchType,
                statusBytes, metav1.PatchOptions{}, "status",
            )
            if err != nil {
                return fmt.Errorf("failed to update status: %v", err)
            }
            
            // 3. Update labels
            labelBytes, err := json.Marshal(labelUpdate)
            if err != nil {
                return err
            }
            
            _, err = hfc.client.Resource(virtualMachineGVR).Namespace("hobbyfarm-system").Patch(
                context.TODO(), vmName, types.MergePatchType,
                labelBytes, metav1.PatchOptions{},
            )
            if err != nil {
                return fmt.Errorf("failed to update labels: %v", err)
            }
            
            log.Printf("✅ Updated HobbyFarm VirtualMachine %s: status=ready, IP=%s, SSH configured", vmName, vmIP)
            return nil
        }
    }
    
    log.Printf("⚠️ No matching VirtualMachine found for session %s (user: %s)", sessionName, sessionUser)
    return nil
}

// Ensure TrainingVM exists for session (always in default namespace)
func (hfc *HobbyFarmController) ensureTrainingVMExists(name, user, session, scenario string) error {
    // Check if TrainingVM already exists
    existingVM, err := hfc.client.Resource(trainingVMGVR).Namespace("default").Get(context.TODO(), name, metav1.GetOptions{})
    if err == nil {
        // TrainingVM exists, check if it has status
        vmIP, _, _ := unstructured.NestedString(existingVM.Object, "status", "vmIP")
        state, _, _ := unstructured.NestedString(existingVM.Object, "status", "state")
        log.Printf("🔍 TrainingVM %s already exists - IP: %s, State: %s", name, vmIP, state)
        return nil // Already exists
    }

    log.Printf("📦 Creating TrainingVM %s for session %s", name, session)

    // Get provisioning config from scenario
    annotations := hfc.getProvisioningAnnotationsForScenario(scenario)

    newVM := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "training.example.com/v1",
            "kind":       "TrainingVM",
            "metadata": map[string]interface{}{
                "name":        name,
                "namespace":   "default", // Always create TrainingVMs in default namespace
                "annotations": annotations,
                "labels": map[string]interface{}{
                    "hobbyfarm.io/session":  session,
                    "hobbyfarm.io/user":     user,
                    "hobbyfarm.io/scenario": scenario,
                    "provisioner":           "hobbyfarm-hybrid",
                    "created-by":            "hybrid-provisioner",
                },
            },
            "spec": map[string]interface{}{
                "user":    user,
                "session": session,
            },
        },
    }

    _, err = hfc.client.Resource(trainingVMGVR).Namespace("default").Create(context.TODO(), newVM, metav1.CreateOptions{})
    if err != nil {
        return fmt.Errorf("failed to create TrainingVM: %v", err)
    }
    
    log.Printf("✅ Created TrainingVM %s - ready for allocation", name)
    return nil
}

// Get provisioning annotations from scenario
func (hfc *HobbyFarmController) getProvisioningAnnotationsForScenario(scenario string) map[string]interface{} {
    annotations := make(map[string]interface{})
    
    if scenario == "" {
        annotations["provisioning.hobbyfarm.io/playbooks"] = "base.yaml,dynamic.yaml"
        annotations["hobbyfarm.io/integration"] = "hybrid-provisioner"
        return annotations
    }

    // Try to get scenario configuration from both namespaces
    namespaces := []string{"default", "hobbyfarm-system"}
    var scenarioObj *unstructured.Unstructured
    var err error
    
    for _, ns := range namespaces {
        scenarioObj, err = hfc.client.Resource(scenarioGVR).Namespace(ns).Get(
            context.TODO(), scenario, metav1.GetOptions{})
        if err == nil {
            log.Printf("🔍 Found scenario %s in namespace %s", scenario, ns)
            break
        }
    }
    
    if err != nil {
        log.Printf("⚠️ Could not get scenario %s, using defaults", scenario)
        annotations["provisioning.hobbyfarm.io/playbooks"] = "base.yaml,dynamic.yaml"
        annotations["hobbyfarm.io/integration"] = "hybrid-provisioner"
        return annotations
    }

    scenarioAnnotations := scenarioObj.GetAnnotations()
    if scenarioAnnotations != nil {
        // Copy provisioning annotations from scenario
        for key, value := range scenarioAnnotations {
            if strings.HasPrefix(key, "provisioning.hobbyfarm.io/") {
                annotations[key] = value
            }
        }
    }
    
    // Ensure we have at least default playbooks
    if _, exists := annotations["provisioning.hobbyfarm.io/playbooks"]; !exists {
        annotations["provisioning.hobbyfarm.io/playbooks"] = "base.yaml,dynamic.yaml"
    }
    
    annotations["hobbyfarm.io/scenario"] = scenario
    annotations["hobbyfarm.io/integration"] = "hybrid-provisioner"

    return annotations
}

// Cleanup old sessions and resources
func (hfc *HobbyFarmController) CleanupReleasedVMs() {
    log.Println("🧹 Running HobbyFarm resource cleanup...")
    
    // Clean up processed sessions map (keep only active sessions from hobbyfarm-system)
    activeSessions := make(map[string]bool)
    
    sessions, err := hfc.client.Resource(sessionGVR).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err == nil {
        for _, session := range sessions.Items {
            sessionKey := fmt.Sprintf("hobbyfarm-system/%s", session.GetName())
            activeSessions[sessionKey] = true
        }
    }
    
    // Remove processed sessions that no longer exist
    for sessionKey := range hfc.processedSessions {
        if !activeSessions[sessionKey] {
            delete(hfc.processedSessions, sessionKey)
        }
    }
    
    log.Printf("🧹 Cleaned up processed sessions map, tracking %d active sessions", len(hfc.processedSessions))
}
