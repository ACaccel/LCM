// Package engine compiles policies into immutable plans and executes runs
// of them on a shared, bounded worker pool with SQLite checkpoints.
//
// # Compile
//
// Deploy and Validate decode a Policy (package model) and compile it: module
// nodes are expanded into scoped ids ("outer/inner/check"), node types,
// ports, edges, cycles and trigger rules are checked, props are validated
// against the node schema, and every expression is compiled with its
// nodes.<id> references checked to be strict ancestors in the same scope.
// The resulting Plan is shared by all runs of that policy version.
//
// Modules: a module node's ports are the ports of the module's
// module.output nodes (one node per port). Edges into the module node enter
// its module.input node, whose output is the module's resolved parameters
// and becomes params inside the module. Parameter values are expressions
// of the calling scope. Inside a module, nodes.<id> refers to module nodes;
// in the caller, nodes.<module node> is the output of whichever
// module.output ran. Module nodes have no error port and no timeout or
// retry of their own.
//
// # Scheduling and dead-path elimination
//
// A run starts at one trigger node, whose output is the event; every other
// node without inbound edges is dead. When a node completes it selects one
// port: edges of that port become live and all its other edges dead. A
// node with joinMode all (the default) becomes ready once every inbound
// edge is resolved and at least one is live, and is dead if all are dead;
// joinMode any becomes ready on the first live edge. Dead nodes resolve all
// their outgoing edges as dead. Branches that rejoin therefore run the join
// node exactly once, after every live branch has finished, without any
// waiting primitive.
//
// Ready nodes from all runs share one FIFO scheduler that round-robins
// between runs, executed by Config.Workers goroutines. Nothing waits on a
// worker: retries and suspensions use timers and re-queue the node.
//
// # Errors, retries and timeouts
//
// A failed attempt is retried retry.max times with exponential backoff
// (capped by maxBackoff, with jitter) unless the error is node.Permanent.
// Expression and props errors are permanent. After the last attempt the
// node's error edge, when present, receives {message, attempts} as the
// node output and all other ports are dead; otherwise the run fails and
// in-flight nodes are cancelled. Node timeouts apply per attempt; the run
// timeout counts from the run's start, including suspended time.
//
// # Suspend and resume
//
// A node that returns Result.Suspend is parked without holding a worker.
// It is resumed by Engine.Resume with the matching key or by its ResumeAt
// timer, and its Run is called again with RunContext.Resumed set (the
// attempt number does not change). A run whose only remaining nodes are
// suspended has status suspended. Suspensions and stored resumes survive
// restarts.
//
// # Durability and recovery
//
// Run creation is written synchronously. Node state changes are written by
// a single journal goroutine that group-commits them; a node's downstream
// nodes start only after its completion is committed. Write nodes also
// store a running row before their Run is called. Only state that recovery
// or the trace needs is stored; dead and pending nodes are derived. See
// Engine.Recover for the restart rules.
//
// # Modes, interception and observers
//
// In dry mode write nodes call DryRun, or output {planned: true, props}
// without running. An Interceptor may supply any node's result (replay).
// Observers receive lifecycle events asynchronously and never block the
// engine.
//
// # Concurrency modes
//
// queue (one active run, later events wait in order), drop (events arriving
// while a run is active are stored as dropped runs), replace (the active run
// is cancelled and the new one starts once it has stopped; older queued
// events are replaced as well) and parallel (up to limit active runs).
// Suspended runs count as active.
package engine
