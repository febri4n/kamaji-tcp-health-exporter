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

    stopChans = make(map[string]chan bool) // Store channels to stop old goroutines
    mu        sync.Mutex                   // Mutex to protect concurrent access to stopChans
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
    services := make(map[string]string)
    for scanner.Scan() {
        line := scanner.Text()
        parts := strings.Fields(line)
        if len(parts) == 2 && parts[1] != "<none>" {
            services[parts[0]] = parts[1]
        }
    }
    return services, nil
}

// Check API health and response code
func checkAPI(name, ip string, stopChan chan bool) {
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
            return
        case <-ticker.C:
            resp, err := client.Get(url)
            if err != nil {
                apiHealthStatus.WithLabelValues(name, ip).Set(0)
                log.Printf("Error connecting to %s (%s): %v\n", name, ip, err)
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
        services, err := getServiceIPs()
        if err != nil {
            log.Printf("Error getting service IPs: %v\n", err)
            time.Sleep(1 * time.Minute)
            continue
        }

        mu.Lock()
        // Stop goroutines for services that no longer exist
        for name, stopChan := range stopChans {
            if _, exists := services[name]; !exists {
                close(stopChan)
                delete(stopChans, name)
            }
        }

        // Start new goroutines for services that are not being monitored yet
        for name, ip := range services {
            if _, exists := stopChans[name]; !exists {
                stopChan := make(chan bool)
                stopChans[name] = stopChan
                go checkAPI(name, ip, stopChan)
            }
        }
        mu.Unlock()

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
