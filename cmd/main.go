// cmd/main.go - Complete updated file with SSH username fix integration
package main

import (
    "log"
    "os"
    "time"
    "context"
    "os/signal"
    "syscall"
    "strings"
    "encoding/json"
    "hobbyfarm-vm-provisioner/internal"
    
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/dynamic"
)

func main() {
    log.Println("🎓 Starting HobbyFarm Hybrid VM Provisioner with Kratix Integration v3.1...")
    log.Println("🔧 Enhanced with automatic SSH username detection and fixing")
    
    // Initialize Kubernetes client
    client := internal.InitKubeClient()
    
    // Create controllers
    hobbyFarmController := internal.NewHobbyFarmController(client)
    kratixController := internal.NewKratixController(client)
    hobbyFarmKratixIntegration := internal.NewHobbyFarmKratixIntegration(client)
    
    // Setup graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    
    // Handle shutdown signals
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
    
    // Start webhook server if enabled
    webhookPort := os.Getenv("WEBHOOK_PORT")
    if webhookPort == "" {
        webhookPort = "8443"
    }
    
    if os.Getenv("ENABLE_WEBHOOK") == "true" {
        log.Println("🌐 Starting webhook server...")
        go func() {
            if err := startWebhookServer(client, webhookPort); err != nil {
                log.Printf("❌ Webhook server error: %v", err)
            }
        }()
    }
    
    // Run initial SSH username fix across all VMs
    log.Println("🔧 Running startup SSH username validation...")
    runGlobalSSHUsernameFix(client)
    
    // Start periodic global SSH username fix
    go startPeriodicGlobalSSHFix(ctx, client)
    
    // Determine integration mode
    integrationMode := os.Getenv("INTEGRATION_MODE")
    if integrationMode == "" {
        integrationMode = "hybrid" // Default: both HobbyFarm and Kratix
    }
    
    log.Printf("🎯 Integration Mode: %s", integrationMode)
    
    // Start controllers based on integration mode
    switch integrationMode {
    case "hobbyfarm-only":
        log.Println("🎓 Starting HobbyFarm-only mode...")
        startHobbyFarmOnlyMode(ctx, hobbyFarmController)
        
    case "kratix-only":
        log.Println("🎯 Starting Kratix-only mode...")
        startKratixOnlyMode(ctx, kratixController)
        
    case "hybrid":
        log.Println("🔗 Starting Hybrid mode (HobbyFarm + Kratix)...")
        startHybridMode(ctx, hobbyFarmController, kratixController, hobbyFarmKratixIntegration)
        
    default:
        log.Fatalf("❌ Unknown integration mode: %s", integrationMode)
    }
    
    // Start common services
    startCommonServices(ctx, client)
    
    // Log startup completion
    logStartupSummary(integrationMode, webhookPort)
    
    // Wait for shutdown signal
    <-sigChan
    log.Println("🛑 Shutdown signal received, gracefully stopping...")
    
    // Cancel context to stop all goroutines
    cancel()
    
    // Give goroutines time to cleanup
    time.Sleep(2 * time.Second)
    log.Println("✅ HobbyFarm Provisioner stopped gracefully")
}

