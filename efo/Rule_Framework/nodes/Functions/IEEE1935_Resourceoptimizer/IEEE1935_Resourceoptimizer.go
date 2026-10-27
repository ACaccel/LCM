/*
Package IEEE1935_Resourceoptimizer redistributes CPU across clusters.

One execution collects each cluster's total resources and the per-service
request counts observed in Jaeger, hands both to an external
ResourceAllocationApp (PSO optimizer, reached at ip:port from the property),
and applies the returned assignment by patching each service's deployment
through the control plane.
*/
package IEEE1935_Resourceoptimizer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	i "1935/efo/Rule_Framework/interfaces"
	"1935/efo/Rule_Framework/nodes/Functions/calm"
	u "1935/efo/Rule_Framework/nodes/utils"
)

type Property struct {
	Ip           string `yaml:"ip"`
	Port         string `yaml:"port"`
	CollectDelay bool   `yaml:"collectdelay"`
	// AlloctionFactor scales each collected cluster's CPU and Memory before
	// they are sent to the ResourceAllocationApp: the fraction of the collected
	// total that may be allocated. A value <= 0 (including an omitted setting,
	// which decodes to 0) means 1.0.
	AlloctionFactor float64 `yaml:"alloctionfactor"`
}

type IEEE1935_Resourceoptimizer struct {
	i.FunctionNode
}

type OutputFormat struct {
	Status_code int
	Response    string
}

type OptimizedResource struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
}

type OptimizationResult struct {
	BestAssignment map[string]map[string]OptimizedResource `json:"best_assignment"`
	BestScore      float64                                 `json:"best_score"`
}

type CurrentDeployment struct {
	Replicas        string `json:"Replicas"`
	AllocatedMemory string `json:"Alocated_memory"`
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_Resourceoptimizer",
	NumInputPort:  1,
	NumOutputPort: 1,
}

// doJSON sends a request bound to ctx. Every call in this package goes through
// a run-scoped context: the optimize call alone may take minutes, and without
// cancellation an in-flight request would delay the next trigger event and any
// undeploy for its full timeout.
func doJSON(ctx context.Context, client *http.Client, method, url string, body io.Reader) (*http.Response, error) {
	return calm.DoJSON(ctx, client, method, url, body)
}

func (n *IEEE1935_Resourceoptimizer) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	var res *http.Response
	var output OutputFormat

	client := &http.Client{Timeout: 45 * time.Second}
	optimizeClient := &http.Client{Timeout: 3 * time.Minute}

	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	ctx, cancel := u.RunContext(info)
	defer cancel()

	// 1. Collect each cluster's total resources.
	clusterResource, err := n.FetchClusterResources(ctx, info.ControlPlaneBaseURL)
	if err != nil {
		output = OutputFormat{Status_code: 500, Response: err.Error()}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}

	factor := property.AlloctionFactor
	if factor <= 0 {
		factor = 1.0
	}
	for name, res := range clusterResource {
		res.CPU *= factor
		res.Memory *= factor
		clusterResource[name] = res
	}

	// 2. Collect the per-service request statistics.
	allRequests, err := n.FetchServicesRequest(ctx, info.ControlPlaneBaseURL, property.CollectDelay)
	if err != nil {
		fmt.Println("IEEE1935_Resourceoptimizer: Fetch services request error,", err)
		output = OutputFormat{Status_code: 500, Response: err.Error()}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	serviceSet := make(map[string]struct{})
	allocation := make(map[string]map[string]int)

	for clusterName, services := range allRequests {
		allocation[clusterName] = make(map[string]int)

		for serviceName, stats := range services {
			serviceSet[serviceName] = struct{}{}
			allocation[clusterName][serviceName] = stats.Number
		}
	}

	var serviceList []string
	for s := range serviceSet {
		serviceList = append(serviceList, s)
	}

	// 3. Feed the allocation app and ask it to optimize.
	allocation_app_url := fmt.Sprintf("http://%s:%s", property.Ip, property.Port)
	res, err = doJSON(ctx, client, http.MethodGet, allocation_app_url+"/ping", nil)
	if err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Cannot ping allocation app API: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	defer res.Body.Close()

	b, errMarshal := json.Marshal(serviceList)
	if errMarshal != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	res, err = doJSON(ctx, client, http.MethodPost, allocation_app_url+"/services/init", bytes.NewBuffer(b))
	if err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Error: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	defer res.Body.Close()
	fmt.Println("Initialized service list.")

	b, errMarshal = json.Marshal(clusterResource)
	if errMarshal != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	res, err = doJSON(ctx, client, http.MethodPost, allocation_app_url+"/clusters/init", bytes.NewBuffer(b))
	if err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Error: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	defer res.Body.Close()
	fmt.Println("Initialized cluster resources.")

	// send request allocation
	b, errMarshal = json.Marshal(allocation)
	if errMarshal != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	res, err = doJSON(ctx, client, http.MethodPost, allocation_app_url+"/clusters/requests", bytes.NewBuffer(b))
	if err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Error: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	defer res.Body.Close()
	fmt.Println("Updated request allocation.")
	fmt.Println("Request allocation:", string(b))

	// get optimized allocation
	res, err = doJSON(ctx, optimizeClient, http.MethodPost, allocation_app_url+"/optimize", nil)
	if err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Error: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)

	fmt.Println("Optimized allocation:", string(body))

	// 4. Apply the optimized allocation to every service.
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		output = OutputFormat{Status_code: res.StatusCode, Response: string(body)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}

	var optimizedResult OptimizationResult
	if err := json.Unmarshal(body, &optimizedResult); err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Decode optimize result error: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}

	if err := n.ApplyOptimizedAllocation(ctx, client, info.ControlPlaneBaseURL, optimizedResult); err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Apply optimized allocation error: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}

	output = OutputFormat{Status_code: res.StatusCode, Response: string(body)}
	fmt.Println("IEEE1935_ResourceOptimizer output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_ResourceOptimizer: Encode output error,", err)
	}

	return i.NodeExecResult{Output: encoded_output}
}

