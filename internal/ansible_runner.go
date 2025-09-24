// internal/ansible_runner.go - Complete updated file with SSH username detection
package internal

import (
    "fmt"
    "log"
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "time"

    "k8s.io/client-go/dynamic"
)

type AnsibleRunner struct {
    inventoryPath   string
    playbookPath    string
    sshKeyPath      string
    client          dynamic.Interface
    packageDetector *PackageDetector
}

type ProvisioningConfig struct {
    Playbooks    []string
    Variables    map[string]string
    Packages     []string
    Requirements []string
}

func NewAnsibleRunner(client dynamic.Interface) *AnsibleRunner {
    homeDir, _ := os.UserHomeDir()
    return &AnsibleRunner{
        inventoryPath:   "./ansible/inventories/hosts",
        playbookPath:    "./ansible/playbooks",
        sshKeyPath:      filepath.Join(homeDir, ".ssh/id_rsa"),
        client:          client,
        packageDetector: NewPackageDetector(client),
    }
}

func (ar *AnsibleRunner) RunPlaybook(vmIP string, sessionName string, scenario string) error {
    log.Printf("🎯 Starting provisioning for %s VM %s (session: %s)", getVMType(vmIP), vmIP, sessionName)

    // For EC2 instances, wait for readiness
    if isPublicIP(vmIP) {
        log.Printf("⏳ Waiting for EC2 instance %s to be fully ready...", vmIP)
        if err := ar.waitForEC2ReadyFixed(vmIP); err != nil {
            return fmt.Errorf("EC2 instance not ready: %v", err)
        }
    }

    // Get dynamic provisioning configuration using smart detection
    config, err := ar.getProvisioningConfig(sessionName, scenario)
    if err != nil {
        log.Printf("❌ Failed to get provisioning config: %v", err)
        return err
    }

    log.Printf("🎯 Provisioning config for session %s: playbooks=%v, packages=%v", sessionName, config.Playbooks, config.Packages)

    // Detect SSH user for this VM using the utility function
    sshUser := getSSHUsername(vmIP)
    log.Printf("🔍 Auto-detected SSH username: %s for %s VM (IP: %s)", sshUser, getVMType(vmIP), vmIP)

    // Verify SSH user works
    if !ar.verifySSHUser(vmIP, sshUser) {
        // Fallback to detection method if auto-detection fails
        log.Printf("⚠️ Auto-detected SSH user %s failed, trying fallback detection...", sshUser)
        detectedUser, err := ar.detectSSHUser(vmIP)
        if err != nil {
            return fmt.Errorf("SSH user detection failed: %v", err)
        }
        sshUser = detectedUser
        log.Printf("🔍 Fallback detected SSH user: %s for %s", sshUser, vmIP)
    }

    // Create dynamic inventory with session-specific variables
    inventoryContent := ar.buildInventory(vmIP, sshUser, sessionName, config)

    // Write temporary inventory file
    tmpInventory := fmt.Sprintf("/tmp/ansible_inventory_%s", sessionName)
    if err := os.WriteFile(tmpInventory, []byte(inventoryContent), 0644); err != nil {
        return fmt.Errorf("failed to write inventory: %v", err)
    }
    defer os.Remove(tmpInventory)

    // Run multiple playbooks in sequence
    for _, playbook := range config.Playbooks {
        log.Printf("🎭 Running playbook %s for session %s on user %s", playbook, sessionName, sshUser)
        if err := ar.runSinglePlaybook(tmpInventory, playbook, sessionName, config); err != nil {
            return fmt.Errorf("playbook %s failed: %v", playbook, err)
        }
    }

    log.Printf("✅ All playbooks completed for session %s on VM %s (user: %s)", sessionName, vmIP, sshUser)
    return nil
}