// Global SSH username fix that works across all VMs
func runGlobalSSHUsernameFix(client dynamic.Interface) {
    log.Println("🔧 Running global SSH username validation...")
    
    // Get all VirtualMachines in hobbyfarm-system namespace
    virtualMachines, err := client.Resource(internal.GetVirtualMachineGVR()).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("❌ Failed to list VirtualMachines: %v", err)
        return
    }
    
    fixedCount := 0
    checkedCount := 0
    
    for _, vm := range virtualMachines.Items {
        vmName := vm.GetName()
        vmIP, _, _ := unstructured.NestedString(vm.Object, "status", "public_ip")
        currentSSHUser, _, _ := unstructured.NestedString(vm.Object, "spec", "ssh_username")
        status, _, _ := unstructured.NestedString(vm.Object, "status", "status")
        
        // Skip VMs without IP or not ready
        if vmIP == "" || status != "ready" {
            continue
        }
        
        checkedCount++
        
        // Use the utility function to get correct SSH username
        correctSSHUser := internal.GetSSHUsername(vmIP)
        
        // Fix if SSH username is wrong
        if currentSSHUser != correctSSHUser {
            log.Printf("🔧 GLOBAL FIX: VM %s has ssh_username=%s but needs %s for %s VM (IP: %s)", 
                vmName, currentSSHUser, correctSSHUser, internal.GetVMTypeFromIP(vmIP), vmIP)
            
            // Patch the VirtualMachine
            patch := map[string]interface{}{
                "spec": map[string]interface{}{
                    "ssh_username": correctSSHUser,
                },
                "metadata": map[string]interface{}{
                    "labels": map[string]interface{}{
                        "vm-type": internal.GetVMTypeFromIP(vmIP),
                        "ssh-username-fixed": "true",
                    },
                },
            }
            
            patchBytes, err := json.Marshal(patch)
            if err != nil {
                log.Printf("❌ Failed to marshal patch for %s: %v", vmName, err)
                continue
            }
            
            _, err = client.Resource(internal.GetVirtualMachineGVR()).Namespace("hobbyfarm-system").Patch(
                context.TODO(), vmName, types.MergePatchType,
                patchBytes, metav1.PatchOptions{},
            )
            
            if err != nil {
                log.Printf("❌ Failed to patch SSH username for %s: %v", vmName, err)
            } else {
                log.Printf("✅ GLOBAL FIX: Fixed SSH username for %s: %s -> %s (%s VM)", 
                    vmName, currentSSHUser, correctSSHUser, internal.GetVMTypeFromIP(vmIP))
                fixedCount++
            }
        } else {
            // Add vm-type label even if SSH username is correct
            labelPatch := map[string]interface{}{
                "metadata": map[string]interface{}{
                    "labels": map[string]interface{}{
                        "vm-type": internal.GetVMTypeFromIP(vmIP),
                    },
                },
            }
            
            labelBytes, _ := json.Marshal(labelPatch)
            client.Resource(internal.GetVirtualMachineGVR()).Namespace("hobbyfarm-system").Patch(
                context.TODO(), vmName, types.MergePatchType,
                labelBytes, metav1.PatchOptions{},
            )
        }
    }
    
    if fixedCount > 0 {
        log.Printf("🎉 Global SSH fix completed: Fixed %d out of %d VirtualMachines", fixedCount, checkedCount)
    } else if checkedCount > 0 {
        log.Printf("✅ Global SSH check: All %d VirtualMachines have correct SSH usernames", checkedCount)
    } else {
        log.Printf("ℹ️ No ready VirtualMachines found to check")
    }
}

// Start periodic global SSH username fix
func startPeriodicGlobalSSHFix(ctx context.Context, client dynamic.Interface) {
    ticker := time.NewTicker(10 * time.Minute) // Check every 10 minutes
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            log.Println("🔧 Running periodic global SSH username check...")
            runGlobalSSHUsernameFix(client)
        }
    }
}

// HobbyFarm-only mode
func startHobbyFarmOnlyMode(ctx context.Context, hobbyFarmController *internal.HobbyFarmController) {
    // Original HobbyFarm Session Controller
    go func() {
        log.Println("🎯 Starting HobbyFarm Session Controller...")
        runControllerWithRetry(ctx, "HobbyFarm Session Controller", func() {
            hobbyFarmController.WatchHobbyFarmVMs()
        })
    }()
    
    // Enhanced VM allocator for TrainingVMs
    go func() {
        log.Println("🔄 Starting HobbyFarm VM allocator...")
        runControllerWithRetry(ctx, "HobbyFarm VM Allocator", func() {
            client := internal.InitKubeClient()
            enhancedAllocator := internal.NewEnhancedVMAllocator(client)
            ticker := time.NewTicker(10 * time.Second)
            defer ticker.Stop()
            
            for {
                select {
                case <-ctx.Done():
                    return
                case <-ticker.C:
                    enhancedAllocator.AllocateTrainingVMs()
                }
            }
        })
    }()
}

