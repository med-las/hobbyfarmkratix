// internal/utils.go - Complete updated file with SSH user detection
package internal

import (
    "net"
    "strings"
    "time"
)

// isPublicIP determines if an IP address is public (EC2) or private (local VM)
func isPublicIP(ip string) bool {
    if ip == "" {
        return false
    }
    
    // Parse IP to validate it's actually an IP address
    parsedIP := net.ParseIP(ip)
    if parsedIP == nil {
        return false
    }
    
    // Private IP ranges (RFC 1918)
    if strings.HasPrefix(ip, "192.168.") ||
       strings.HasPrefix(ip, "10.") {
        return false
    }
    
    // 172.16.0.0 to 172.31.255.255 (172.16/12)
    if strings.HasPrefix(ip, "172.") {
        parts := strings.Split(ip, ".")
        if len(parts) >= 2 {
            second := parts[1]
            // Check if second octet is between 16-31
            if second >= "16" && second <= "31" {
                return false
            }
        }
    }
    
    // Link-local addresses (169.254.0.0/16)
    if strings.HasPrefix(ip, "169.254.") {
        return false
    }
    
    // Loopback (127.0.0.0/8)
    if strings.HasPrefix(ip, "127.") {
        return false
    }
    
    // If none of the above private ranges, assume it's public
    return true
}

// getVMType returns a string describing the VM type
func getVMType(ip string) string {
    if isPublicIP(ip) {
        return "EC2"
    }
    return "static"
}

// GetVMTypeFromIP - exported version for external use
func GetVMTypeFromIP(ip string) string {
    return getVMType(ip)
}

// getSSHUsername returns the correct SSH username based on VM type
func getSSHUsername(ip string) string {
    if isPublicIP(ip) {
        return "ubuntu" // EC2 instances typically use ubuntu
    }
    return "kube" // Local VMs use kube
}

// GetSSHUsername - exported version for external use
func GetSSHUsername(ip string) string {
    return getSSHUsername(ip)
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

// validateSSHUsername checks if the SSH username is correct for the given IP
func validateSSHUsername(ip, currentUsername string) (bool, string) {
    correctUsername := getSSHUsername(ip)
    return currentUsername == correctUsername, correctUsername
}

// isVMReachable checks if a VM is reachable via SSH
func isVMReachable(ip string) bool {
    // For EC2 instances (public IPs), give more time and try different approaches
    if isPublicIP(ip) {
        return isEC2Reachable(ip)
    }
    
    // For local VMs, use the original quick check
    return isLocalVMReachable(ip)
}

func isLocalVMReachable(ip string) bool {
    timeout := 5 * time.Second
    conn, err := net.DialTimeout("tcp", ip+":22", timeout)
    if err != nil {
        return false
    }
    conn.Close()
    return true
}

func isEC2Reachable(ip string) bool {
    // For EC2 instances, use longer timeout and multiple attempts
    timeout := 15 * time.Second
    maxAttempts := 3
    
    for attempt := 1; attempt <= maxAttempts; attempt++ {
        conn, err := net.DialTimeout("tcp", ip+":22", timeout)
        if err == nil {
            conn.Close()
            return true
        }
        
        // Wait between attempts for EC2 instances (they take longer to boot)
        if attempt < maxAttempts {
            time.Sleep(10 * time.Second)
        }
    }
    
    return false
}
