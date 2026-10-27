/*
Package Request_tracer walks the control plane's inventory — clusters, apps and
the services running in each cluster — and logs which services live where. It
is the exploratory sibling of the collector built into IEEE1935_Resourceoptimizer.
*/
package Request_tracer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	i "1935/efo/Rule_Framework/interfaces"
	u "1935/efo/Rule_Framework/nodes/utils"
)

type Property struct {
	Ip   string `yaml:"ip"`
	Port string `yaml:"port"`
}

type Request_tracer struct {
	i.FunctionNode
}

type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "Request_tracer",
	NumInputPort:  1,
	NumOutputPort: 1,
}

// get issues a GET bound to ctx, so a request still in flight when the run is
// retired is abandoned rather than holding the walker.
func get(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (n *Request_tracer) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	var res *http.Response
	var output OutputFormat

	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	ctx, cancel := u.RunContext(info)
	defer cancel()

	// fetch all clusters
	client := &http.Client{}
	res, err := get(ctx, client, fmt.Sprintf("%s/api/clusters", info.ControlPlaneBaseURL))
	if err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Error: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	defer res.Body.Close()

	var clusters []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&clusters); err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Cannot fetch clusters data: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}

	// fetch app name and its corresponding Id
	res, err = get(ctx, client, fmt.Sprintf("%s/api/apps", info.ControlPlaneBaseURL))
	if err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Error fetching apps data: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}
	defer res.Body.Close()

	var apps []map[string]any
	if err := json.NewDecoder(res.Body).Decode(&apps); err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Cannot parse apps data: %v", err)}
		encoded_output, _ := u.EncodeOutput(output, Description)
		return i.NodeExecResult{Output: encoded_output}
	}

	appNamesMap := make(map[string]string)
	for _, app := range apps {
		if appId, ok := app["Application_ID"].(string); ok {
			if appName, ok := app["Application_Name"].(string); ok {
				appNamesMap[appId] = appName
			}
		}
	}

	// fetch all services name in each cluster
	servicesInCluster := make(map[string][]string)

	for _, item := range clusters {
		if clusterName, ok := item["Cluster_Name"].(string); ok {
			res, err = get(ctx, client, fmt.Sprintf("%s/api/clusters/%s/services", info.ControlPlaneBaseURL, clusterName))
			if err != nil {
				output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Error fetching services for cluster %s: %v", clusterName, err)}
				encoded_output, _ := u.EncodeOutput(output, Description)
				return i.NodeExecResult{Output: encoded_output}
			}
			var services []map[string]any
			if err := json.NewDecoder(res.Body).Decode(&services); err != nil {
				output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Cannot parse services data for cluster %s: %v", clusterName, err)}
				encoded_output, _ := u.EncodeOutput(output, Description)
				return i.NodeExecResult{Output: encoded_output}
			}

			for _, svc := range services {
				serviceId := fmt.Sprint(svc["Service_ID"])
				if name, exists := appNamesMap[serviceId]; exists {
					servicesInCluster[clusterName] = append(servicesInCluster[clusterName], name)
				} else {
					// Log the orphan: a service without a matching app entry
					// would otherwise vanish from the listing silently.
					fmt.Printf("Unknown(%s)", serviceId)
				}
			}
			res.Body.Close()
		}
	}

	fmt.Printf("All app:\n %v\n", appNamesMap)
	fmt.Printf("All clusters:\n %v\n", clusters)
	fmt.Printf("Services in each cluster:\n %v\n", servicesInCluster)

	return i.NodeExecResult{Output: nil}
}