// Kratix-only mode
func startKratixOnlyMode(ctx context.Context, kratixController *internal.KratixController) {
    // Kratix Promise VM Provisioning Controller
    go func() {
        log.Println("🎯 Starting Kratix Promise Controller...")
        runControllerWithRetry(ctx, "Kratix Promise Controller", func() {
            kratixController.WatchVMProvisioningRequestsWithCloudMonitoring()
        })
    }()
}

// Hybrid mode (both HobbyFarm and Kratix)
func startHybridMode(ctx context.Context, hobbyFarmController *internal.HobbyFarmController, kratixController *internal.KratixController, integration *internal.HobbyFarmKratixIntegration) {
    // Option 1: HobbyFarm creates TrainingVMs (Original behavior)
    if os.Getenv("HOBBYFARM_DIRECT_MODE") == "true" {
        log.Println("🎓 Hybrid Mode: HobbyFarm Direct (Sessions → TrainingVMs)")
        go func() {
            runControllerWithRetry(ctx, "HobbyFarm Session Controller", func() {
                hobbyFarmController.WatchHobbyFarmVMs()
            })
        }()
        
        go func() {
            runControllerWithRetry(ctx, "HobbyFarm VM Allocator", func() {
                client := internal.InitKubeClient()
                enhancedAllocator := internal.NewEnhancedVMAllocator(client)
                ticker := time.NewTicker(10 * time.Second)
                defer ticker.Stop()
                
                for {
                    select {
                    case <-ctx.Done():
                        return
                    case <-ticker.C:
                        enhancedAllocator.AllocateTrainingVMs()
                    }
                }
            })
        }()
    } else {
        // Option 2: HobbyFarm → Kratix → VMs (New Promise-based behavior)
        log.Println("🔗 Hybrid Mode: HobbyFarm → Kratix Promises (Sessions → VMProvisioningRequests)")
        
        // HobbyFarm → Kratix Integration
        go func() {
            runControllerWithRetry(ctx, "HobbyFarm → Kratix Integration", func() {
                integration.WatchSessionsForKratix()
            })
        }()
        
        // Kratix Promise Controller
        go func() {
            runControllerWithRetry(ctx, "Kratix Promise Controller", func() {
                kratixController.WatchVMProvisioningRequestsWithCloudMonitoring()
            })
        }()
    }
}

// Common services (monitoring, cleanup, etc.)
func startCommonServices(ctx context.Context, client dynamic.Interface) {
    // Cleanup routine
    go func() {
        log.Println("🧹 Starting cleanup routine...")
        ticker := time.NewTicker(5 * time.Minute)
        defer ticker.Stop()
        
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                log.Println("🧹 Running periodic cleanup...")
                cleanupOrphanedResources(client)
                internal.CleanupFailedEC2Instances(client)
            }
        }
    }()
    
    // Health monitoring
    go func() {
        log.Println("💓 Starting health monitoring...")
        runHealthMonitoring(ctx, client)
    }()
    
    // Resource discovery
    go func() {
        log.Println("🔍 Starting resource discovery...")
        runResourceDiscovery(ctx, client)
    }()
}

func startWebhookServer(client dynamic.Interface, port string) error {
    internal.StartWebhookServer(client, port)
    return nil
}

func runControllerWithRetry(ctx context.Context, name string, controllerFunc func()) {
    retryCount := 0
    maxRetries := 5
    
    for {
        select {
        case <-ctx.Done():
            log.Printf("🛑 Stopping %s", name)
            return
        default:
            func() {
                defer func() {
                    if r := recover(); r != nil {
                        retryCount++
                        log.Printf("❌ %s crashed (attempt %d/%d): %v", name, retryCount, maxRetries, r)
                        
                        if retryCount >= maxRetries {
                            log.Printf("💀 %s exceeded max retries, stopping", name)
                            return
                        }
                        
                        // Exponential backoff
                        backoff := time.Duration(retryCount) * 10 * time.Second
                        log.Printf("⏳ Retrying %s in %v...", name, backoff)
                        time.Sleep(backoff)
                    }
                }()
                
                // Reset retry count on successful run
                retryCount = 0
                controllerFunc()
            }()
        }
    }
}

