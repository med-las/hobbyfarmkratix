// internal/package_detector.go - Smart package detection based on course/session names
package internal

import (
    "context"
    "encoding/base64"
    "fmt"
    "log"
    "strings"
    
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/client-go/dynamic"
    "gopkg.in/yaml.v2"
)

type PackageRule struct {
    Keywords  []string          `yaml:"keywords"`
    Packages  []string          `yaml:"packages"`
    Variables map[string]string `yaml:"variables"`
    Playbooks []string          `yaml:"playbooks"`
}

type PackageDetectionConfig struct {
    PackageRules  []PackageRule     `yaml:"package_rules"`
    DefaultConfig PackageRule       `yaml:"default_config"`
}

type PackageDetector struct {
    client dynamic.Interface
    config *PackageDetectionConfig
}

func NewPackageDetector(client dynamic.Interface) *PackageDetector {
    detector := &PackageDetector{
        client: client,
    }
    detector.loadConfiguration()
    return detector
}

func (pd *PackageDetector) loadConfiguration() {
    // Try to load from ConfigMap
    configMap, err := pd.client.Resource(configMapGVR).Namespace("default").Get(
        context.TODO(), "package-detection-rules", metav1.GetOptions{})
    
    if err != nil {
        log.Printf("⚠️ Could not load package detection ConfigMap, using defaults: %v", err)
        pd.setDefaultConfiguration()
        return
    }
    
    rulesYAML, found, _ := unstructured.NestedString(configMap.Object, "data", "rules.yaml")
    if !found {
        log.Printf("⚠️ No rules.yaml found in ConfigMap, using defaults")
        pd.setDefaultConfiguration()
        return
    }
    
    var config PackageDetectionConfig
    if err := yaml.Unmarshal([]byte(rulesYAML), &config); err != nil {
        log.Printf("❌ Failed to parse package detection rules: %v", err)
        pd.setDefaultConfiguration()
        return
    }
    
    pd.config = &config
    log.Printf("✅ Loaded %d package detection rules from ConfigMap", len(config.PackageRules))
}

func (pd *PackageDetector) setDefaultConfiguration() {
    pd.config = &PackageDetectionConfig{
        PackageRules: []PackageRule{
            {
                Keywords:  []string{"devops", "docker", "kubernetes"},
                Packages:  []string{"docker.io", "kubectl", "helm"},
                Variables: map[string]string{"docker_install": "true", "k8s_tools": "true"},
                Playbooks: []string{"base.yaml", "dynamic.yaml"},
            },
            {
                Keywords:  []string{"web", "node", "javascript"},
                Packages:  []string{"nodejs", "npm", "nginx"},
                Variables: map[string]string{"node_version": "18"},
                Playbooks: []string{"base.yaml", "dynamic.yaml"},
            },
            {
                Keywords:  []string{"python", "django", "flask"},
                Packages:  []string{"python3", "python3-pip", "python3-venv"},
                Variables: map[string]string{"python_version": "3"},
                Playbooks: []string{"base.yaml", "dynamic.yaml"},
            },
        },
        DefaultConfig: PackageRule{
            Packages:  []string{},
            Variables: map[string]string{},
            Playbooks: []string{"base.yaml", "dynamic.yaml"},
        },
    }
}

// DetectPackagesFromSession - Main detection function
func (pd *PackageDetector) DetectPackagesFromSession(sessionName string) *ProvisioningConfig {
    log.Printf("🔍 Starting smart package detection for session: %s", sessionName)
    
    // Try multiple detection strategies
    strategies := []func(string) *ProvisioningConfig{
        pd.detectFromSessionName,
        pd.detectFromCourse,
        pd.detectFromScenario,
    }
    
    for i, strategy := range strategies {
        log.Printf("🎯 Trying detection strategy %d", i+1)
        if config := strategy(sessionName); config != nil {
            log.Printf("✅ Package detection successful with strategy %d", i+1)
            return config
        }
    }
    
    log.Printf("⚠️ No package rules matched, using default configuration")
    return pd.getDefaultProvisioningConfig()
}

// Strategy 1: Direct session name analysis
func (pd *PackageDetector) detectFromSessionName(sessionName string) *ProvisioningConfig {
    // Decode if base64 encoded
    decodedName := pd.decodeIfBase64(sessionName)
    normalizedName := strings.ToLower(decodedName)
    
    log.Printf("🔍 Analyzing session name: '%s' (decoded: '%s')", sessionName, decodedName)
    
    return pd.findMatchingRule(normalizedName)
}

