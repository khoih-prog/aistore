// Package cli provides easy-to-use commands to manage, monitor, and utilize AIS clusters.
// This file contains util functions and types.
/*
 * Copyright (c) 2018-2022, NVIDIA CORPORATION. All rights reserved.
 */
package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmd/cli/tmpls"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/xact"
	"github.com/urfave/cli"
)

func xactionDesc(onlyStartable bool) string {
	xs := xact.ListDisplayNames(onlyStartable)
	return fmt.Sprintf("%s can be one of: %s", xactionArgument, strings.Join(xs, ", "))
}

func toMonitorMsg(c *cli.Context, xjid string) string {
	return toShowMsg(c, xjid, "To monitor the progress", false)
}

func toShowMsg(c *cli.Context, xjid, prompt string, verbose bool) string {
	// use command search
	cmds := findCmdMultiKeyAlt(commandShow, c.Command.Name)
	if len(cmds) == 0 {
		// generic
		cmds = findCmdMultiKeyAlt(commandShow, subcmdXaction)
	}
	for _, cmd := range cmds {
		if strings.HasPrefix(cmd, cliName+" "+commandShow+" ") {
			var sid, sv string
			if verbose {
				sv = " -v"
			}
			if xjid != "" {
				sid = " " + xjid
			}
			return fmt.Sprintf("%s, run '%s%s%s'", prompt, cmd, sid, sv)
		}
	}
	return ""
}

// Parse [TARGET_ID] [XACTION_ID|XACTION_KIND] [BUCKET]
// Relying on xact.IsValidKind and similar to differentiate (best-effort guess, in effect)
func parseXactionFromArgs(c *cli.Context) (nodeID, xactID, xactKind string, bck cmn.Bck, err error) {
	var smap *cluster.Smap
	smap, err = api.GetClusterMap(apiBP)
	if err != nil {
		return
	}

	var shift int
	what := argDaemonID(c)
	if node := smap.GetProxy(xactKind); node != nil {
		return "", "", "", bck, fmt.Errorf("node %q is a proxy (expecting target, see --help)", what)
	}
	if node := smap.GetTarget(what); node != nil {
		nodeID = what
		xactKind = c.Args().Get(1) // assuming ...
		shift++
	} else {
		xactKind = what // unless determined otherwise (see next)
	}

	var uri string
	if strings.Contains(xactKind, apc.BckProviderSeparator) {
		uri = xactKind
		xactKind = ""
	} else if !xact.IsValidKind(xactKind) {
		if cos.IsValidUUID(xactKind) {
			xactID = xactKind
		}
		xactKind = ""
	}

	what = c.Args().Get(1 + shift)
	if what == "" {
		return
	}

	if xactKind == "" && xact.IsValidKind(what) {
		xactKind = what
	} else if strings.Contains(what, apc.BckProviderSeparator) {
		uri = what
	} else if xactID == "" && cos.IsValidUUID(what) {
		xactID = what
	}

	if uri == "" || xactKind == "" {
		return
	}

	// validate bucket
	if xact.IsSameScope(xactKind, xact.ScopeB, xact.ScopeGB) {
		if bck, err = parseBckURI(c, uri); err != nil {
			return
		}
		if _, err = headBucket(bck, true /* don't add */); err != nil {
			return
		}
	} else {
		warn := fmt.Sprintf("%q is a non bucket-scope xaction, ignoring %q argument", xactKind, uri)
		actionWarn(c, warn)
	}
	return
}

// Wait for xaction to run for completion, warn if aborted
func waitForXactionCompletion(apiBP api.BaseParams, args api.XactReqArgs) (err error) {
	if args.Timeout == 0 {
		args.Timeout = time.Minute // TODO: make it a flag and an argument with configurable default
	}
	status, err := api.WaitForXactionIC(apiBP, args)
	if err != nil {
		return err
	}
	if status.Aborted() {
		return fmt.Errorf("xaction %q appears to be aborted", status.UUID)
	}
	return nil
}