// UPDATED: Use smart package detection instead of annotation-based detection
func (ar *AnsibleRunner) getProvisioningConfig(sessionName, scenario string) (*ProvisioningConfig, error) {
    log.Printf("🧠 Using smart package detection for session: %s", sessionName)
    
    // Use smart detection instead of annotation-based detection
    config := ar.packageDetector.DetectPackagesFromSession(sessionName)
    
    if config == nil {
        log.Printf("⚠️ Smart detection failed, using fallback config")
        return &ProvisioningConfig{
            Playbooks: []string{"base.yaml", "dynamic.yaml"},
            Variables: map[string]string{},
            Packages:  []string{},
            Requirements: []string{},
        }, nil
    }
    
    log.Printf("✅ Smart package detection result:")
    log.Printf("  📦 Packages: %v", config.Packages)
    log.Printf("  🔧 Variables: %v", config.Variables)
    log.Printf("  🎭 Playbooks: %v", config.Playbooks)
    
    return config, nil
}

// Verify if SSH user works with the VM
func (ar *AnsibleRunner) verifySSHUser(vmIP, sshUser string) bool {
    cmd := exec.Command("ssh",
        "-o", "StrictHostKeyChecking=no",
        "-o", "UserKnownHostsFile=/dev/null",
        "-o", "ConnectTimeout=15",
        "-o", "BatchMode=yes",
        "-i", ar.sshKeyPath,
        fmt.Sprintf("%s@%s", sshUser, vmIP),
        "echo", "SSH_TEST_SUCCESS",
    )
    
    output, err := cmd.CombinedOutput()
    if err == nil && strings.Contains(string(output), "SSH_TEST_SUCCESS") {
        log.Printf("✅ SSH verification successful with user %s for %s", sshUser, vmIP)
        return true
    }
    
    log.Printf("⚠️ SSH verification failed with user %s for %s", sshUser, vmIP)
    return false
}

// EC2 readiness check
func (ar *AnsibleRunner) waitForEC2ReadyFixed(vmIP string) error {
    maxWait := 5 * time.Minute
    deadline := time.Now().Add(maxWait)
    
    log.Printf("🔍 Testing SSH connectivity to EC2 instance %s...", vmIP)
    
    for time.Now().Before(deadline) {
        if ar.testSSHSimple(vmIP) {
            log.Printf("✅ EC2 instance %s SSH is ready", vmIP)
            return nil
        }
        
        log.Printf("⏳ SSH not ready yet for %s, retrying in 10 seconds...", vmIP)
        time.Sleep(10 * time.Second)
    }
    
    return fmt.Errorf("EC2 instance %s SSH not ready after %v", vmIP, maxWait)
}

// Simplified SSH test that actually works
func (ar *AnsibleRunner) testSSHSimple(vmIP string) bool {
    // Use the utility function to get the expected SSH user
    expectedUser := getSSHUsername(vmIP)
    users := []string{expectedUser}
    
    // Add fallback users for robustness
    if isPublicIP(vmIP) {
        users = append(users, "ubuntu", "ec2-user", "admin")
    } else {
        users = append(users, "kube", "ubuntu", "admin")
    }
    
    for _, user := range users {
        cmd := exec.Command("ssh",
            "-o", "StrictHostKeyChecking=no",
            "-o", "UserKnownHostsFile=/dev/null",
            "-o", "ConnectTimeout=15",
            "-o", "BatchMode=yes",
            "-i", ar.sshKeyPath,
            fmt.Sprintf("%s@%s", user, vmIP),
            "echo", "SSH_TEST_SUCCESS",
        )
        
        output, err := cmd.CombinedOutput()
        if err == nil && strings.Contains(string(output), "SSH_TEST_SUCCESS") {
            log.Printf("🔍 SSH test successful with user %s for %s", user, vmIP)
            return true
        }
    }
    
    return false
}

