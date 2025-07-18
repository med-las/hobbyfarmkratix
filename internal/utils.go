// internal/utils.go - Shared utility functions
package internal

import (
    "strings"
    "time"
)

// isPublicIP determines if an IP address is public (EC2) or private (local VM)
func isPublicIP(ip string) bool {
	// Simple heuristic: if it's not in private ranges, consider it public
	return !strings.HasPrefix(ip, "192.168.") && 
		   !strings.HasPrefix(ip, "10.") && 
		   !strings.HasPrefix(ip, "172.")
}

// getVMType returns a string describing the VM type
func getVMType(ip string) string {
	if isPublicIP(ip) {
		return "EC2"
	}
	return "static"
}

// getBootWaitTime returns appropriate boot wait time based on VM type
func getBootWaitTime(ip string) time.Duration {
	if isPublicIP(ip) {
		return 2 * time.Minute // EC2 instances need more time
	}
	return 30 * time.Second // Static VMs boot faster
}

// getSSHTimeout returns appropriate SSH timeout based on VM type
func getSSHTimeout(ip string) time.Duration {
	if isPublicIP(ip) {
		return 5 * time.Minute // EC2 instances need more time for SSH
	}
	return 2 * time.Minute // Static VMs should be ready faster
}