func flattenXactStats(snap *xact.SnapExt) nvpairList {
	props := make(nvpairList, 0, 16)
	if snap == nil {
		return props
	}
	fmtTime := func(t time.Time) string {
		if t.IsZero() {
			return tmpls.NotSetVal
		}
		return t.Format("01-02 15:04:05")
	}
	_, xname := xact.GetKindName(snap.Kind)
	if xname != snap.Kind {
		props = append(props, nvpair{Name: ".display-name", Value: xname})
	}
	props = append(props,
		// Start xaction properties with a dot to make them first alphabetically
		nvpair{Name: ".id", Value: snap.ID},
		nvpair{Name: ".kind", Value: snap.Kind},
		nvpair{Name: ".bck", Value: snap.Bck.String()},
		nvpair{Name: ".start", Value: fmtTime(snap.StartTime)},
		nvpair{Name: ".end", Value: fmtTime(snap.EndTime)},
		nvpair{Name: ".aborted", Value: fmt.Sprintf("%t", snap.AbortedX)},
	)
	if snap.Stats.Objs != 0 || snap.Stats.Bytes != 0 {
		props = append(props,
			nvpair{Name: "loc.obj.n", Value: fmt.Sprintf("%d", snap.Stats.Objs)},
			nvpair{Name: "loc.obj.size", Value: formatStatHuman(".size", snap.Stats.Bytes)},
		)
	}
	if snap.Stats.InObjs != 0 || snap.Stats.InBytes != 0 {
		props = append(props,
			nvpair{Name: "in.obj.n", Value: fmt.Sprintf("%d", snap.Stats.InObjs)},
			nvpair{Name: "in.obj.size", Value: formatStatHuman(".size", snap.Stats.InBytes)},
		)
	}
	if snap.Stats.Objs != 0 || snap.Stats.Bytes != 0 {
		props = append(props,
			nvpair{Name: "out.obj.n", Value: fmt.Sprintf("%d", snap.Stats.OutObjs)},
			nvpair{Name: "out.obj.size", Value: formatStatHuman(".size", snap.Stats.OutBytes)},
		)
	}
	if extStats, ok := snap.Ext.(map[string]any); ok {
		for k, v := range extStats {
			var value string
			if strings.HasSuffix(k, ".size") {
				val := v.(string)
				if i, err := strconv.ParseInt(val, 10, 64); err == nil {
					value = cos.B2S(i, 2)
				}
			}
			if value == "" {
				value = fmt.Sprintf("%v", v)
			}
			props = append(props, nvpair{Name: k, Value: value})
		}
	}
	sort.Slice(props, func(i, j int) bool {
		return props[i].Name < props[j].Name
	})
	return props
}

func getXactSnap(xactArgs api.XactReqArgs) (*xact.SnapExt, error) {
	xs, err := api.QueryXactionSnaps(apiBP, xactArgs)
	if err != nil {
		return nil, err
	}
	for _, snaps := range xs {
		for _, snap := range snaps {
			return snap, nil
		}
	}
	return nil, nil
}

func queryXactions(xactArgs api.XactReqArgs) (xs api.NodesXactMultiSnap, err error) {
	xs, err = api.QueryXactionSnaps(apiBP, xactArgs)
	if err != nil {
		return
	}
	if xactArgs.OnlyRunning {
		for tid, snaps := range xs {
			if len(snaps) == 0 {
				continue
			}
			runningStats := xs[tid][:0]
			for _, xctn := range snaps {
				if xctn.Running() {
					runningStats = append(runningStats, xctn)
				}
			}
			xs[tid] = runningStats
		}
	}

	if xactArgs.DaemonID != "" {
		var found bool
		for tid := range xs {
			if tid == xactArgs.DaemonID {
				found = true
				break
			}
		}
		if !found {
			return
		}
		// remove all other targets
		for tid := range xs {
			if tid != xactArgs.DaemonID {
				delete(xs, tid)
			}
		}
	}
	return
}