package securityhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"

	"github.com/ACaccel/lcm/internal/model"
)

var ratePattern = regexp.MustCompile(`^[1-9][0-9]{0,4}r/[ms]$`)

func decodeSettings(raw json.RawMessage, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing settings")
	}
	return nil
}
func validateSettings(action string, raw json.RawMessage) error {
	invalid := func() error {
		return model.Issues{{Path: "settings", Message: "settings must contain exactly the typed fields and approved bounds for this action"}}
	}
	switch action {
	case "aide.check", "baseline.request":
		var value map[string]json.RawMessage
		if decodeSettings(raw, &value) != nil || value == nil || len(value) != 0 {
			return invalid()
		}
	case "aide.schedule":
		var value struct {
			Hour   *int `json:"hour"`
			Minute *int `json:"minute"`
		}
		if decodeSettings(raw, &value) != nil || value.Hour == nil || value.Minute == nil || *value.Hour < 0 || *value.Hour > 23 || *value.Minute < 0 || *value.Minute > 59 {
			return invalid()
		}
	case "nginx.configure":
		var value struct {
			Limits []Limit `json:"limits"`
		}
		if decodeSettings(raw, &value) != nil || len(value.Limits) != 4 {
			return invalid()
		}
		seen := map[string]bool{}
		for _, limit := range value.Limits {
			if seen[limit.Category] || !ratePattern.MatchString(limit.Rate) || limit.Burst < 1 || limit.Burst > 10000 || limit.Connections < 1 || limit.Connections > 4096 {
				return invalid()
			}
			switch limit.Category {
			case "auth", "api", "upload", "socket":
			default:
				return invalid()
			}
			seen[limit.Category] = true
		}
	case "audit.profile", "aide.profile":
		var value struct {
			Profiles []struct {
				Name    string `json:"name"`
				Enabled *bool  `json:"enabled"`
			} `json:"profiles"`
		}
		if decodeSettings(raw, &value) != nil || len(value.Profiles) < 1 || len(value.Profiles) > 16 {
			return invalid()
		}
		seen := map[string]bool{}
		for _, profile := range value.Profiles {
			if !identifier.MatchString(profile.Name) || profile.Enabled == nil || seen[profile.Name] {
				return invalid()
			}
			seen[profile.Name] = true
		}
	}
	return nil
}
