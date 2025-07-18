// internal/enhanced_vm_allocator.go - FINAL FIX: Remove all session/TrainingVM creation
package internal

import (
    "log"
    "k8s.io/client-go/dynamic"
)

type EnhancedVMAllocator struct {
    client        dynamic.Interface
    ansibleRunner *AnsibleRunner
}

func NewEnhancedVMAllocator(client dynamic.Interface) *EnhancedVMAllocator {
    return &EnhancedVMAllocator{
        client:        client,
        ansibleRunner: NewAnsibleRunner(client),
    }
}

func (eva *EnhancedVMAllocator) AllocateTrainingVMs() {
    log.Println("🔄 Enhanced VM Allocator: Starting allocation cycle...")
    
    // ONLY do allocation - NO TrainingVM creation
    // TrainingVM creation is handled ONLY by HobbyFarmController
    usedIPs := CleanupVMStatuses(eva.client)
    AllocateTrainingVMs(eva.client, usedIPs, eva.ansibleRunner)
    
    log.Println("🔄 Enhanced VM Allocator: Allocation cycle complete")
}
