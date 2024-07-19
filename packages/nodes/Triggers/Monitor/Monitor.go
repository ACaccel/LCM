package monitor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	i "lcm/packages/interfaces"
	u "lcm/packages/nodes/utils"
)

type InputFormat struct {
	Label         string
	OriginalProps struct {
		IP string
	}
	UpdatedProps struct {
		Cpu_total    float64
		Cpu_usage    float64
		Mem_total    float64
		Mem_usage    float64
		Disk_total   float64
		Disk_usage   float64
		Network_recv uint64
		Network_sent uint64
		Rtt          []struct {
			SourceIP []byte
			DestIP   string
			Distance float64
		}
	}
}

type Property struct {
	Interval 	float64
	Cpu_usage 	[]map[string]float64
	Mem_usage 	[]map[string]float64
	Disk_usage 	[]map[string]float64
}

var Description = i.TriggerDescription{
	TriggerType: "MonitorEvent",
}

type Monitor struct {
	i.TriggerNode
}

func (n *Monitor) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.GetProperty(n.Config.Property, &property)

	ticker := time.NewTicker(time.Duration((time.Duration(property.Interval) * time.Second)))
	go func() {
		status := true
		for status {
			select {
			case <-ticker.C:
				fmt.Println("MonitorEvent: Execute node", n.Config.NodeName)

				// send request to monitoring tool
				res, err := http.Get("http://localhost:4000/realTime")
				if err != nil {
					fmt.Println("MonitorEvent: Get monitor info error,", err)
				}
				resBody, err := io.ReadAll(res.Body)
				if err != nil {
					fmt.Println("MonitorEvent: Read response body error,", err)
				}
				var input InputFormat
				if err := json.Unmarshal(resBody, &input); err != nil {
					fmt.Println("MonitorEvent: Decode response body error,", err)
				}


				// event trigger
				event := true
				for _, condition := range property.Cpu_usage {
					for k, v := range condition {
						if !u.OperatorExec(k, input.UpdatedProps.Cpu_usage, v) {
							event = false
						}
					}
				}
				for _, condition := range property.Mem_usage {
					for k, v := range condition {
						if !u.OperatorExec(k, input.UpdatedProps.Mem_usage, v) {
							event = false
						}
					}
				}
				for _, condition := range property.Disk_usage {
					for k, v := range condition {
						if !u.OperatorExec(k, input.UpdatedProps.Disk_usage, v) {
							event = false
						}
					}
				}

				if event {
					output, err := u.EncodeTriggerOutput(input)
					if err != nil {
						fmt.Println("MonitorEvent: Encode output error,", err)
					}
					fmt.Println("MonitorEvent: output", output)
					info.TriggerCh <- output
				}
			case policyStatus := <-info.StatusCh:
				status = policyStatus
				if !status {
					ticker.Stop()
				}
			}
		}
	}()

	return i.NodeExecResult{}
}