// Strategy 2: Get course name from session
func (pd *PackageDetector) detectFromCourse(sessionName string) *ProvisioningConfig {
    // Get session to find course
    session, err := pd.client.Resource(sessionGVR).Namespace("hobbyfarm-system").Get(
        context.TODO(), sessionName, metav1.GetOptions{})
    if err != nil {
        log.Printf("⚠️ Could not get session %s: %v", sessionName, err)
        return nil
    }
    
    // Extract course from session (if available)
    courseID, found, _ := unstructured.NestedString(session.Object, "spec", "course")
    if !found {
        log.Printf("⚠️ No course found in session %s", sessionName)
        return nil
    }
    
    log.Printf("🔍 Found course ID: %s", courseID)
    
    // Get course details
    course, err := pd.client.Resource(courseGVR).Namespace("hobbyfarm-system").Get(
        context.TODO(), courseID, metav1.GetOptions{})
    if err != nil {
        log.Printf("⚠️ Could not get course %s: %v", courseID, err)
        return nil
    }
    
    // Extract course name and description
    courseName, _, _ := unstructured.NestedString(course.Object, "spec", "name")
    courseDesc, _, _ := unstructured.NestedString(course.Object, "spec", "description")
    
    // Decode base64 if needed
    decodedName := pd.decodeIfBase64(courseName)
    decodedDesc := pd.decodeIfBase64(courseDesc)
    
    log.Printf("🔍 Analyzing course: '%s' / '%s'", decodedName, decodedDesc)
    
    searchText := strings.ToLower(fmt.Sprintf("%s %s", decodedName, decodedDesc))
    return pd.findMatchingRule(searchText)
}

// Strategy 3: Get scenario name/description
func (pd *PackageDetector) detectFromScenario(sessionName string) *ProvisioningConfig {
    // Get session to find scenario
    session, err := pd.client.Resource(sessionGVR).Namespace("hobbyfarm-system").Get(
        context.TODO(), sessionName, metav1.GetOptions{})
    if err != nil {
        return nil
    }
    
    // Extract scenario from session
    scenarioID, found, _ := unstructured.NestedString(session.Object, "spec", "scenario")
    if !found {
        return nil
    }
    
    log.Printf("🔍 Found scenario ID: %s", scenarioID)
    
    // Get scenario details
    scenario, err := pd.client.Resource(scenarioGVR).Namespace("hobbyfarm-system").Get(
        context.TODO(), scenarioID, metav1.GetOptions{})
    if err != nil {
        return nil
    }
    
    // Extract scenario name and description
    scenarioName, _, _ := unstructured.NestedString(scenario.Object, "spec", "name")
    scenarioDesc, _, _ := unstructured.NestedString(scenario.Object, "spec", "description")
    
    // Decode base64 if needed
    decodedName := pd.decodeIfBase64(scenarioName)
    decodedDesc := pd.decodeIfBase64(scenarioDesc)
    
    log.Printf("🔍 Analyzing scenario: '%s' / '%s'", decodedName, decodedDesc)
    
    searchText := strings.ToLower(fmt.Sprintf("%s %s", decodedName, decodedDesc))
    return pd.findMatchingRule(searchText)
}

// Helper: Find matching rule based on keywords
func (pd *PackageDetector) findMatchingRule(searchText string) *ProvisioningConfig {
    log.Printf("🎯 Searching for keyword matches in: '%s'", searchText)
    
    for i, rule := range pd.config.PackageRules {
        for _, keyword := range rule.Keywords {
            if strings.Contains(searchText, strings.ToLower(keyword)) {
                log.Printf("✅ Found keyword match: '%s' (rule %d)", keyword, i+1)
                log.Printf("📦 Packages: %v", rule.Packages)
                log.Printf("🔧 Variables: %v", rule.Variables)
                
                return &ProvisioningConfig{
                    Playbooks:    rule.Playbooks,
                    Packages:     rule.Packages,
                    Requirements: []string{}, // Could be added to rules later
                    Variables:    rule.Variables,
                }
            }
        }
    }
    
    return nil
}

// Helper: Decode base64 if it's encoded
func (pd *PackageDetector) decodeIfBase64(input string) string {
    if decoded, err := base64.StdEncoding.DecodeString(input); err == nil {
        // Check if decoded result contains only printable ASCII characters
        decodedStr := string(decoded)
        if pd.isPrintableASCII(decodedStr) {
            return decodedStr
        }
    }
    return input
}

// Helper: Check if string contains only printable ASCII
func (pd *PackageDetector) isPrintableASCII(s string) bool {
    for _, r := range s {
        if r < 32 || r > 126 {
            return false
        }
    }
    return true
}

// Get default provisioning config
func (pd *PackageDetector) getDefaultProvisioningConfig() *ProvisioningConfig {
    return &ProvisioningConfig{
        Playbooks:    pd.config.DefaultConfig.Playbooks,
        Packages:     pd.config.DefaultConfig.Packages,
        Requirements: []string{},
        Variables:    pd.config.DefaultConfig.Variables,
    }
}

// Add missing GVR for ConfigMap
var (
    configMapGVR = schema.GroupVersionResource{
        Group:    "",
        Version:  "v1",
        Resource: "configmaps",
    }
    courseGVR = schema.GroupVersionResource{
        Group:    "hobbyfarm.io",
        Version:  "v1",
        Resource: "courses",
    }
)