func cleanupOrphanedResources(client dynamic.Interface) {
    log.Println("🔍 Checking for orphaned resources...")
    
    // Cleanup orphaned TrainingVMs
    cleanupOrphanedTrainingVMs(client)
    
    // Cleanup orphaned VMProvisioningRequests
    cleanupOrphanedVMProvisioningRequests(client)
}

func cleanupOrphanedTrainingVMs(client dynamic.Interface) {
    trainingVMs, err := client.Resource(internal.GetTrainingVMGVR()).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }
    
    sessions, err := client.Resource(internal.GetSessionGVR()).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }
    
    // Build map of active sessions
    activeSessions := make(map[string]bool)
    for _, session := range sessions.Items {
        activeSessions[session.GetName()] = true
    }
    
    // Check for orphaned TrainingVMs
    orphanedCount := 0
    for _, tvm := range trainingVMs.Items {
        tvmName := tvm.GetName()
        
        // Skip VMs that start with "req-" or "kratix-" (these are special)
        if strings.HasPrefix(tvmName, "req-") || strings.HasPrefix(tvmName, "kratix-") {
            continue
        }
        
        // Check if VM has corresponding session
        if !activeSessions[tvmName] {
            // Check VM age before cleanup
            creationTime := tvm.GetCreationTimestamp()
            if time.Since(creationTime.Time) > 1*time.Hour {
                log.Printf("🗑️ Cleaning up orphaned TrainingVM: %s", tvmName)
                err := client.Resource(internal.GetTrainingVMGVR()).Namespace("default").Delete(
                    context.TODO(), tvmName, metav1.DeleteOptions{})
                if err != nil {
                    log.Printf("❌ Failed to delete orphaned TrainingVM %s: %v", tvmName, err)
                } else {
                    orphanedCount++
                }
            }
        }
    }
    
    if orphanedCount > 0 {
        log.Printf("🧹 Cleaned up %d orphaned TrainingVMs", orphanedCount)
    }
}

func cleanupOrphanedVMProvisioningRequests(client dynamic.Interface) {
    vmProvisioningRequestGVR := internal.GetVMProvisioningRequestGVR()
    
    requests, err := client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }
    
    sessions, err := client.Resource(internal.GetSessionGVR()).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return
    }
    
    // Build map of active sessions
    activeSessions := make(map[string]bool)
    for _, session := range sessions.Items {
        activeSessions[session.GetName()] = true
    }
    
    // Check for orphaned VMProvisioningRequests
    orphanedCount := 0
    for _, req := range requests.Items {
        reqName := req.GetName()
        labels := req.GetLabels()
        
        // Only process requests created from HobbyFarm integration
        if labels != nil && labels["source"] == "hobbyfarm-integration" {
            sessionName := labels["hobbyfarm.io/session"]
            if sessionName != "" && !activeSessions[sessionName] {
                // Check age before cleanup
                creationTime := req.GetCreationTimestamp()
                if time.Since(creationTime.Time) > 1*time.Hour {
                    log.Printf("🗑️ Cleaning up orphaned VMProvisioningRequest: %s", reqName)
                    err := client.Resource(vmProvisioningRequestGVR).Namespace("default").Delete(
                        context.TODO(), reqName, metav1.DeleteOptions{})
                    if err != nil {
                        log.Printf("❌ Failed to delete orphaned VMProvisioningRequest %s: %v", reqName, err)
                    } else {
                        orphanedCount++
                    }
                }
            }
        }
    }
    
    if orphanedCount > 0 {
        log.Printf("🧹 Cleaned up %d orphaned VMProvisioningRequests", orphanedCount)
    }
}

