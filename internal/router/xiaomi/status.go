package xiaomi

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// Status source names, the keys of Status.Sources.
const (
	SourceStatus = "status" // misystem/status
	SourceWAN    = "wan"    // xqnetwork/wan_info
)

// CoverageState tells whether a source contributed to a Status.
type CoverageState string

// Coverage states.
const (
	CoverageAvailable   CoverageState = "available"
	CoverageUnavailable CoverageState = "unavailable"
)

// Coverage is the availability of one source. Reason is a short
// description of why it is unavailable (for example "http 404" or
// "code 1"); it never contains response content.
type Coverage struct {
	State  CoverageState
	Reason string
}

// Status is a merged observation. Pointer fields are nil when the router
// did not report a usable value, so that unknown is never confused with 0.
type Status struct {
	CPUPercent    *float64
	CPUCores      *int
	MemoryPercent *float64
	UptimeSeconds *float64
	// TemperatureC is nil on routers without a sensor, which report 0.
	TemperatureC *float64
	// Clients is the number of currently online clients.
	Clients *int
	WAN     WAN
	Sources map[string]Coverage
	// Model and Firmware come from misystem/status, falling back to the
	// latest init_info.
	Model    string
	Firmware string
}

// WAN is the router's WAN link and traffic. Addresses, DNS servers and MAC
// addresses are deliberately not read.
type WAN struct {
	Link            *bool
	Status          *int
	UptimeSeconds   *float64
	Type            string
	UpBytesPerSec   *float64
	DownBytesPerSec *float64
	UploadedBytes   *int64
	DownloadedBytes *int64
}

// statusAnswer lists only the misystem/status fields LCM uses. dev[] (client
// MACs and names) and hardware.mac/sn are deliberately absent.
type statusAnswer struct {
	CPU struct {
		Core flexNum `json:"core"`
		Load flexNum `json:"load"`
	} `json:"cpu"`
	Mem struct {
		Usage flexNum `json:"usage"`
	} `json:"mem"`
	UpTime      flexNum `json:"upTime"`
	Temperature flexNum `json:"temperature"`
	Count       struct {
		Online flexNum `json:"online"`
	} `json:"count"`
	WAN struct {
		UpSpeed   flexNum `json:"upspeed"`
		DownSpeed flexNum `json:"downspeed"`
		Upload    flexNum `json:"upload"`
		Download  flexNum `json:"download"`
	} `json:"wan"`
	Hardware struct {
		Platform string `json:"platform"`
		Version  string `json:"version"`
	} `json:"hardware"`
}

// wanAnswer lists only the xqnetwork/wan_info fields LCM uses; ipv4, DNS,
// gateway, MAC and PPPoE details are deliberately absent.
type wanAnswer struct {
	Info struct {
		Link    flexNum `json:"link"`
		Status  flexNum `json:"status"`
		Uptime  flexNum `json:"uptime"`
		Details struct {
			WANType string `json:"wanType"`
		} `json:"details"`
	} `json:"info"`
}

// Status logs in as needed and reads misystem/status and xqnetwork/wan_info.
// A source that fails is marked unavailable and the others still count; an
// error is returned when login fails or no source is available.
func (c *Client) Status(ctx context.Context) (Status, error) {
	st := Status{Sources: map[string]Coverage{}}
	relogged := false

	var sa statusAnswer
	cov, err := c.source(ctx, epStatus, &sa, &relogged)
	if err != nil {
		return Status{}, err
	}
	st.Sources[SourceStatus] = cov
	if cov.State == CoverageAvailable {
		applyStatus(&st, &sa)
	}

	var wa wanAnswer
	cov, err = c.source(ctx, epWAN, &wa, &relogged)
	if se, ok := errors.AsType[*sendError](err); ok && se.ep == epWAN && ctx.Err() == nil {
		// The router answered the first request, so a transport failure
		// here is specific to this source rather than an outage.
		cov, err = Coverage{State: CoverageUnavailable, Reason: "transport error"}, nil
	}
	if err != nil {
		return Status{}, err
	}
	st.Sources[SourceWAN] = cov
	if cov.State == CoverageAvailable {
		applyWAN(&st.WAN, &wa)
	}

	c.mu.Lock()
	if info := c.info; info != nil {
		if st.Model == "" {
			st.Model = info.Model
		}
		if st.Firmware == "" {
			st.Firmware = info.Firmware
		}
	}
	c.mu.Unlock()

	if st.Sources[SourceStatus].State != CoverageAvailable && st.Sources[SourceWAN].State != CoverageAvailable {
		return st, fmt.Errorf("xiaomi: %w: no status source available (%s: %s, %s: %s)", ErrUnavailable,
			SourceStatus, st.Sources[SourceStatus].Reason, SourceWAN, st.Sources[SourceWAN].Reason)
	}
	return st, nil
}

// source fetches one optional source. An unusable answer (HTTP error,
// non-JSON, non-zero code) marks the source unavailable; login and
// transport failures are returned as errors for the caller to classify.
func (c *Client) source(ctx context.Context, ep endpoint, v any, relogged *bool) (Coverage, error) {
	res, err := c.authed(ctx, c.http, ep, relogged)
	if err != nil {
		return Coverage{}, err
	}
	env, reason := decode(res, v)
	if reason == "" {
		if code, ok := env.Code.int(); !ok {
			reason = "missing code"
		} else if code != 0 {
			reason = "code " + strconv.FormatInt(code, 10)
		}
	}
	if reason != "" {
		return Coverage{State: CoverageUnavailable, Reason: reason}, nil
	}
	return Coverage{State: CoverageAvailable}, nil
}

func applyStatus(st *Status, sa *statusAnswer) {
	st.CPUPercent = percent(sa.CPU.Load)
	st.CPUCores = sa.CPU.Core.count()
	st.MemoryPercent = percent(sa.Mem.Usage)
	st.UptimeSeconds = sa.UpTime.nonNegative()
	if t, ok := sa.Temperature.float(); ok && t != 0 {
		st.TemperatureC = &t
	}
	st.Clients = sa.Count.Online.count()
	st.WAN.UpBytesPerSec = sa.WAN.UpSpeed.nonNegative()
	st.WAN.DownBytesPerSec = sa.WAN.DownSpeed.nonNegative()
	st.WAN.UploadedBytes = sa.WAN.Upload.bytes()
	st.WAN.DownloadedBytes = sa.WAN.Download.bytes()
	st.Model = sa.Hardware.Platform
	st.Firmware = sa.Hardware.Version
}

func applyWAN(w *WAN, wa *wanAnswer) {
	w.Link = wa.Info.Link.flag()
	if s, ok := wa.Info.Status.int(); ok && s >= -1<<31 && s < 1<<31 {
		v := int(s)
		w.Status = &v
	}
	w.UptimeSeconds = wa.Info.Uptime.nonNegative()
	w.Type = wa.Info.Details.WANType
}

// percent converts the router's 0..1 fractions.
func percent(n flexNum) *float64 {
	f := n.nonNegative()
	if f == nil {
		return nil
	}
	p := *f * 100
	return &p
}
