package internal

import (
    "log"
    "os"
    "path/filepath"

    "k8s.io/client-go/dynamic"
    "k8s.io/client-go/tools/clientcmd"
)

func InitKubeClient() dynamic.Interface {
    kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
    config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
    if err != nil {
        log.Fatalf("❌ Could not load kubeconfig: %v", err)
    }

    client, err := dynamic.NewForConfig(config)
    if err != nil {
        log.Fatalf("❌ Failed to create dynamic client: %v", err)
    }
    return client
}
