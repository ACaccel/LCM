package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/upgrademarker"
)

// HostUpgradeActor is the actor of a deployment recorded from the marker of
// "lcm host upgrade".
var HostUpgradeActor = auth.Actor{Kind: auth.KindSystem, ID: "host-upgrade", Name: "lcm host upgrade"}

// HostUpgradeResult says what RecordHostUpgrade did with a marker.
type HostUpgradeResult int

// Results of RecordHostUpgrade.
const (
	// HostUpgradeRecorded: a deployment entry was added.
	HostUpgradeRecorded HostUpgradeResult = iota
	// HostUpgradeCurrent: the newest successful deployment already is the
	// marker's build, so there is nothing to add.
	HostUpgradeCurrent
	// HostUpgradeForeign: the marker names a build other than the running
	// one, so it says nothing about what this process runs.
	HostUpgradeForeign
	// HostUpgradeNoResource: the service does not exist.
	HostUpgradeNoResource
)

// RecordHostUpgrade makes the deployment history of the service agree with an
// upgrade that "lcm host upgrade" attested through its marker. It adds a
// successful deployment of the marker's build only when the marker names
// running, the commit of the process that reads it: the catalog records what
// the root command attested and this process observes, nothing else. The
// entry has the action "deploy" (the deployments table knows no other) and
// the actor HostUpgradeActor. It is safe to call again: once the entry
// exists the service is current.
func (c *Catalog) RecordHostUpgrade(ctx context.Context, name string, m upgrademarker.Marker, running string) (HostUpgradeResult, error) {
	if !upgrademarker.SameCommit(m.To.Commit, running) {
		return HostUpgradeForeign, nil
	}
	if _, err := c.Get(ctx, name); err != nil {
		if errors.Is(err, ErrNotFound) {
			return HostUpgradeNoResource, nil
		}
		return 0, err
	}
	deps, err := c.Deployments(ctx, name, 100, 0)
	if err != nil {
		return 0, err
	}
	last := lastSuccessful(deps)
	if last != "" && upgrademarker.SameCommit(last, m.To.Commit) {
		return HostUpgradeCurrent, nil
	}
	previous := last
	if previous == "" {
		previous = m.From.Commit
	}
	id, err := c.BeginDeployment(ctx, Deployment{
		Resource: name, Action: ActionDeploy, Version: m.To.Commit, PreviousVersion: previous,
		StartedAt: m.At, Actor: HostUpgradeActor,
	})
	if err != nil {
		return 0, err
	}
	log := fmt.Sprintf("recorded from the marker of lcm host upgrade: %s (%s, storage schema %d) -> %s (%s, storage schema %d), upgraded at %s\n",
		m.From.Version, m.From.Commit, m.From.Schema, m.To.Version, m.To.Commit, m.To.Schema, m.At.UTC().Format("2006-01-02T15:04:05Z"))
	if err := c.FinishDeployment(ctx, id, OutcomeSucceeded, "", log); err != nil {
		return 0, err
	}
	// The entry is the record; a failing summary line must not undo it.
	actx := auth.WithActor(ctx, HostUpgradeActor)
	if err := c.audit.Record(actx, "service.host-upgrade", name, audit.OutcomeOK,
		map[string]any{"deployment": id, "from": m.From.Commit, "to": m.To.Commit}); err != nil {
		c.log.Error("write audit entry failed", "service", name, "action", "host-upgrade", "error", err)
	}
	err = c.AppendTimeline(ctx, Entry{
		Resource: name, Kind: KindDeployment,
		Summary: "deploy succeeded at " + shortSHA(m.To.Commit) + " (lcm host upgrade)",
		Data: map[string]any{
			"action": ActionDeploy, "outcome": "succeeded", "actor": HostUpgradeActor.Name,
			"version": m.To.Commit, "previousVersion": previous, "deploymentId": id,
		},
		Ref: fmt.Sprint(id),
	})
	if err != nil {
		c.log.Error("append timeline entry failed", "service", name, "action", "host-upgrade", "error", err)
	}
	return HostUpgradeRecorded, nil
}
