// internal/hobbyfarm_controller_enhanced.go - FIXED VERSION: Remove unused imports and add missing method
package internal

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "strings"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/types"
)

// Additional function to handle the VM claim mismatch
func (hfc *HobbyFarmController) updateVirtualMachineStatusesEnhanced() {
    // Get all sessions first to understand the expected VM claims
    sessions, err := hfc.client.Resource(sessionGVR).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("❌ Failed to list sessions: %v", err)
        return
    }
    
    // Build a map of session -> expected VM claim
    sessionToVMClaim := make(map[string]string)
    for _, session := range sessions.Items {
        sessionName := session.GetName()
        
        // Extract vm_claim from session
        vmClaims, found, _ := unstructured.NestedSlice(session.Object, "spec", "vm_claim")
        if found && len(vmClaims) > 0 {
            if claim, ok := vmClaims[0].(map[string]interface{}); ok {
                if claimID, ok := claim["id"].(string); ok {
                    sessionToVMClaim[sessionName] = claimID
                    log.Printf("🔗 Session %s expects VM claim %s", sessionName, claimID)
                }
            }
        }
    }
    
    // Get ready TrainingVMs
    trainingVMs, err := hfc.client.Resource(trainingVMGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }
    
    // For each ready TrainingVM, update the corresponding VirtualMachine
    for i := range trainingVMs.Items {
        tvm := &trainingVMs.Items[i]
        tvmName := tvm.GetName()
        tvmIP, _, _ := unstructured.NestedString(tvm.Object, "status", "vmIP")
        tvmState, _, _ := unstructured.NestedString(tvm.Object, "status", "state")
        tvmProvisioned, _, _ := unstructured.NestedBool(tvm.Object, "status", "provisioned")
        
        // Only process fully ready TrainingVMs
        if tvmState != "allocated" || !tvmProvisioned || tvmIP == "" {
            continue
        }
        
        log.Printf("🔄 Processing ready TrainingVM %s (IP: %s)", tvmName, tvmIP)
        
        // If this TrainingVM corresponds to a session, find the expected VM
        if expectedVMClaim, exists := sessionToVMClaim[tvmName]; exists {
            log.Printf("🎯 Session %s expects VM from claim %s", tvmName, expectedVMClaim)
            
            // Find all VMs that belong to this claim
            vms, _ := hfc.client.Resource(virtualMachineGVR).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{
                LabelSelector: fmt.Sprintf("vmc=%s", expectedVMClaim),
            })
            
            if len(vms.Items) > 0 {
                // Update the first available VM from this claim
                for _, vm := range vms.Items {
                    vmName := vm.GetName()
                    currentStatus, _, _ := unstructured.NestedString(vm.Object, "status", "status")
                    currentIP, _, _ := unstructured.NestedString(vm.Object, "status", "public_ip")
                    
                    // Only update if not already updated
                    if currentStatus != "ready" || currentIP == "" {
                        log.Printf("🔄 Updating VirtualMachine %s with IP %s for session %s", vmName, tvmIP, tvmName)
                        if hfc.updateVMStatus(vmName, "hobbyfarm-system", tvmIP) {
                            log.Printf("✅ Updated VirtualMachine %s for session %s", vmName, tvmName)
                            break
                        }
                    }
                }
            } else {
                log.Printf("⚠️ No VirtualMachine found for claim %s", expectedVMClaim)
            }
        } else {
            // Fallback to original logic for VMs without session mapping
            hfc.updateVirtualMachinesByDirectMatch(tvm)
        }
    }
}

// Original logic as fallback
func (hfc *HobbyFarmController) updateVirtualMachinesByDirectMatch(tvm *unstructured.Unstructured) {
    tvmName := tvm.GetName()
    tvmIP, _, _ := unstructured.NestedString(tvm.Object, "status", "vmIP")
    
    // Try to find a VM with matching name
    vms, _ := hfc.client.Resource(virtualMachineGVR).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    
    for _, vm := range vms.Items {
        vmName := vm.GetName()
        
        // Check various matching strategies
        if vmName == tvmName || strings.Contains(vmName, tvmName) || strings.Contains(tvmName, vmName) {
            currentIP, _, _ := unstructured.NestedString(vm.Object, "status", "public_ip")
            if currentIP == "" {
                log.Printf("🔄 Updating VirtualMachine %s with IP %s (direct match)", vmName, tvmIP)
                hfc.updateVMStatus(vmName, "hobbyfarm-system", tvmIP)
            }
        }
    }
}

// Add the missing updateVMStatus method
func (hfc *HobbyFarmController) updateVMStatus(vmName, namespace, vmIP string) bool {
    // Update the VirtualMachine status
    statusUpdate := map[string]interface{}{
        "status":     "ready",
        "public_ip":  vmIP,
        "private_ip": vmIP,
        "hostname":   vmIP,
    }
    
    // Update ready label to true
    labelUpdate := map[string]interface{}{
        "metadata": map[string]interface{}{
            "labels": map[string]interface{}{
                "ready": "true",
            },
        },
    }
    
    // Patch status
    statusBytes, err := json.Marshal(map[string]interface{}{"status": statusUpdate})
    if err != nil {
        log.Printf("❌ Failed to marshal status update: %v", err)
        return false
    }
    
    _, err = hfc.client.Resource(virtualMachineGVR).Namespace(namespace).Patch(
        context.TODO(), vmName, types.MergePatchType,
        statusBytes, metav1.PatchOptions{}, "status",
    )
    if err != nil {
        log.Printf("❌ Failed to update VM status: %v", err)
        return false
    }
    
    // Patch labels
    labelBytes, err := json.Marshal(labelUpdate)
    if err != nil {
        log.Printf("❌ Failed to marshal label update: %v", err)
        return false
    }
    
    _, err = hfc.client.Resource(virtualMachineGVR).Namespace(namespace).Patch(
        context.TODO(), vmName, types.MergePatchType,
        labelBytes, metav1.PatchOptions{},
    )
    if err != nil {
        log.Printf("❌ Failed to update VM labels: %v", err)
        return false
    }
    
    log.Printf("✅ Successfully updated VirtualMachine %s: status=ready, IP=%s", vmName, vmIP)
    return true
}

// Replace the original updateVirtualMachineStatuses with this enhanced version
func (hfc *HobbyFarmController) updateVirtualMachineStatuses() {
    hfc.updateVirtualMachineStatusesEnhanced()
}
