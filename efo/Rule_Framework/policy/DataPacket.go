package policy

import (
	i "1935/efo/LCM_Enabler/interfaces"
	"errors"
	"fmt"
	"strings"
)

type dataPacket struct {
	i.DataPacketInfo

	// producer is the node whose output this packet holds. It decides which
	// namespace wins a field-name clash when the edge sets no explicit order.
	producer string
	// carry names the namespaces this connection forwards; empty forwards all.
	carry []string
	// carried is the namespace map that survives this connection, kept for the
	// downstream node to accumulate onto. It is what travels; Output.Content is
	// the flattened view the downstream node reads.
	carried i.DataPacketContent

	ready chan bool
}

func newDataPacket(nodeName string, port int) *dataPacket {
	packet := dataPacket{}
	packet.Name = fmt.Sprintf("%s-output-%d", nodeName, port)
	packet.producer = nodeName
	packet.Input.InPort = -1
	packet.Input.Content = nil
	packet.Output.OutPort = -1
	packet.Output.Content = nil
	packet.TransFormat = nil
	packet.ready = make(chan bool, 1)
	return &packet
}

/*
 * transformer
 */

// transform resolves what crosses this connection.
//
// Two results come out of it and they are not the same thing: carried is the
// namespace map that continues downstream, and Output.Content is the flat view
// the receiving node actually reads. Keeping them apart is what lets a node
// rename a field for its own use without rewriting what the rest of the policy
// still sees under the original name.
func (packet *dataPacket) transform() error {
	if !packet.isValid() {
		return errors.New("input content is not valid")
	}

	selected := selectNamespaces(packet.Input.Content, packet.carry)
	flat := flattenNamespaces(selected, flattenOrder(selected, packet.carry, packet.producer))

	output, err := transformContent(flat, selected, packet.TransFormat)
	if err != nil {
		return err
	}
	packet.carried = selected
	packet.Output.Content = output

	return nil
}

// transformContent renames fields for the receiving node.
//
// flat is the flattened view; namespaced is the same data still grouped by the
// node that produced it, so a dotted source can reach a field the flattening
// overwrote -- "webhook-0.Value" when a later node also emitted Value.
//
// An empty (or nil) transFormat is pass-through. The copy is load-bearing, not
// an optimization to remove: EncodeOutput assigns the SAME map object to every
// output port, so returning src directly would alias that one map across every
// consumer and into the execution trace, breaking the invariant Trace.go's
// cloneContent relies on: every map in flight is freshly allocated per call.
func transformContent(
	flat i.DataPacketContent,
	namespaced i.DataPacketContent,
	transFormat i.TransFormat,
) (i.DataPacketContent, error) {
	if len(transFormat) == 0 {
		passthrough := make(i.DataPacketContent, len(flat))
		for key, value := range flat {
			passthrough[key] = value
		}
		return passthrough, nil
	}

	output := make(i.DataPacketContent)
	for _, format := range transFormat {
		srcFieldValue := resolveField(flat, namespaced, format.Src)
		if srcFieldValue == nil {
			return nil, errors.New("field " + format.Src + " not found in src")
		}
		output[format.Dst] = srcFieldValue
	}
	return output, nil
}

// resolveField looks a source path up in the flat view first, then across
// namespaces. A bare name is a field; a dotted name may be either a path into a
// nested value or a namespace-qualified field, and both are tried before giving
// up so neither form needs a different syntax.
func resolveField(flat i.DataPacketContent, namespaced i.DataPacketContent, src string) any {
	if value, found := flat[src]; found {
		return value
	}
	path := strings.Split(src, ".")
	if len(path) == 1 {
		return nil
	}
	if value := getMapFieldValue(namespaced, path); value != nil {
		return value
	}
	return getMapFieldValue(flat, path)
}