func runHealthMonitoring(ctx context.Context, client dynamic.Interface) {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            performHealthCheck(client)
        }
    }
}

func performHealthCheck(client dynamic.Interface) {
    // Check static VM pool health
    staticVMsUp := 0
    staticVMsTotal := len(internal.GetVMPool())
    
    for _, vmIP := range internal.GetVMPool() {
        if internal.IsVMReachable(vmIP) {
            staticVMsUp++
        }
    }
    
    // Check TrainingVMs
    trainingVMs, err := client.Resource(internal.GetTrainingVMGVR()).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("⚠️ Health check failed to list TrainingVMs: %v", err)
        return
    }
    
    trainingVMStats := map[string]int{
        "pending":      0,
        "allocated":    0,
        "provisioned":  0,
        "failed":       0,
    }
    
    for _, tvm := range trainingVMs.Items {
        state, _, _ := unstructured.NestedString(tvm.Object, "status", "state")
        provisioned, _, _ := unstructured.NestedBool(tvm.Object, "status", "provisioned")
        
        if state == "allocated" && provisioned {
            trainingVMStats["provisioned"]++
        } else if state == "allocated" {
            trainingVMStats["allocated"]++
        } else if state == "failed" {
            trainingVMStats["failed"]++
        } else {
            trainingVMStats["pending"]++
        }
    }
    
    // Check VMProvisioningRequests (Kratix)
    vmProvisioningRequestGVR := internal.GetVMProvisioningRequestGVR()
    requests, err := client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("⚠️ Health check failed to list VMProvisioningRequests: %v", err)
        return
    }
    
    kratixStats := map[string]int{
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
        kratixStats[state]++
    }
    
    // Log health summary periodically (every 5th check)
    if time.Now().Minute()%5 == 0 {
        log.Printf("💓 Health Summary:")
        log.Printf("   📊 Static VMs: %d/%d up", staticVMsUp, staticVMsTotal)
        log.Printf("   📊 TrainingVMs: pending=%d, allocated=%d, provisioned=%d, failed=%d", 
            trainingVMStats["pending"], trainingVMStats["allocated"], trainingVMStats["provisioned"], trainingVMStats["failed"])
        log.Printf("   📊 Kratix Requests: pending=%d, allocated=%d, provisioning=%d, ready=%d, failed=%d", 
            kratixStats["pending"], kratixStats["allocated"], kratixStats["provisioning"], kratixStats["ready"], kratixStats["failed"])
    }
}

func runResourceDiscovery(ctx context.Context, client dynamic.Interface) {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    lastSessionCount := 0
    lastVMCount := 0
    lastKratixCount := 0
    
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // Discover Sessions
            sessionCount := discoverSessions(client)
            if sessionCount != lastSessionCount {
                log.Printf("🔍 Session count changed: %d -> %d", lastSessionCount, sessionCount)
                lastSessionCount = sessionCount
            }
            
            // Discover VirtualMachines
            vmCount := discoverVirtualMachines(client)
            if vmCount != lastVMCount {
                log.Printf("🔍 VirtualMachine count changed: %d -> %d", lastVMCount, vmCount)
                lastVMCount = vmCount
            }
            
            // Discover VMProvisioningRequests
            kratixCount := discoverVMProvisioningRequests(client)
            if kratixCount != lastKratixCount {
                log.Printf("🔍 VMProvisioningRequest count changed: %d -> %d", lastKratixCount, kratixCount)
                lastKratixCount = kratixCount
            }
        }
    }
}

func discoverSessions(client dynamic.Interface) int {
    sessions, err := client.Resource(internal.GetSessionGVR()).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return 0
    }
    
    if len(sessions.Items) > 0 {
        log.Printf("🔍 Found %d Sessions in hobbyfarm-system", len(sessions.Items))
        for _, session := range sessions.Items {
            user, _, _ := unstructured.NestedString(session.Object, "spec", "user")
            scenario, _, _ := unstructured.NestedString(session.Object, "spec", "scenario")
            log.Printf("  📋 Session: %s, User: %s, Scenario: %s", session.GetName(), user, scenario)
        }
    }
    
    return len(sessions.Items)
}

