package internal

import (
    "context"
    "log"
    "time"

    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
    "k8s.io/apimachinery/pkg/types"
    "k8s.io/client-go/dynamic"
)

func CleanupVMStatuses(client dynamic.Interface) map[string]bool {
    trainingVMs, _ := client.Resource(trainingVMGVR).Namespace("default").List(context.TODO(), metav1.ListOptions{})
    usedIPs := make(map[string]bool)

    for _, tvm := range trainingVMs.Items {
        ip, _, _ := unstructured.NestedString(tvm.Object, "status", "vmIP")
        state, _, _ := unstructured.NestedString(tvm.Object, "status", "state")

        if state == "allocated" {
            allocatedAt, found, _ := unstructured.NestedString(tvm.Object, "status", "allocatedAt")
            if found {
                t, err := time.Parse(time.RFC3339, allocatedAt)
                if err == nil && time.Since(t) > allocationTimeout {
                    log.Printf("♻️ Releasing expired VM %s", ip)
                    patch := `{"status":{"vmIP":"","state":"","allocatedAt":""}}`
                    client.Resource(trainingVMGVR).Namespace("default").Patch(
                        context.TODO(), tvm.GetName(), types.MergePatchType,
                        []byte(patch), metav1.PatchOptions{}, "status",
                    )
                    continue
                }
            }
        }

        if ip != "" {
            usedIPs[ip] = true
        }
    }

    return usedIPs
}