func (ar *AnsibleRunner) detectSSHUser(vmIP string) (string, error) {
    // Start with the utility function prediction
    expectedUser := getSSHUsername(vmIP)
    users := []string{expectedUser}
    
    // Add fallback users based on VM type
    if isPublicIP(vmIP) {
        // For EC2, try common users
        users = append(users, "ubuntu", "ec2-user", "admin")
    } else {
        // For local VMs, try kube first
        users = append(users, "kube", "ubuntu", "admin")
    }

    for _, user := range users {
        cmd := exec.Command("ssh",
            "-o", "StrictHostKeyChecking=no",
            "-o", "UserKnownHostsFile=/dev/null",
            "-o", "ConnectTimeout=15",
            "-o", "BatchMode=yes",
            "-i", ar.sshKeyPath,
            fmt.Sprintf("%s@%s", user, vmIP),
            "echo", "success",
        )

        if err := cmd.Run(); err == nil {
            log.Printf("🔍 Detected existing SSH user for %s: %s", vmIP, user)
            return user, nil
        }
    }

    return "", fmt.Errorf("no working SSH user found for %s", vmIP)
}

// Build inventory for existing user instead of session user
func (ar *AnsibleRunner) buildInventory(vmIP string, sshUser string, sessionName string, config *ProvisioningConfig) string {
    var inventory strings.Builder

    // Base inventory with detected SSH user (existing user)
    inventory.WriteString(fmt.Sprintf(`[target]
%s ansible_user=%s ansible_ssh_private_key_file=%s ansible_ssh_common_args='-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null'

[all:vars]
ansible_python_interpreter=/usr/bin/python3
session_name=%s
`, vmIP, sshUser, ar.sshKeyPath, sessionName))

    // Add session-specific variables
    for key, value := range config.Variables {
        inventory.WriteString(fmt.Sprintf("%s=%s\n", key, value))
    }

    // Add package list if specified
    if len(config.Packages) > 0 {
        inventory.WriteString(fmt.Sprintf("session_packages=%s\n", strings.Join(config.Packages, ",")))
    }

    // Add requirements if specified
    if len(config.Requirements) > 0 {
        inventory.WriteString(fmt.Sprintf("session_requirements=%s\n", strings.Join(config.Requirements, ",")))
    }

    return inventory.String()
}

func (ar *AnsibleRunner) runSinglePlaybook(inventory, playbook, sessionName string, config *ProvisioningConfig) error {
    playbookPath := filepath.Join(ar.playbookPath, playbook)

    // Check if playbook exists
    if _, err := os.Stat(playbookPath); os.IsNotExist(err) {
        return fmt.Errorf("playbook %s does not exist", playbookPath)
    }

    cmd := exec.Command("ansible-playbook",
        "-i", inventory,
        playbookPath,
        "-v",
        "--timeout=90",
    )

    // Add extra variables from config
    for key, value := range config.Variables {
        cmd.Args = append(cmd.Args, "-e", fmt.Sprintf("%s=%s", key, value))
    }

    // Add session name as extra variable
    cmd.Args = append(cmd.Args, "-e", fmt.Sprintf("session_name=%s", sessionName))

    // Set environment variables for Ansible
    cmd.Env = append(os.Environ(),
        "ANSIBLE_HOST_KEY_CHECKING=False",
        "ANSIBLE_SSH_RETRIES=5",
        "ANSIBLE_TIMEOUT=90",
    )

    // Capture output for better debugging
    output, err := cmd.CombinedOutput()

    if err != nil {
        log.Printf("❌ Ansible output for %s (session %s):\n%s", playbook, sessionName, string(output))
        return fmt.Errorf("ansible playbook %s failed: %v", playbook, err)
    }

    log.Printf("✅ Playbook %s completed successfully for session %s", playbook, sessionName)
    log.Printf("📝 Ansible output:\n%s", string(output))
    return nil
}

