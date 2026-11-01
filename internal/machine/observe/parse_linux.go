package observe

import (
	"math"
	"strconv"
	"strings"
	"time"
)

func parseOS(b []byte) (OS, error) {
	values := make(map[string]string)
	for line := range strings.SplitSeq(string(b), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "ID" && key != "VERSION_ID") {
			continue
		}
		if _, exists := values[key]; exists {
			return OS{}, errInvalid
		}
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if len(value) > 128 || !tokenPattern.MatchString(value) {
			return OS{}, errInvalid
		}
		values[key] = value
	}
	if values["ID"] == "" {
		return OS{}, errInvalid
	}
	return OS{ID: values["ID"], VersionID: values["VERSION_ID"]}, nil
}

func number(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) || v < 0 {
		return 0, errInvalid
	}
	return v, nil
}

func parseUptime(b []byte) (time.Duration, error) {
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return 0, errInvalid
	}
	v, err := number(f[0])
	if err != nil || v >= float64(math.MaxInt64)/float64(time.Second) {
		return 0, errInvalid
	}
	if _, err = number(f[1]); err != nil {
		return 0, err
	}
	return time.Duration(v * float64(time.Second)), nil
}

func parseLoad(b []byte) (Load, error) {
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return Load{}, errInvalid
	}
	a, e1 := number(f[0])
	c, e2 := number(f[1])
	d, e3 := number(f[2])
	if e1 != nil || e2 != nil || e3 != nil {
		return Load{}, errInvalid
	}
	return Load{One: a, Five: c, Fifteen: d}, nil
}

func parseCPU(b []byte) (CPU, error) {
	var counters []uint64
	count := 0
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] == "cpu" {
			if counters != nil || len(f) < 5 || len(f) > 11 {
				return CPU{}, errInvalid
			}
			counters = make([]uint64, 10)
			for i, s := range f[1:] {
				v, err := strconv.ParseUint(s, 10, 64)
				if err != nil {
					return CPU{}, errInvalid
				}
				counters[i] = v
			}
		} else if strings.HasPrefix(f[0], "cpu") {
			if _, err := strconv.ParseUint(strings.TrimPrefix(f[0], "cpu"), 10, 32); err != nil {
				return CPU{}, errInvalid
			}
			count++
		}
	}
	if counters == nil || count == 0 {
		return CPU{}, errInvalid
	}
	return CPU{User: counters[0], Nice: counters[1], System: counters[2], Idle: counters[3],
		IOWait: counters[4], IRQ: counters[5], SoftIRQ: counters[6], Steal: counters[7],
		Guest: counters[8], GuestNice: counters[9], Count: count}, nil
}

func parseMemory(b []byte) (Memory, error) {
	values := make(map[string]uint64)
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "MemTotal:", "MemAvailable:", "SwapTotal:", "SwapFree:":
		default:
			continue
		}
		if len(f) != 3 || f[2] != "kB" {
			return Memory{}, errInvalid
		}
		if _, exists := values[f[0]]; exists {
			return Memory{}, errInvalid
		}
		v, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil || v > math.MaxUint64/1024 {
			return Memory{}, errInvalid
		}
		values[f[0]] = v * 1024
	}
	if len(values) != 4 || values["MemTotal:"] == 0 || values["MemAvailable:"] > values["MemTotal:"] || values["SwapFree:"] > values["SwapTotal:"] {
		return Memory{}, errInvalid
	}
	return Memory{TotalBytes: values["MemTotal:"], AvailableBytes: values["MemAvailable:"], SwapTotalBytes: values["SwapTotal:"], SwapFreeBytes: values["SwapFree:"]}, nil
}

func capacity(s FSStats) (Capacity, error) {
	if s.BlockSize == 0 || s.Blocks > math.MaxUint64/s.BlockSize || s.FreeBlocks > s.Blocks || s.AvailableBlocks > s.FreeBlocks || s.FreeFiles > s.Files {
		return Capacity{}, errInvalid
	}
	return Capacity{TotalBytes: s.Blocks * s.BlockSize, FreeBytes: s.FreeBlocks * s.BlockSize,
		AvailableBytes: s.AvailableBlocks * s.BlockSize, Inodes: s.Files, FreeInodes: s.FreeFiles}, nil
}

func parseLink(b []byte) (Link, error) {
	s := strings.TrimSpace(string(b))
	switch s {
	case "unknown", "notpresent", "down", "lowerlayerdown", "testing", "dormant", "up":
		return Link{OperState: s}, nil
	}
	return Link{}, errInvalid
}

func parsePSI(b []byte) (PSI, error) {
	var v PSI
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(line)
		if len(f) != 5 {
			return PSI{}, errInvalid
		}
		keys := []string{"avg10=", "avg60=", "avg300=", "total="}
		for i, k := range keys {
			if !strings.HasPrefix(f[i+1], k) {
				return PSI{}, errInvalid
			}
		}
		a, e1 := number(strings.TrimPrefix(f[1], keys[0]))
		c, e2 := number(strings.TrimPrefix(f[2], keys[1]))
		d, e3 := number(strings.TrimPrefix(f[3], keys[2]))
		total, e4 := strconv.ParseUint(strings.TrimPrefix(f[4], keys[3]), 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || a > 100 || c > 100 || d > 100 {
			return PSI{}, errInvalid
		}
		stall := &Stall{Avg10: a, Avg60: c, Avg300: d, TotalMicros: total}
		switch f[0] {
		case "some":
			if v.Some != nil {
				return PSI{}, errInvalid
			}
			v.Some = stall
		case "full":
			if v.Full != nil {
				return PSI{}, errInvalid
			}
			v.Full = stall
		default:
			return PSI{}, errInvalid
		}
	}
	if v.Some == nil {
		return PSI{}, errInvalid
	}
	return v, nil
}
