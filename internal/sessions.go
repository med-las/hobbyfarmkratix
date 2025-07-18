// internal/sessions.go - FINAL FIX: Remove CreateMissingTrainingVMs function completely
package internal

import (
    "context"
    "log"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/client-go/dynamic"
)

func ListSessions(client dynamic.Interface) []unstructured.Unstructured {
    // Only check default namespace to avoid confusion with dual session creation
    sessions, err := client.Resource(sessionGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("⚠️ Could not list Sessions in default namespace: %v", err)
        return nil
    }
    
    if len(sessions.Items) > 0 {
        log.Printf("🧠 Found %d sessions in default namespace", len(sessions.Items))
        
        // Debug: Show session details
        for _, session := range sessions.Items {
            user, _, _ := unstructured.NestedString(session.Object, "spec", "user")
            scenario, _, _ := unstructured.NestedString(session.Object, "spec", "scenario")
            log.Printf("  📋 Session: %s, User: %s, Scenario: %s", session.GetName(), user, scenario)
        }
    }
    
    return sessions.Items
}

func GetExistingTrainingVMs(client dynamic.Interface) map[string]bool {
    trainingVMs, err := client.Resource(trainingVMGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("⚠️ Could not list TrainingVMs: %v", err)
        return make(map[string]bool)
    }
    
    existing := make(map[string]bool)
    for _, tvm := range trainingVMs.Items {
        existing[tvm.GetName()] = true
        
        // Debug: log existing TrainingVMs
        name := tvm.GetName()
        vmIP, _, _ := unstructured.NestedString(tvm.Object, "status", "vmIP")
        state, _, _ := unstructured.NestedString(tvm.Object, "status", "state")
        log.Printf("🔍 Existing TrainingVM: %s, IP: %s, State: %s", name, vmIP, state)
    }
    
    log.Printf("🔍 Found %d existing TrainingVMs", len(existing))
    return existing
}

// REMOVED: CreateMissingTrainingVMs function
// This is now handled ONLY by the HobbyFarmController to avoid duplication
