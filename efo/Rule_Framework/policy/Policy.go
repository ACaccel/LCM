package policy

import (
	i "1935/efo/LCM_Enabler/interfaces"
	abi "1935/efo/Rule_Framework/interfaces"
	"1935/efo/Rule_Framework/nodes"
	u "1935/efo/Rule_Framework/nodes/utils"
	"errors"
	"log"
	"sync"
)

/*
 * Struct
 */

type Policy struct {
	Description struct {
		PolicyName string
	}
	Triggers     []*ListNode
	WebhookChMap map[string]chan any
	WebhookSubs  map[string][]chan any
	// TriggerCh is shared by every trigger; each event names its own trigger.
	TriggerCh chan abi.TriggerEvent
	// StartCh hands the policy over to its own goroutine. Deploy sends once and
	// blocks until Execute is listening, so a policy is never reported running
	// before it is.
	StartCh    chan bool
	PolicyList *PolicyList

	// triggers indexes Triggers by name. Filled during Init and read-only
	// afterwards, so the walkers share it without synchronization.
	triggers map[string]*ListNode

	// stop is closed once, by Stop. Closing is what makes shutdown a broadcast:
	// every trigger and the policy loop see it, and nobody can block delivering
	// it. triggerWG lets Stop wait for the trigger goroutines it signalled, so
	// undeploying is finished when it returns rather than merely requested.
	stop      chan struct{}
	stopOnce  sync.Once
	triggerWG sync.WaitGroup

	// run is the traversal currently walking the graph, or nil between runs.
	// Only Execute touches it, and only between events, so it needs no lock.
	run *runContext

	// trace records the latest execution; nil disables tracing.
	trace *Recorder
}

// runContext is one traversal of the graph: everything the engine needs to end
// it deterministically.
//
// A policy keeps one set of data packets and resets them between runs. That is
// only safe if no walker from the previous run can still touch one, and nothing
// used to guarantee that -- Execute reset the packets the moment a new event
// arrived, while the previous run was very possibly still walking. done is how
// those walkers are told to stop; walkers is how the engine knows they have.
type runContext struct {
	done    chan struct{}
	walkers sync.WaitGroup
}

func newRunContext() *runContext {
	return &runContext{done: make(chan struct{})}
}

// cancelled reports whether the run has been superseded or the policy stopped.
func (r *runContext) cancelled() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// end cancels the run and waits for every walker to leave.
//
// The wait terminates because each blocking point a walker can reach selects on
// done: waiting for an input (dataPacket.isReady), publishing an output
// (setReady), a module branch reporting back, and a Wait node's delay. Before
// that, a branch nobody joined -- the untaken side of an If, an input that never
// arrives -- simply blocked forever and its goroutine lived as long as the
// process did.
func (r *runContext) end() {
	close(r.done)
	r.walkers.Wait()
}

// spawn starts one walker and counts it. Add happens here rather than inside
// the goroutine so a scheduled-but-not-started walker cannot be missed by Wait.
func (r *runContext) spawn(node *ListNode, info abi.ExecInfo) {
	info.RunDone = r.done
	r.walkers.Add(1)
	go func() {
		defer r.walkers.Done()
		r.walk(node, info)
	}()
}

// StopCh is the channel a trigger watches to learn the policy has stopped.
func (p *Policy) StopCh() <-chan struct{} { return p.stop }

// Stop shuts the policy down and waits for its triggers to leave their loops.
//
// Undeploying used to send one value on a channel that every trigger goroutine
// was selecting on: exactly one of them received it and the rest ran on. The
// broadcast meant to cover for that could not, because a trigger parked handing
// an event over is not selecting at all, and the send blocked forever.
func (p *Policy) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	p.triggerWG.Wait()
}

// StartTrigger runs one trigger's loop and tracks it, so Stop can wait for it.
func (p *Policy) StartTrigger(trigger *ListNode, info abi.ExecInfo) {
	p.triggerWG.Add(1)
	go func() {
		defer p.triggerWG.Done()
		trigger.TriggerExecute(info)
	}()
}

/*
 * Method: Policy
 */

func (p *Policy) Init(pConfig i.PolicyConfig, modules map[string]i.ModuleConfig) error {
	return p.initWithModulePath(pConfig, modules, nil)
}

