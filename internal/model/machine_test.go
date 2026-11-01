package model_test

import (
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/model"
)

func TestMachineDocument(t *testing.T) {
	base := "apiVersion: lcm/v1\nkind: Machine\nmetadata: {name: workstation}\nspec: "
	m, err := model.DecodeMachine([]byte(base + "{}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Spec.Host != "local" || m.Spec.Monitoring.Interval.D().Seconds() != 30 {
		t.Fatalf("defaults: %+v", m.Spec)
	}
	b, err := model.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = model.DecodeMachine(b); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"{host: ssh}", "{connectionRef: remote}", "{monitoring: {interval: 1ms}}", "{monitoring: {journal: true}}", "{monitoring: {units: [--danger.service]}}", "{monitoring: {filesystems: [/tmp/../etc]}}", "{monitoring: {thresholds: {cpuPercent: .nan}}}"} {
		if _, err := model.DecodeMachine([]byte(base + bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := model.DecodeMachine([]byte(strings.Replace(string(b), `"kind":"Machine"`, `"kind":"Service"`, 1))); err == nil {
		t.Fatal("wrong kind accepted")
	}
}