func discoverVirtualMachines(client dynamic.Interface) int {
    virtualMachineGVR := internal.GetVirtualMachineGVR()
    
    vms, err := client.Resource(virtualMachineGVR).Namespace("hobbyfarm-system").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return 0
    }
    
    if len(vms.Items) > 0 {
        log.Printf("🔍 Found %d VirtualMachines in hobbyfarm-system", len(vms.Items))
        for _, vm := range vms.Items {
            user, _, _ := unstructured.NestedString(vm.Object, "spec", "user")
            status, _, _ := unstructured.NestedString(vm.Object, "status", "status")
            publicIP, _, _ := unstructured.NestedString(vm.Object, "status", "public_ip")
            sshUsername, _, _ := unstructured.NestedString(vm.Object, "spec", "ssh_username")
            vmType, _, _ := unstructured.NestedString(vm.Object, "metadata", "labels", "vm-type")
            
            log.Printf("  📋 VirtualMachine: %s, User: %s, Status: %s, IP: %s, SSH: %s, Type: %s", 
                vm.GetName(), user, status, publicIP, sshUsername, vmType)
        }
    }
    
    return len(vms.Items)
}

func discoverVMProvisioningRequests(client dynamic.Interface) int {
    vmProvisioningRequestGVR := internal.GetVMProvisioningRequestGVR()
    
    requests, err := client.Resource(vmProvisioningRequestGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        return 0
    }
    
    if len(requests.Items) > 0 {
        log.Printf("🔍 Found %d VMProvisioningRequests", len(requests.Items))
        for _, req := range requests.Items {
            user, _, _ := unstructured.NestedString(req.Object, "spec", "user")
            session, _, _ := unstructured.NestedString(req.Object, "spec", "session")
            state, _, _ := unstructured.NestedString(req.Object, "status", "state")
            vmIP, _, _ := unstructured.NestedString(req.Object, "status", "vmIP")
            vmType, _, _ := unstructured.NestedString(req.Object, "status", "vmType")
            log.Printf("  📋 VMProvisioningRequest: %s, User: %s, Session: %s, State: %s, IP: %s, Type: %s", 
                req.GetName(), user, session, state, vmIP, vmType)
        }
    }
    
    return len(requests.Items)
}

func logStartupSummary(integrationMode, webhookPort string) {
    log.Println("🎉 =============================================")
    log.Println("🎉 HobbyFarm Hybrid Provisioner with Kratix")
    log.Println("🎉 =============================================")
    log.Printf("🔗 Integration Mode: %s", integrationMode)
    
    switch integrationMode {
    case "hobbyfarm-only":
        log.Println("🎓 HobbyFarm Session → TrainingVM → Allocation")
    case "kratix-only":
        log.Println("🎯 Kratix VMProvisioningRequest → VM Allocation")
    case "hybrid":
        if os.Getenv("HOBBYFARM_DIRECT_MODE") == "true" {
            log.Println("🔗 HobbyFarm Session → TrainingVM → Allocation")
        } else {
            log.Println("🔗 HobbyFarm Session → Kratix VMProvisioningRequest → VM")
        }
    }
    
    log.Println("🔧 SSH Username Auto-Detection: Enabled")
    log.Println("   🏠 Local VMs (192.168.x.x): ssh_username=kube")
    log.Println("   🌐 EC2 VMs (public IPs): ssh_username=ubuntu")
    log.Println("🧹 Orphaned resource cleanup: Enabled")
    log.Println("💓 Health monitoring: Enabled")
    log.Println("🔍 Resource discovery: Enabled")
    
    if os.Getenv("ENABLE_WEBHOOK") == "true" {
        log.Printf("🌐 Webhook server: Port %s", webhookPort)
    }
    
    log.Println("🎉 =============================================")
    log.Println("🎯 Ready to provision VMs with correct SSH users!")
    log.Println("🎉 =============================================")
}
