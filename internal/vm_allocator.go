package internal

import (
    "context"
    "fmt"
    "log"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/dynamic"
)

func AllocateTrainingVMs(client dynamic.Interface, usedIPs map[string]bool, ansibleRunner *AnsibleRunner) {
    // Get TrainingVMs directly
    trainingVMs, err := client.Resource(trainingVMGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    if err != nil {
        log.Printf("❌ Failed to list TrainingVMs: %v", err)
        return
    }

    if len(trainingVMs.Items) == 0 {
        log.Printf("🔍 No TrainingVMs found in default namespace")
        return
    }

    log.Printf("🔍 Processing %d TrainingVMs for allocation", len(trainingVMs.Items))

    for _, tvm := range trainingVMs.Items {
        name := tvm.GetName()
        state, _, _ := unstructured.NestedString(tvm.Object, "status", "state")
        ip, _, _ := unstructured.NestedString(tvm.Object, "status", "vmIP")
        
        // Check if already provisioned
        provisioned, _, _ := unstructured.NestedBool(tvm.Object, "status", "provisioned")

        log.Printf("🔍 TrainingVM %s: IP=%s, State=%s, Provisioned=%v", name, ip, state, provisioned)

        if state != "" && ip != "" {
            allocatedAtStr, found, _ := unstructured.NestedString(tvm.Object, "status", "allocatedAt")
            
            // Different boot times for different VM types
            bootWaitTime := getBootWaitTime(ip)
            
            // Only check grace period if VM is NOT provisioned yet
            if found && !provisioned {
                if t, err := time.Parse(time.RFC3339, allocatedAtStr); err == nil {
                    if time.Since(t) < bootWaitTime {
                        vmType := getVMType(ip)
                        log.Printf("⏳ Waiting for %s VM %s to boot (allocated %v ago, need %v)", 
                            vmType, ip, time.Since(t).Round(time.Second), bootWaitTime)
                        continue
                    }
                }
            }

            if isVMReachable(ip) {
                // If VM is allocated but not provisioned, run Ansible
                if !provisioned {
                    log.Printf("🎯 VM %s is ready for provisioning", ip)
                    
                    // Get session details to determine scenario
                    sessionName := name // TrainingVM name should match session name
                    session, err := client.Resource(sessionGVR).Namespace("hobbyfarm-system").Get(
                        context.TODO(), sessionName, metav1.GetOptions{})
                    if err != nil {
                        log.Printf("❌ Failed to get session %s from hobbyfarm-system: %v", sessionName, err)
                        continue
                    }
                    
                    scenario, _, _ := unstructured.NestedString(session.Object, "spec", "scenario")
                    log.Printf("📋 Session %s uses scenario: %s", sessionName, scenario)
                    
                    // Wait for SSH with appropriate timeout
                    sshTimeout := getSSHTimeout(ip)
                    log.Printf("🔐 Waiting for SSH on %s VM %s...", getVMType(ip), ip)
                    if err := ansibleRunner.WaitForSSH(ip, sshTimeout); err != nil {
                        log.Printf("❌ SSH not ready on VM %s: %v", ip, err)
                        
                        // For EC2 instances, don't immediately release - they might need more time
                        if isPublicIP(ip) {
                            log.Printf("ℹ️  EC2 instance %s still booting, will retry next cycle", ip)
                        }
                        continue
                    }
                    
                    // Run Ansible provisioning
                    log.Printf("🚀 Starting Ansible provisioning for VM %s", ip)
                    if err := ansibleRunner.RunPlaybook(ip, name, scenario); err != nil {
                        log.Printf("❌ Ansible provisioning failed for VM %s: %v", ip, err)
                        continue
                    }
                    
                    // Mark as provisioned - Use status subresource after CRD update
                    patch := `{"status":{"provisioned":true}}`
                    _, err = client.Resource(trainingVMGVR).Namespace("default").Patch(
                        context.TODO(), name, types.MergePatchType,
                        []byte(patch), metav1.PatchOptions{}, "status")
                    if err != nil {
                        log.Printf("❌ Failed to mark VM as provisioned: %v", err)
                    } else {
                        log.Printf("✅ VM %s marked as provisioned", ip)
                    }
                } else {
                    log.Printf("✅ VM %s already provisioned", ip)
                }
                continue
            } else {
                vmType := getVMType(ip)
                
                // For EC2 instances, be more patient before releasing
                if isPublicIP(ip) && found {
                    if t, err := time.Parse(time.RFC3339, allocatedAtStr); err == nil {
                        // Give EC2 instances up to 10 minutes to become ready
                        if time.Since(t) < 10*time.Minute {
                            log.Printf("⏳ EC2 instance %s still starting up (%v old), waiting...", 
                                ip, time.Since(t).Round(time.Second))
                            continue
                        }
                    }
                }
                
                log.Printf("⚠️ Releasing unreachable %s VM %s", vmType, ip)
                patch := `{"status":{"vmIP":"","state":"","allocatedAt":"","provisioned":false}}`
                client.Resource(trainingVMGVR).Namespace("default").Patch(
                    context.TODO(), name, types.MergePatchType,
                    []byte(patch), metav1.PatchOptions{}, "status")
                continue
            }
        }

        // If no VM allocated, try to allocate one from static pool
        log.Printf("🔍 TrainingVM %s needs allocation", name)
        var selectedIP string
        for _, candidateIP := range vmPool {
            if !usedIPs[candidateIP] && isVMReachable(candidateIP) {
                selectedIP = candidateIP
                break
            }
        }

        if selectedIP != "" {
            patch := fmt.Sprintf(`{
              "status": {
                "vmIP": "%s",
                "state": "allocated",
                "allocatedAt": "%s",
                "provisioned": false
              }
            }`, selectedIP, time.Now().Format(time.RFC3339))

            log.Printf("🔧 Attempting to patch TrainingVM %s with IP %s", name, selectedIP)
            
            // Use status subresource after CRD update
            _, err := client.Resource(trainingVMGVR).Namespace("default").Patch(
                context.TODO(), name, types.MergePatchType,
                []byte(patch), metav1.PatchOptions{}, "status")
            if err == nil {
                log.Printf("✅ Allocated static VM %s to TrainingVM %s", selectedIP, name)
                usedIPs[selectedIP] = true
            } else {
                log.Printf("❌ Failed to allocate VM %s to TrainingVM %s: %v", selectedIP, name, err)
                log.Printf("🔧 Retrying without status subresource...")
                
                // Fallback to patching without status subresource
                _, fallbackErr := client.Resource(trainingVMGVR).Namespace("default").Patch(
                    context.TODO(), name, types.MergePatchType,
                    []byte(patch), metav1.PatchOptions{})
                if fallbackErr == nil {
                    log.Printf("✅ Allocated static VM %s to TrainingVM %s (fallback method)", selectedIP, name)
                    usedIPs[selectedIP] = true
                } else {
                    log.Printf("❌ Both allocation methods failed for %s: %v", name, fallbackErr)
                }
            }
        } else {
            log.Printf("🚀 No static VMs available, trying EC2 fallback for %s", name)
            HandleEC2Fallback(client, name)
        }
    }
}