func (n *IEEE1935_Resourceoptimizer) ApplyOptimizedAllocation(ctx context.Context, client *http.Client, baseURL string, optimizedResult OptimizationResult) error {
	for clusterName, services := range optimizedResult.BestAssignment {
		for serviceName, resource := range services {
			if resource.CPU < 0.01 {
				fmt.Printf("Skip optimized allocation for %s/%s: CPU=%g\n", clusterName, serviceName, resource.CPU)
				continue
			}

			deployment, err := n.fetchCurrentDeployment(ctx, client, baseURL, clusterName, serviceName)
			if err != nil {
				return err
			}
			payload := map[string]string{
				"CPU":      strconv.FormatFloat(resource.CPU, 'f', -1, 64),
				"Memory":   deployment.AllocatedMemory,
				"Replicas": deployment.Replicas,
			}
			b, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("marshal scaling payload for %s/%s failed: %w", clusterName, serviceName, err)
			}

			req, err := http.NewRequestWithContext(
				ctx,
				http.MethodPatch,
				fmt.Sprintf(
					"%s/api/clusters/%s/services/%s",
					baseURL,
					url.PathEscape(clusterName),
					url.PathEscape(serviceName),
				),
				bytes.NewBuffer(b),
			)
			if err != nil {
				return fmt.Errorf("create scaling request for %s/%s failed: %w", clusterName, serviceName, err)
			}
			req.Header.Set("Content-Type", "application/json")

			res, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("scale service %s/%s failed: %w", clusterName, serviceName, err)
			}
			body, readErr := io.ReadAll(res.Body)
			res.Body.Close()
			if readErr != nil {
				return fmt.Errorf("read scaling response for %s/%s failed: %w", clusterName, serviceName, readErr)
			}
			if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
				return fmt.Errorf("scale service %s/%s returned %d: %s", clusterName, serviceName, res.StatusCode, string(body))
			}
			fmt.Printf("Applied optimized CPU allocation to %s/%s: CPU=%s (Memory unchanged: %s, Replicas unchanged: %s)\n", clusterName, serviceName, payload["CPU"], payload["Memory"], payload["Replicas"])
		}
	}
	return nil
}

func (n *IEEE1935_Resourceoptimizer) fetchCurrentDeployment(ctx context.Context, client *http.Client, baseURL, clusterName, serviceName string) (CurrentDeployment, error) {
	res, err := doJSON(ctx, client, http.MethodGet, fmt.Sprintf(
		"%s/api/clusters/%s/deployments/%s",
		baseURL,
		url.PathEscape(clusterName),
		url.PathEscape(serviceName),
	), nil)
	if err != nil {
		return CurrentDeployment{}, fmt.Errorf("fetch current deployment for %s/%s failed: %w", clusterName, serviceName, err)
	}
	defer res.Body.Close()

	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(res.Body)
		return CurrentDeployment{}, fmt.Errorf("fetch current deployment for %s/%s returned %d: %s", clusterName, serviceName, res.StatusCode, string(body))
	}

	var deployment CurrentDeployment
	if err := json.NewDecoder(res.Body).Decode(&deployment); err != nil {
		return CurrentDeployment{}, fmt.Errorf("decode current deployment for %s/%s failed: %w", clusterName, serviceName, err)
	}
	if deployment.Replicas == "" {
		return CurrentDeployment{}, fmt.Errorf("current deployment for %s/%s has empty replicas", clusterName, serviceName)
	}
	if deployment.AllocatedMemory == "" {
		return CurrentDeployment{}, fmt.Errorf("current deployment for %s/%s has empty allocated memory", clusterName, serviceName)
	}
	return deployment, nil
}
