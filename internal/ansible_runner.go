// internal/ansible_runner.go - COMPLETE VERSION: Use existing user instead of creating session users
package internal

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

type AnsibleRunner struct {
	inventoryPath string
	playbookPath  string
	sshKeyPath    string
	client        dynamic.Interface
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
		inventoryPath: "./ansible/inventories/hosts",
		playbookPath:  "./ansible/playbooks",
		sshKeyPath:    filepath.Join(homeDir, ".ssh/id_rsa"),
		client:        client,
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

	// Get dynamic provisioning configuration
	config, err := ar.getProvisioningConfig(sessionName, scenario)
	if err != nil {
		log.Printf("❌ Failed to get provisioning config: %v", err)
		return err
	}

	log.Printf("🎯 Provisioning config for session %s: playbooks=%v, packages=%v", sessionName, config.Playbooks, config.Packages)

	// Detect SSH user for this VM (existing user)
	sshUser, err := ar.detectSSHUser(vmIP)
	if err != nil {
		return fmt.Errorf("failed to detect SSH user: %v", err)
	}
	log.Printf("🔍 Using existing SSH user: %s for %s (session: %s)", sshUser, vmIP, sessionName)

	// Create dynamic inventory with session-specific variables but existing user
	inventoryContent := ar.buildInventory(vmIP, sshUser, sessionName, config)

	// Write temporary inventory file
	tmpInventory := fmt.Sprintf("/tmp/ansible_inventory_%s", sessionName)
	if err := os.WriteFile(tmpInventory, []byte(inventoryContent), 0644); err != nil {
		return fmt.Errorf("failed to write inventory: %v", err)
	}
	defer os.Remove(tmpInventory)

	// Run multiple playbooks in sequence
	for _, playbook := range config.Playbooks {
		log.Printf("🎭 Running playbook %s for session %s on existing user %s", playbook, sessionName, sshUser)
		if err := ar.runSinglePlaybook(tmpInventory, playbook, sessionName, config); err != nil {
			return fmt.Errorf("playbook %s failed: %v", playbook, err)
		}
	}

	log.Printf("✅ All playbooks completed for session %s on VM %s (user: %s)", sessionName, vmIP, sshUser)
	return nil
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
	users := []string{"ubuntu", "ec2-user", "admin"}
	
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
	users := []string{"ubuntu", "ec2-user", "admin", "kube"}
	
	if isPublicIP(vmIP) {
		// For EC2, try common users
		users = []string{"ubuntu", "ec2-user", "admin"}
	} else {
		// For local VMs, try kube first
		users = []string{"kube", "ubuntu", "admin"}
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

func (ar *AnsibleRunner) getProvisioningConfig(sessionName, scenario string) (*ProvisioningConfig, error) {
	// Try to get config from Session first
	sessionConfig, err := ar.getSessionProvisioningConfig(sessionName)
	if err == nil && sessionConfig != nil {
		return sessionConfig, nil
	}

	// Fallback to Scenario config
	scenarioConfig, err := ar.getScenarioProvisioningConfig(scenario)
	if err == nil && scenarioConfig != nil {
		return scenarioConfig, nil
	}

	// Ultimate fallback to default config
	log.Printf("⚠️ Using default provisioning config for session %s", sessionName)
	return &ProvisioningConfig{
		Playbooks: []string{"base.yaml", "dynamic.yaml"},
		Variables: map[string]string{},
		Packages:  []string{},
	}, nil
}

func (ar *AnsibleRunner) getSessionProvisioningConfig(sessionName string) (*ProvisioningConfig, error) {
	session, err := ar.client.Resource(sessionGVR).Namespace("default").Get(
		context.TODO(), sessionName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return ar.extractProvisioningFromAnnotations(session.GetAnnotations())
}

func (ar *AnsibleRunner) getScenarioProvisioningConfig(scenario string) (*ProvisioningConfig, error) {
	if scenario == "" {
		return nil, fmt.Errorf("no scenario specified")
	}

	scenarioObj, err := ar.client.Resource(scenarioGVR).Namespace("default").Get(
		context.TODO(), scenario, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return ar.extractProvisioningFromAnnotations(scenarioObj.GetAnnotations())
}

func (ar *AnsibleRunner) extractProvisioningFromAnnotations(annotations map[string]string) (*ProvisioningConfig, error) {
	config := &ProvisioningConfig{
		Variables: make(map[string]string),
	}

	// Extract playbooks
	if playbooks, exists := annotations["provisioning.hobbyfarm.io/playbooks"]; exists {
		config.Playbooks = strings.Split(playbooks, ",")
		for i := range config.Playbooks {
			config.Playbooks[i] = strings.TrimSpace(config.Playbooks[i])
		}
	}

	// Extract packages
	if packages, exists := annotations["provisioning.hobbyfarm.io/packages"]; exists {
		config.Packages = strings.Split(packages, ",")
		for i := range config.Packages {
			config.Packages[i] = strings.TrimSpace(config.Packages[i])
		}
	}

	// Extract requirements
	if requirements, exists := annotations["provisioning.hobbyfarm.io/requirements"]; exists {
		config.Requirements = strings.Split(requirements, ",")
		for i := range config.Requirements {
			config.Requirements[i] = strings.TrimSpace(config.Requirements[i])
		}
	}

	// Extract variables (key=value format, one per line)
	if variables, exists := annotations["provisioning.hobbyfarm.io/variables"]; exists {
		lines := strings.Split(variables, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				config.Variables[key] = value
			}
		}
	}

	// If no playbooks specified, return nil to try scenario or use default
	if len(config.Playbooks) == 0 {
		return nil, fmt.Errorf("no playbooks specified in annotations")
	}

	return config, nil
}

// MODIFIED: Build inventory for existing user instead of session user
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

// MODIFIED: Session cleanup function - only clean up session workspace, not user
func (ar *AnsibleRunner) CleanupSession(vmIP string, sessionName string) error {
	log.Printf("🧹 Starting workspace cleanup for session %s on VM %s", sessionName, vmIP)

	// Detect SSH user
	sshUser, err := ar.detectSSHUser(vmIP)
	if err != nil {
		return fmt.Errorf("failed to detect SSH user for cleanup: %v", err)
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
	users := []string{"kube", "ubuntu", "admin"}
	
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

// Keep the old functions for compatibility but redirect them to new logic
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
	// Skip cloud-init check - it's unreliable and not necessary for existing user approach
	return true
}
