package internal

import (
    "net"
    "time"
)

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