// Session cleanup function - only clean up session workspace, not user
func (ar *AnsibleRunner) CleanupSession(vmIP string, sessionName string) error {
    log.Printf("🧹 Starting workspace cleanup for session %s on VM %s", sessionName, vmIP)

    // Use utility function to get SSH user
    sshUser := getSSHUsername(vmIP)
    
    // Verify SSH user works, fallback if needed
    if !ar.verifySSHUser(vmIP, sshUser) {
        detectedUser, err := ar.detectSSHUser(vmIP)
        if err != nil {
            return fmt.Errorf("failed to detect SSH user for cleanup: %v", err)
        }
        sshUser = detectedUser
    }

    log.Printf("🧹 Cleaning up session workspace for session %s (user: %s)", sessionName, sshUser)

    // Create cleanup command to remove session workspace
    cleanupCmd := fmt.Sprintf("rm -rf /home/%s/workspace/%s", sshUser, sessionName)
    
    cmd := exec.Command("ssh",
        "-o", "StrictHostKeyChecking=no",
        "-o", "UserKnownHostsFile=/dev/null",
        "-o", "ConnectTimeout=30",
        "-i", ar.sshKeyPath,
        fmt.Sprintf("%s@%s", sshUser, vmIP),
        cleanupCmd,
    )

    output, err := cmd.CombinedOutput()

    if err != nil {
        log.Printf("❌ Session workspace cleanup failed for %s:\n%s", sessionName, string(output))
        return fmt.Errorf("session workspace cleanup failed: %v", err)
    }

    log.Printf("✅ Session %s workspace cleanup completed successfully", sessionName)
    log.Printf("📝 Cleanup output:\n%s", string(output))
    
    // Also stop any session-specific services
    serviceCleanupCmd := fmt.Sprintf("sudo systemctl stop wso2-%s 2>/dev/null || true; sudo systemctl disable wso2-%s 2>/dev/null || true; sudo rm -f /etc/systemd/system/wso2-%s.service 2>/dev/null || true; sudo systemctl daemon-reload 2>/dev/null || true", sessionName, sessionName, sessionName)
    
    serviceCmd := exec.Command("ssh",
        "-o", "StrictHostKeyChecking=no",
        "-o", "UserKnownHostsFile=/dev/null",
        "-o", "ConnectTimeout=30",
        "-i", ar.sshKeyPath,
        fmt.Sprintf("%s@%s", sshUser, vmIP),
        serviceCleanupCmd,
    )

    serviceOutput, serviceErr := serviceCmd.CombinedOutput()
    if serviceErr != nil {
        log.Printf("⚠️ Service cleanup had issues (non-critical): %s", string(serviceOutput))
    } else {
        log.Printf("✅ Session services cleanup completed")
    }

    return nil
}

// WaitForSSH waits for SSH to be available on the VM
func (ar *AnsibleRunner) WaitForSSH(vmIP string, timeout time.Duration) error {
    // For EC2 instances, use the enhanced ready check
    if isPublicIP(vmIP) {
        return ar.waitForEC2ReadyFixed(vmIP)
    }
    
    // For local VMs, use simpler check
    return ar.waitForLocalSSH(vmIP, time.Now().Add(timeout))
}

func (ar *AnsibleRunner) waitForLocalSSH(vmIP string, deadline time.Time) error {
    // Use utility function for expected user
    expectedUser := getSSHUsername(vmIP)
    users := []string{expectedUser, "kube", "ubuntu", "admin"}
    
    for time.Now().Before(deadline) {
        for _, user := range users {
            cmd := exec.Command("ssh",
                "-o", "StrictHostKeyChecking=no",
                "-o", "UserKnownHostsFile=/dev/null",
                "-o", "ConnectTimeout=5",
                "-i", ar.sshKeyPath,
                fmt.Sprintf("%s@%s", user, vmIP),
                "echo", "ready",
            )

            if err := cmd.Run(); err == nil {
                log.Printf("✅ SSH is ready on static VM %s with user %s", vmIP, user)
                return nil
            }
        }

        time.Sleep(5 * time.Second)
    }

    return fmt.Errorf("SSH timeout for VM %s", vmIP)
}

// Keep compatibility functions
func (ar *AnsibleRunner) waitForEC2Ready(vmIP string) error {
    return ar.waitForEC2ReadyFixed(vmIP)
}

func (ar *AnsibleRunner) pingTest(vmIP string) bool {
    cmd := exec.Command("ping", "-c", "1", "-W", "3", vmIP)
    return cmd.Run() == nil
}

func (ar *AnsibleRunner) sshTest(vmIP string) bool {
    return ar.testSSHSimple(vmIP)
}

func (ar *AnsibleRunner) cloudInitDone(vmIP string) bool {
    return true
}
