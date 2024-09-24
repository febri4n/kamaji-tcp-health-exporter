package main

import (
    "bufio"
    "bytes"
    "crypto/tls"
    "fmt"
    "log"
    "net/http"
    "os/exec"
    "strings"
    "sync"
    "time"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
    apiHealthStatus = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "api_health_status",
            Help: "Status of the API (1 for healthy, 0 for unhealthy)",
        },
        []string{"name", "ip"},
    )

    stopChans sync.Map
    services  sync.Map
)

func init() {
    prometheus.MustRegister(apiHealthStatus)
}

// Get the list of services and their external IPs
func getServiceIPs() (map[string]string, error) {
    cmd := exec.Command("kubectl", "-n", "kamaji-tcp", "get", "service", "-o", "custom-columns=NAME:.metadata.name,EXTERNAL-IP:.status.loadBalancer.ingress[0].ip", "--no-headers")
    var out bytes.Buffer
    cmd.Stdout = &out
    if err := cmd.Run(); err != nil {
        return nil, fmt.Errorf("failed to run kubectl command: %v", err)
    }

    scanner := bufio.NewScanner(&out)
    serviceMap := make(map[string]string)
    for scanner.Scan() {
        line := scanner.Text()
        parts := strings.Fields(line)
        if len(parts) == 2 && parts[1] != "<none>" {
            serviceMap[parts[0]] = parts[1]
        }
    }
    return serviceMap, nil
}

// Check API health and response code
func checkAPI(name, ip string, stopChan <-chan struct{}) {
    url := fmt.Sprintf("https://%s:6443", ip)
    tr := &http.Transport{
        TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
    }
    client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for {
        select {
        case <-stopChan:
            log.Printf("Stopping monitoring for %s (%s)\n", name, ip)
            apiHealthStatus.DeleteLabelValues(name, ip)
            services.Delete(name)
            return
        case <-ticker.C:
            resp, err := client.Get(url)
            if err != nil {
                log.Printf("Error connecting to %s (%s): %v\n", name, ip, err)
                apiHealthStatus.WithLabelValues(name, ip).Set(0)
                continue
            }
            apiHealthStatus.WithLabelValues(name, ip).Set(1)
            log.Printf("Successfully connected to %s (%s), status code: %d\n", name, ip, resp.StatusCode)
            resp.Body.Close()
        }
    }
}

// Update the services being monitored, start new goroutines if needed, and stop old ones
func updateServices() {
    for {
        serviceMap, err := getServiceIPs()
        if err != nil {
            log.Printf("Error getting service IPs: %v\n", err)
            time.Sleep(1 * time.Minute)
            continue
        }

        // Stop goroutines for services that no longer exist
        stopChans.Range(func(key, value interface{}) bool {
            name := key.(string)
            if _, exists := serviceMap[name]; !exists {
                close(value.(chan struct{}))
                stopChans.Delete(name)
                apiHealthStatus.DeleteLabelValues(name, serviceMap[name])
            }
            return true
        })

        // Start new goroutines for services that are not being monitored yet
        for name, ip := range serviceMap {
            if _, exists := stopChans.Load(name); !exists {
                stopChan := make(chan struct{})
                stopChans.Store(name, stopChan)
                go checkAPI(name, ip, stopChan)
            }
        }

        time.Sleep(1 * time.Minute) // Check for updates every minute
    }
}

func main() {
    go updateServices() // Start service updater

    // Expose the registered metrics via HTTP
    http.Handle("/metrics", promhttp.Handler())
    fmt.Println("Exporter is running on :8080")
    log.Fatal(http.ListenAndServe(":8080", nil))
}