// initWithModulePath builds the policy, carrying the module names already being
// built further up the stack so initModule can reject a module that reaches
// itself before the recursion overflows the goroutine stack.
func (p *Policy) initWithModulePath(
	pConfig i.PolicyConfig,
	modules map[string]i.ModuleConfig,
	modulePath []string,
) error {
	p.Description.PolicyName = pConfig.PolicyName
	p.WebhookChMap = make(map[string]chan any)
	p.WebhookSubs = make(map[string][]chan any)
	p.TriggerCh = make(chan abi.TriggerEvent)
	p.StartCh = make(chan bool)
	p.stop = make(chan struct{})
	p.triggers = make(map[string]*ListNode)
	var policyList *PolicyList = new(PolicyList)
	policyList.init()
	// A non-empty module path means this list belongs to a module.
	if len(modulePath) > 0 {
		hostModulePolicy(policyList)
	}

	for _, triggerConfig := range pConfig.PolicyTrigger {
		trigger, err := nodes.NewTrigger(triggerConfig)
		if err != nil {
			return err
		}
		 
		triggerNode := policyList.initTrigger(trigger, triggerConfig.NodeName, triggerConfig.NodeType)
		if triggerConfig.NodeType == "Webhook" {
			ch := make(chan any)
			p.WebhookChMap[triggerConfig.NodeName] = ch
			// Subscribe this webhook trigger to its declared event topic so
			// ExecuteWebhook can dispatch matching events to it.
			var sub struct {
				EventType string `yaml:"event_type"`
			}
			// Left unchecked, a property that fails to decode subscribes the
			// trigger to the empty topic: the policy deploys, reports DEPLOYED,
			// and every webhook reports dispatched:0 with nothing to explain it.
			if err := u.GetProperty(triggerConfig.Property, &sub); err != nil {
				return errors.New("LCM: trigger " + triggerConfig.NodeName +
					" has an unreadable event_type: " + err.Error())
			}
			if sub.EventType == "" {
				return errors.New("LCM: trigger " + triggerConfig.NodeName +
					" declares no event_type, so no webhook could ever reach it")
			}
			p.WebhookSubs[sub.EventType] = append(p.WebhookSubs[sub.EventType], ch)
		}
		p.triggers[triggerConfig.NodeName] = triggerNode
		p.Triggers = append(p.Triggers, triggerNode)
	}

	for _, nodeConfig := range pConfig.PolicyNode {
		if nodeConfig.NodeType == "Module" {
			functionNode, numPort, err := initModule(nodeConfig, modules, modulePath)
			if err != nil {
				return err
			}
			// A module hosts its own policy and is entered once per arriving
			// branch, so it keeps the wait-for-everything default.
			policyList.initNode(functionNode, nodeConfig.NodeName, nodeConfig.NodeType, numPort, abi.JoinAll)
		} else {
			functionNode, numPort, join, err := nodes.NewNode(nodeConfig)
			if err != nil {
				return err
			}
			
			if functionNode == nil {
				log.Println("Policy: Node not found", nodeConfig.NodeName)
				continue
			}
			policyList.initNode(functionNode, nodeConfig.NodeName, nodeConfig.NodeType, numPort, join)
		}
	}

	for _, path := range pConfig.PolicyPath {
		err := policyList.connectNode(path)
		if err != nil {
			return err
		}
	}

	p.PolicyList = policyList
	return nil
}

// Execute is the policy's own loop, and there is exactly one of it.
//
// It used to be started once per trigger. All of those goroutines wrote the same
// Status field, all of them selected on the one channel undeploy signalled once,
// and all of them read the one TriggerCh with no way to tell whose event they
// had taken -- so an event could be walked from a different trigger's
// successors. One loop removes all three at the root: the state is a local, the
// stop signal is a closed channel, and the event says who sent it.
func (p *Policy) Execute(info abi.ExecInfo) {
	if running := <-p.StartCh; !running {
		return
	}
	for {
		select {
		case <-p.stop:
			p.endRun()
			return
		case event := <-p.TriggerCh:
			triggerNode, known := p.triggers[event.Node]
			if !known {
				log.Println("Policy: event from unknown trigger", event.Node)
				continue
			}
			// The previous run has to be over before its packets are reused,
			// not merely superseded. endRun is what makes the difference.
			p.endRun()
			p.run = newRunContext()
			runID := p.trace.StartRun(
				triggerNode.Description.NodeName,
				triggerNode.Description.NodeType,
				triggerContent(event.Content),
			)
			triggerNode.setTriggerReady(event.Content, p.run.done)
			runInfo := abi.ExecInfo{
				Service_Name:        info.Service_Name,
				File_Path:           info.File_Path,
				ControlPlaneBaseURL: info.ControlPlaneBaseURL,
				RunID:               runID,
			}

			if triggerNode.Next[0] != nil {
				p.trace.Spawn(runID)
				p.run.spawn(triggerNode.Next[0], runInfo)
			}
		}
	}
}

// endRun retires the current run and leaves the graph ready for the next one.
//
// The order is the whole point: cancel, wait, and only then reset. Resetting
// first is what used to kill the process, because clean() closed the readiness
// channel a live walker was about to send on.
func (p *Policy) endRun() {
	if p.run != nil {
		p.run.end()
		p.run = nil
	}
	p.Clean()
}

func (p *Policy) Clean() {
	for _, node := range p.PolicyList.NodeSet {
		node.clean()
	}
}

// walk is one walker: satisfy the node's inputs, run it, then continue along
// the ports it selected.
func (r *runContext) walk(node *ListNode, info abi.ExecInfo) {
	defer node.rec.Done(info.RunID)

	if !node.inputsSatisfied(info) {
		return
	}

	// execute node and update the status
	node.execute(info)

	// Successors spawned after cancellation would only find their inputs
	// cancelled too, so stopping here keeps a superseded run from fanning out
	// a burst of goroutines that exist purely to give up.
	if r.cancelled() {
		return
	}

	// execute next node
	for _, port := range node.nextPath {
		if node.Next[port] != nil {
			var newInfo = abi.ExecInfo{
				Service_Name:        info.Service_Name,
				File_Path:           info.File_Path,
				ControlPlaneBaseURL: info.ControlPlaneBaseURL,
				RunID:               info.RunID,
				ActiveInPort:        node.packet.output[port].outPort(),
			}
			node.rec.Spawn(info.RunID)
			r.spawn(node.Next[port], newInfo)
		}
	}
}
