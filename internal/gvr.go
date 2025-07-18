package internal

import (
    "time"
    "k8s.io/apimachinery/pkg/runtime/schema"
)

var (
    sessionGVR = schema.GroupVersionResource{
        Group:    "hobbyfarm.io",
        Version:  "v1",
        Resource: "sessions",
    }
    scenarioGVR = schema.GroupVersionResource{
        Group:    "hobbyfarm.io",
        Version:  "v1",
        Resource: "scenarios",
    }
    trainingVMGVR = schema.GroupVersionResource{
        Group:    "training.example.com",
        Version:  "v1",
        Resource: "trainingvms",
    }
    trainingVMRequestGVR = schema.GroupVersionResource{
        Group:    "training.example.com",
        Version:  "v1",
        Resource: "trainingvmrequests",
    }

    vmPool = []string{
        "192.168.2.37",
        "192.168.2.38",
    }

    allocationTimeout = time.Hour
)
