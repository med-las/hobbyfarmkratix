// internal/webhook.go - FIXED VERSION: Add vmRequestGVR
package internal

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "net/http"
    "strings"

    admissionv1 "k8s.io/api/admission/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/runtime/schema"
    "k8s.io/client-go/dynamic"
)

// Local vmRequestGVR for webhook (to avoid conflicts)
var (
    webhookVMRequestGVR = schema.GroupVersionResource{
        Group:    "vm.hobbyfarm.io",
        Version:  "v1",
        Resource: "vmrequests",
    }
)

type WebhookServer struct {
    client dynamic.Interface
    server *http.Server
}

func NewWebhookServer(client dynamic.Interface, port string) *WebhookServer {
    ws := &WebhookServer{
        client: client,
    }

    mux := http.NewServeMux()
    mux.HandleFunc("/mutate", ws.mutateHandler)
    mux.HandleFunc("/health", ws.healthHandler)

    ws.server = &http.Server{
        Addr:    ":" + port,
        Handler: mux,
    }

    return ws
}

func (ws *WebhookServer) Start() error {
    log.Printf("🌐 Starting webhook server on %s", ws.server.Addr)
    return ws.server.ListenAndServe()
}

func (ws *WebhookServer) healthHandler(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    w.Write([]byte("OK"))
}

