package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/api"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/hostsetup"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// ingressStatusTimeout bounds the read of the site status through the
// helper; the console shows the files as they are when it fails.
const ingressStatusTimeout = 10 * time.Second

// withIngressStatus completes the ingress component of the host status
// with what only the root helper can read: the digest of the site the host
// configuration renders, against the installed one, and the last result of
// applying it. Without a helper, or when it refuses (the component is off),
// the status stays what the unprivileged read found. The result is cached
// by the API for a short time, so the helper is not called per request.
func withIngressStatus(base api.HostStatusFunc, helper rt.DeployHelper) api.HostStatusFunc {
	if helper == nil {
		return base
	}
	return func() ([]hostsetup.ComponentStatus, error) {
		list, err := base()
		if err != nil {
			return nil, err
		}
		i := slices.IndexFunc(list, func(c hostsetup.ComponentStatus) bool { return c.Name == hostsetup.ComponentIngress })
		if i < 0 || list[i].State == hostsetup.StateNotInstalled {
			return list, nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), ingressStatusTimeout)
		defer cancel()
		resp, err := rt.CallHelper(ctx, helper, deployhelper.Request{Op: deployhelper.OpIngressStatus, OperationID: rt.NewOperationID()})
		if err != nil || resp.Ingress == nil {
			return list, nil
		}
		list[i] = completeIngress(list[i], *resp.Ingress)
		return list, nil
	}
}

// completeIngress adds the helper's view to the site file of the ingress
// component and recomputes the component state.
func completeIngress(cs hostsetup.ComponentStatus, st hostsetup.IngressStatus) hostsetup.ComponentStatus {
	files := slices.Clone(cs.Files)
	i := slices.IndexFunc(files, func(f hostsetup.FileStatus) bool { return f.Path == hostsetup.SiteFile })
	if i < 0 {
		return cs
	}
	f := files[i]
	if st.Installed != "" {
		f.Installed = st.Installed
	}
	var notes []string
	switch {
	case st.RenderError != "":
		f.State = hostsetup.StateUnknown
		notes = append(notes, "host.json cannot be rendered: "+st.RenderError)
	case st.Reverted:
		f.State = hostsetup.StateOutdated
		notes = append(notes, "reverted; sudo bin/lcm host apply installs the site again")
	default:
		f.Embedded = st.Rendered
		switch {
		case f.State == hostsetup.StateMissing:
		case st.Installed != st.Rendered:
			f.State = hostsetup.StateOutdated
			notes = append(notes, "differs from the site host.json renders")
		case !st.Enabled:
			f.State = hostsetup.StateOutdated
			notes = append(notes, "not enabled in nginx")
		default:
			f.State = hostsetup.StateUpToDate
		}
		if len(st.Dropped) > 0 {
			if f.State == hostsetup.StateUpToDate {
				f.State = hostsetup.StateOutdated
			}
			notes = append(notes, st.Dropped...)
		}
	}
	if l := st.Last; l != nil {
		note := fmt.Sprintf("last apply %s: %s", time.UnixMilli(l.At).UTC().Format(time.RFC3339), l.Outcome)
		if l.Message != "" {
			note += " (" + l.Message + ")"
		}
		notes = append(notes, note)
	}
	f.Detail = strings.Join(notes, "; ")
	files[i] = f
	cs.Files = files
	cs.State = hostsetup.ComponentState(files)
	return cs
}