// getMapFieldValue walks a dotted field path.
//
// It used to stop at the first level: the comma-ok bool was read as an error and
// broke the loop as soon as a key was found, so "a.b" returned the whole of "a"
// and no path ever reached its second segment.
func getMapFieldValue(m map[string]any, fieldPath []string) any {
	var current any = m
	for _, path := range fieldPath {
		asMap, ok := asContent(current)
		if !ok {
			return nil
		}
		value, found := asMap[path]
		if !found {
			return nil
		}
		current = value
	}
	return current
}

/*
 * other methods
 */

func (packet *dataPacket) isValid() bool {
	return packet.Input.Content != nil
}

// clean resets the packet for the next run.
//
// It must only be called once the run that used this packet has been cancelled
// AND its walkers have left; runContext.end is what guarantees both.
//
// This used to close the readiness channel and install a fresh one, which was
// doing two unrelated jobs at once. Closing was the only way a walker parked in
// isReady ever got released, so it could not simply be dropped -- but it also
// panicked any walker that happened to be publishing a packet at that moment,
// and a walker goroutine has no recover, so that panic took the whole EFO
// process down. Cancellation now releases parked walkers through the run's done
// channel, which leaves this free to drain the channel instead of closing it,
// and to keep the same channel rather than swapping the field out from under a
// walker that had already loaded it.
func (packet *dataPacket) clean() {
	packet.Input.Content = nil
	packet.Output.Content = nil
	packet.carried = nil
	select {
	case <-packet.ready:
	default:
	}
}

func (packet *dataPacket) setInPort(port int) {
	packet.Input.InPort = port
}

func (packet *dataPacket) setOutPort(port int) {
	packet.Output.OutPort = port
}

func (packet *dataPacket) outPort() int {
	return packet.Output.OutPort
}

func (packet *dataPacket) setContent(content i.DataPacketContent) {
	packet.Input.Content = content
}

func (packet *dataPacket) content() i.DataPacketContent {
	return packet.Output.Content
}

func (packet *dataPacket) rawContent() i.DataPacketContent {
	if packet.Input.Content == nil {
		return i.DataPacketContent{}
	}
	return packet.Input.Content
}

func (packet *dataPacket) setTransFormat(transFormat i.TransFormat) {
	packet.TransFormat = transFormat
}

func (packet *dataPacket) setCarry(carry []string) {
	packet.carry = carry
}

// carriedNamespaces is what this connection forwards. It is empty until
// transform has run, which is the same point content() becomes meaningful.
func (packet *dataPacket) carriedNamespaces() i.DataPacketContent {
	if packet.carried == nil {
		return i.DataPacketContent{}
	}
	return packet.carried
}

// setReady publishes this packet to the walker waiting on it.
//
// The channel holds one token and a run writes each packet once, so the send
// normally completes immediately. Selecting on the run's cancellation is what
// covers the case where it cannot: nobody is left to receive after the run has
// been superseded, and an unguarded send would park this walker forever.
func (packet *dataPacket) setReady(done <-chan struct{}) {
	select {
	case packet.ready <- true:
	case <-done:
	}
}

// tryReady takes the readiness token if it is already there.
//
// Separating this from the blocking wait is what makes a module's quiescence
// precise. A walker is only parked once it has failed to take a token; counting
// it as parked before checking would let a module conclude its walk was over
// while a walker was in fact about to run.
func (packet *dataPacket) tryReady() (bool, bool) {
	select {
	case ready := <-packet.ready:
		return ready, true
	default:
		return false, false
	}
}

// isReady blocks until the producer has published this packet, and reports
// false when the walker must give up instead.
//
// Giving up is not an error path -- it is the normal end of a branch that was
// still waiting when a newer trigger event superseded the run, or when the
// policy was undeployed. Before cancellation existed such a branch simply never
// returned, so its goroutine lived until the process did.
func (packet *dataPacket) isReady(done <-chan struct{}) bool {
	select {
	case ready := <-packet.ready:
		return ready
	case <-done:
		return false
	}
}