func (ws *WebhookServer) mutateHandler(w http.ResponseWriter, r *http.Request) {
    var body []byte
    if r.Body != nil {
        if data, err := io.ReadAll(r.Body); err == nil {
            body = data
        }
    }

    var review admissionv1.AdmissionReview
    if err := json.Unmarshal(body, &review); err != nil {
        log.Printf("❌ Could not unmarshal admission review: %v", err)
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }

    response := ws.processAdmissionReview(&review)
    
    respBytes, err := json.Marshal(response)
    if err != nil {
        log.Printf("❌ Could not marshal admission response: %v", err)
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    w.Write(respBytes)
}

func (ws *WebhookServer) processAdmissionReview(review *admissionv1.AdmissionReview) *admissionv1.AdmissionReview {
    req := review.Request
    response := &admissionv1.AdmissionResponse{
        UID:     req.UID,
        Allowed: true,
    }

    // Check if this is a VirtualMachineClaim creation
    if req.Kind.Kind == "VirtualMachineClaim" && req.Operation == admissionv1.Create {
        log.Printf("🎯 Intercepting VirtualMachineClaim creation")
        
        var vmClaim unstructured.Unstructured
        if err := json.Unmarshal(req.Object.Raw, &vmClaim); err != nil {
            log.Printf("❌ Could not unmarshal VirtualMachineClaim: %v", err)
            response.Allowed = false
            response.Result = &metav1.Status{
                Message: fmt.Sprintf("Could not unmarshal object: %v", err),
            }
            return &admissionv1.AdmissionReview{Response: response}
        }

        // Create VMRequest instead of allowing the VirtualMachineClaim
        if err := ws.createVMRequestFromClaim(&vmClaim); err != nil {
            log.Printf("❌ Failed to create VMRequest: %v", err)
            response.Allowed = false
            response.Result = &metav1.Status{
                Message: fmt.Sprintf("Failed to create VMRequest: %v", err),
            }
        } else {
            log.Printf("✅ Successfully created VMRequest from VirtualMachineClaim")
            // Deny the original VirtualMachineClaim since we've created a VMRequest instead
            response.Allowed = false
            response.Result = &metav1.Status{
                Code:    http.StatusOK,
                Message: "Redirected to hybrid provisioner VMRequest",
            }
        }
    }

    return &admissionv1.AdmissionReview{Response: response}
}

func (ws *WebhookServer) createVMRequestFromClaim(vmClaim *unstructured.Unstructured) error {
    // Extract information from VirtualMachineClaim
    claimName := vmClaim.GetName()
    namespace := vmClaim.GetNamespace()
    
    // Extract user and session from labels or annotations
    labels := vmClaim.GetLabels()
    annotations := vmClaim.GetAnnotations()
    
    user := ""
    session := ""
    scenario := ""
    
    if labels != nil {
        user = labels["hobbyfarm.io/user"]
        session = labels["hobbyfarm.io/session"]
        scenario = labels["hobbyfarm.io/scenario"]
    }
    
    // Fallback to annotations
    if user == "" && annotations != nil {
        user = annotations["hobbyfarm.io/user"]
    }
    if session == "" && annotations != nil {
        session = annotations["hobbyfarm.io/session"]
    }
    if scenario == "" && annotations != nil {
        scenario = annotations["hobbyfarm.io/scenario"]
    }

    // Extract VM template and environment info
    vmTemplate, _, _ := unstructured.NestedString(vmClaim.Object, "spec", "virtualMachineTemplate")
    environment, _, _ := unstructured.NestedString(vmClaim.Object, "spec", "environment")

    // Get scenario information to extract provisioning config
    provisioningConfig := ws.getProvisioningConfigFromScenario(scenario)

    // Create VMRequest
    vmRequestName := fmt.Sprintf("vmreq-%s", session)
    vmRequest := &unstructured.Unstructured{
        Object: map[string]interface{}{
            "apiVersion": "vm.hobbyfarm.io/v1",
            "kind":       "VMRequest",
            "metadata": map[string]interface{}{
                "name":      vmRequestName,
                "namespace": namespace,
                "labels": map[string]interface{}{
                    "hobbyfarm.io/user":       user,
                    "hobbyfarm.io/session":    session,
                    "hobbyfarm.io/scenario":   scenario,
                    "hobbyfarm.io/environment": environment,
                    "hobbyfarm.io/vmtemplate": vmTemplate,
                    "hobbyfarm.io/claim":      claimName,
                    "provisioner":             "hybrid-provisioner",
                },
                "annotations": map[string]interface{}{
                    "hobbyfarm.io/original-claim": claimName,
                    "hobbyfarm.io/integration":    "webhook-redirect",
                },
            },
            "spec": map[string]interface{}{
                "user":           user,
                "session":        session,
                "scenario":       scenario,
                "vmTemplate":     vmTemplate,
                "timeout":        600,
                "preferStaticVM": true,
                "provisioning":   provisioningConfig,
            },
        },
    }

    _, err := ws.client.Resource(webhookVMRequestGVR).Namespace(namespace).Create(
        context.TODO(), vmRequest, metav1.CreateOptions{})
    
    if err != nil {
        return fmt.Errorf("failed to create VMRequest: %v", err)
    }

    log.Printf("✅ Created VMRequest %s for user %s, session %s", vmRequestName, user, session)
    return nil
}

func (ws *WebhookServer) getProvisioningConfigFromScenario(scenarioName string) map[string]interface{} {
    if scenarioName == "" {
        return ws.getDefaultProvisioningConfig()
    }

    // Try to get scenario from cluster
    scenario, err := ws.client.Resource(scenarioGVR).Namespace("default").Get(
        context.TODO(), scenarioName, metav1.GetOptions{})
    if err != nil {
        // Try hobbyfarm-system namespace
        scenario, err = ws.client.Resource(scenarioGVR).Namespace("hobbyfarm-system").Get(
            context.TODO(), scenarioName, metav1.GetOptions{})
        if err != nil {
            log.Printf("⚠️ Could not get scenario %s, using defaults: %v", scenarioName, err)
            return ws.getDefaultProvisioningConfig()
        }
    }

    annotations := scenario.GetAnnotations()
    if annotations == nil {
        return ws.getDefaultProvisioningConfig()
    }

    config := map[string]interface{}{}

    // Extract playbooks
    if playbooks, exists := annotations["provisioning.hobbyfarm.io/playbooks"]; exists {
        config["playbooks"] = strings.Split(playbooks, ",")
    } else {
        config["playbooks"] = []string{"base.yaml", "dynamic.yaml"}
    }

    // Extract packages
    if packages, exists := annotations["provisioning.hobbyfarm.io/packages"]; exists {
        packageList := strings.Split(packages, ",")
        cleanPackages := make([]string, 0, len(packageList))
        for _, pkg := range packageList {
            if trimmed := strings.TrimSpace(pkg); trimmed != "" {
                cleanPackages = append(cleanPackages, trimmed)
            }
        }
        config["packages"] = cleanPackages
    } else {
        config["packages"] = []string{}
    }

    // Extract requirements
    if requirements, exists := annotations["provisioning.hobbyfarm.io/requirements"]; exists {
        reqList := strings.Split(requirements, ",")
        cleanReqs := make([]string, 0, len(reqList))
        for _, req := range reqList {
            if trimmed := strings.TrimSpace(req); trimmed != "" {
                cleanReqs = append(cleanReqs, trimmed)
            }
        }
        config["requirements"] = cleanReqs
    } else {
        config["requirements"] = []string{}
    }

    // Extract variables
    if variables, exists := annotations["provisioning.hobbyfarm.io/variables"]; exists {
        varMap := make(map[string]string)
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
                varMap[key] = value
            }
        }
        config["variables"] = varMap
    } else {
        config["variables"] = map[string]string{}
    }

    return config
}

func (ws *WebhookServer) getDefaultProvisioningConfig() map[string]interface{} {
    return map[string]interface{}{
        "playbooks":    []string{"base.yaml", "dynamic.yaml"},
        "packages":     []string{},
        "requirements": []string{},
        "variables":    map[string]string{},
    }
}

// Start webhook server in a goroutine
func StartWebhookServer(client dynamic.Interface, port string) {
    webhookServer := NewWebhookServer(client, port)
    
    go func() {
        if err := webhookServer.Start(); err != nil && err != http.ErrServerClosed {
            log.Fatalf("❌ Webhook server failed to start: %v", err)
        }
    }()
    
    log.Printf("🌐 Webhook server started on port %s", port)
}
