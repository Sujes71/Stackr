package portforward

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/stackr/stackr/internal/config"
)

// CheckKubectl returns true if kubectl is found in PATH.
func CheckKubectl() bool {
	_, err := exec.LookPath("kubectl")
	return err == nil
}

// CheckK8sAuth verifies kubectl can access the given context by running
// `kubectl cluster-info --context <ctx>`. Returns nil on success, or an
// error containing the stderr output on failure (expired token, etc.).
func CheckK8sAuth(context string) error {
	out, err := exec.Command("kubectl", "cluster-info", "--context", context).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// DiscoverContexts returns the list of available K8s context names
// by running: kubectl config get-contexts -o name.
func DiscoverContexts() ([]string, error) {
	out, err := exec.Command("kubectl", "config", "get-contexts", "-o", "name").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl: %w", err)
	}
	return splitLines(string(out)), nil
}

// DiscoverNamespaces returns the list of namespaces for the given
// K8s context by running: kubectl get namespaces --context <ctx> -o name.
// The "namespace/" prefix is stripped from each entry.
func DiscoverNamespaces(context string) ([]string, error) {
	out, err := exec.Command("kubectl", "get", "namespaces", "--context", context, "-o", "name").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl: %w", err)
	}
	namespaces := splitLines(string(out))
	for i := range namespaces {
		namespaces[i] = strings.TrimPrefix(namespaces[i], "namespace/")
	}
	return namespaces, nil
}

// DiscoverServices returns port-forward configurations for all services
// that expose at least one port in the given context and namespace.
// It runs: kubectl get svc -n <ns> --context <ctx> -o json.
//
// Services without any ports are filtered out. The returned PortForwards
// have Name, K8sContext, K8sNamespace, K8sService, and RemotePort set.
// LocalPort is left at zero (to be assigned by the caller).
func DiscoverServices(context, namespace string) ([]config.PortForward, error) {
	out, err := exec.Command("kubectl", "get", "svc", "-n", namespace, "--context", context, "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("kubectl: %w", err)
	}

	var result struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Ports []struct {
					Port int `json:"port"`
				} `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}

	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse k8s response: %w", err)
	}

	var forwards []config.PortForward
	for _, item := range result.Items {
		if len(item.Spec.Ports) == 0 {
			continue
		}
		pf := config.PortForward{
			Name:         item.Metadata.Name,
			K8sContext:   context,
			K8sNamespace: namespace,
			K8sService:   item.Metadata.Name,
			RemotePort:   item.Spec.Ports[0].Port,
		}
		forwards = append(forwards, pf)
	}
	return forwards, nil
}

// splitLines splits a string on newlines and trims space.
// Returns nil for empty or whitespace-only input.
func splitLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
