// Package cli provides easy-to-use commands to manage, monitor, and utilize AIS clusters.
// This file contains util functions and types.
/*
 * Copyright (c) 2023-2025, NVIDIA CORPORATION. All rights reserved.
 */
package cli

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmd/cli/teb"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/stats"

	"github.com/urfave/cli"
)

type (
	perfcb func(c *cli.Context,
		metrics cos.StrKVs, mapBegin, mapEnd teb.StstMap, elapsed time.Duration) bool
)

// true when called by top-level handler
var allPerfTabs bool

var verboseCounters = [...]string{
	stats.LcacheCollisionCount,
	stats.LcacheEvictedCount,
	stats.LcacheFlushColdCount,
	cos.StreamsOutObjCount,
	cos.StreamsOutObjSize,
	cos.StreamsInObjCount,
	cos.StreamsInObjSize,

	// NOTE: including (not to confuse with `stats.IOErrGetCount`)
	stats.ErrGetCount,
}

var (
	showPerfFlags = sortFlags(append(
		longRunFlags,
		noHeaderFlag,
		regexColsFlag,
		unitsFlag,
		averageSizeFlag,
		nonverboseFlag,
		verboseFlag,
	))

	// `show performance` command
	showCmdPerformance = cli.Command{
		Name:      commandPerf,
		Usage:     showPerfArgument,
		ArgsUsage: optionalTargetIDArgument,
		Flags:     showPerfFlags,
		Action:    showPerfHandler,
		Subcommands: []cli.Command{
			showCounters,
			showThroughput,
			showLatency,
			showCmdMpathCapacity,
			makeAlias(&showCmdDisk, &mkaliasOpts{newName: cmdShowDisk}),
		},
	}
	showCounters = cli.Command{
		Name: cmdShowCounters,
		Usage: "Show (GET, PUT, DELETE, RENAME, EVICT, APPEND) object counts, as well as:\n" +
			indent2 + "\t- numbers of list-objects requests;\n" +
			indent2 + "\t- (GET, PUT, etc.) cumulative and average sizes;\n" +
			indent2 + "\t- associated error counters, if any, and more.",
		ArgsUsage:    optionalTargetIDArgument,
		Flags:        showPerfFlags,
		Action:       showCountersHandler,
		BashComplete: suggestTargets,
	}
	showThroughput = cli.Command{
		Name:         cmdShowThroughput,
		Usage:        "Show GET and PUT throughput, associated (cumulative, average) sizes and counters",
		ArgsUsage:    optionalTargetIDArgument,
		Flags:        showPerfFlags,
		Action:       showThroughputHandler,
		BashComplete: suggestTargets,
	}
	showLatency = cli.Command{
		Name:         cmdShowLatency,
		Usage:        "Show GET, PUT, and APPEND latencies and average sizes",
		ArgsUsage:    optionalTargetIDArgument,
		Flags:        showPerfFlags,
		Action:       showLatencyHandler,
		BashComplete: suggestTargets,
	}
	showCmdMpathCapacity = cli.Command{
		Name:         cmdCapacity,
		Usage:        "Show target mountpaths, disks, and used/available capacity",
		ArgsUsage:    optionalTargetIDArgument,
		Flags:        append(showPerfFlags, mountpathFlag),
		Action:       showMpathCapHandler,
		BashComplete: suggestTargets,
	}
)

func showPerfHandler(c *cli.Context) error {
	allPerfTabs = true // global (TODO: consider passing as param)

	if argIsFlag(c, 1) {
		return fmt.Errorf("misplaced flags in %v (hint: change the order of arguments or %s specific view)",
			c.Args(), tabtab)
	}

	if err := showCountersHandler(c); err != nil {
		return err
	}
	fmt.Fprintln(c.App.Writer)

	if err := showThroughputHandler(c); err != nil {
		return err
	}
	fmt.Fprintln(c.App.Writer)

	if err := showLatencyHandler(c); err != nil {
		return err
	}
	fmt.Fprintln(c.App.Writer)

	return nil
}

func _warnThruLatIters(c *cli.Context) {
	if flagIsSet(c, refreshFlag) || flagIsSet(c, nonverboseFlag) {
		return
	}
	warn := fmt.Sprintf("for better results, use %s option and/or run several iterations\n", qflprn(refreshFlag))
	actionWarn(c, warn)
}

func perfCptn(c *cli.Context, tab string) {
	stamp := cos.FormatNowStamp()
	repeat := 40 - len(stamp) - len(tab)
	actionCptn(c, tab, strings.Repeat("-", repeat), stamp)
}

// show non-zero counters _and_ sizes (unless `allColumnsFlag`)
func showCountersHandler(c *cli.Context) error {
	metrics, err := getMetricNames(c)
	if err != nil {
		return err
	}
	var (
		selected = make(cos.StrKVs, len(metrics))
		regexStr = parseStrFlag(c, regexColsFlag)
		verbose  = flagIsSet(c, verboseFlag)
	)
	for name, kind := range metrics {
		if metrics[name] == stats.KindCounter || metrics[name] == stats.KindSize {
			//
			// skip assorted internal counters and sizes, unless verbose or regex
			//
			if !verbose && regexStr == "" {
				if cos.StringInSlice(name, verboseCounters[:]) {
					continue
				}
			}
			selected[name] = kind
		}
	}
	return showPerfTab(c, selected, nil, cmdShowCounters, nil /*totals*/, false)
}

func showThroughputHandler(c *cli.Context) error {
	var (
		totals       = make(map[string]int64, 12) // throughput metrics ("columns") to tally up
		verbose      = flagIsSet(c, verboseFlag)
		metrics, err = getMetricNames(c)
	)
	if err != nil {
		return err
	}

	_warnThruLatIters(c)

	// - select metrics to include in the 'ais performance throughput' table
	// - add a few as well to show locally computed throughput
	// - for naming conventions, see stats/common
	selected := make(cos.StrKVs, len(metrics))

	for name, kind := range metrics {
		// - always show io-errors
		// - other errors only if (get|put) and verbose
		// - otherwise, skip anything other than the two relevant kinds
		if stats.IsIOErrMetric(name) {
			selected[name] = kind
			continue
		}
		if stats.IsErrMetric(name) {
			if !verbose {
				continue
			}
			if !strings.Contains(name, "get") && !strings.Contains(name, "put") {
				continue
			}
			selected[name] = kind
			continue
		}

		// - take (get, put) counters and the corespoinding (total) sizes
		// - compute via _throughput() callback
		switch kind {
		case stats.KindCounter:
			if name == stats.GetCount || name == stats.PutCount ||
				strings.HasSuffix(name, "."+stats.GetCount) || strings.HasSuffix(name, "."+stats.PutCount) {
				selected[name] = kind
			}
		case stats.KindSize:
			if name == stats.GetSize || name == stats.PutSize ||
				strings.HasSuffix(name, "."+stats.GetSize) || strings.HasSuffix(name, "."+stats.PutSize) {
				selected[name] = kind

				if bpsName, _ := stats.SizeToThroughputCount(name, stats.KindSize); bpsName != "" {
					selected[bpsName] = stats.KindThroughput
					totals[bpsName] = 0
				}
			}
		}
	}
	// `true` to show average get/put sizes
	return showPerfTab(c, selected, _throughput /*cb*/, cmdShowThroughput, totals, true)
}

// update mapBegin <= (size/s)
func _throughput(c *cli.Context, metrics cos.StrKVs, mapBegin, mapEnd teb.StstMap, elapsed time.Duration) (idle bool) {
	var (
		seconds = max(int64(elapsed.Seconds()), 1) // averaging per second
		num     int
	)
	for tid, begin := range mapBegin {
		end := mapEnd[tid]
		if end == nil {
			warn := fmt.Sprintf("missing %s in the get-stats-and-status results\n", meta.Tname(tid))
			actionWarn(c, warn)
			continue
		}
		for name, v := range begin.Tracker {
			kind, ok := metrics[name]
			if !ok || kind != stats.KindSize {
				continue
			}
			bpsName, cntName := stats.SizeToThroughputCount(name, stats.KindSize)
			if bpsName == "" {
				continue
			}

			// - check (begin, end) counters
			// - zero-out resulting throughput when no change
			var (
				cntBegin, okb = begin.Tracker[cntName]
				cntEnd, oke   = end.Tracker[cntName]
			)
			if okb && oke && cntBegin.Value >= cntEnd.Value {
				debug.Assert(cntBegin.Value == cntEnd.Value, cntName, ": ", cntBegin.Value, " vs ", cntEnd.Value)
				v.Value = 0
				begin.Tracker[bpsName] = v
				continue
			}

			//
			// given this (KindSize) metric change and elapsed time, add computed throughput:
			//
			vend := end.Tracker[name]
			v.Value = (vend.Value - v.Value) / seconds
			begin.Tracker[bpsName] = v
			num++
		}
	}

	idle = num == 0
	return idle
}

// otherwise, skip computing (TODO: add comdline option)
const miLatencyCntChange = 4

func showLatencyHandler(c *cli.Context) error {
	verbose := flagIsSet(c, verboseFlag)
	metrics, err := getMetricNames(c)
	if err != nil {
		return err
	}

	_warnThruLatIters(c)

	// statically filter metrics (names):
	// take sizes and latencies that _map_ to their respective counters
	// for naming conventions, see stats/common

	selected := make(cos.StrKVs, len(metrics))
	for name, kind := range metrics {
		if name == stats.GetSize || name == stats.PutSize {
			selected[name] = kind
			continue
		}
		// skipping internal/computed latency; computing here over GetLatencyTotal instead
		if kind == stats.KindLatency {
			continue
		}

		// - always show io-errors
		// - other errors only if (get|put) and verbose
		// - otherwise, skip anything other than the two relevant kinds
		if stats.IsIOErrMetric(name) {
			selected[name] = kind
			continue
		}
		if stats.IsErrMetric(name) {
			if !verbose {
				continue
			}
			if !strings.Contains(name, "get") && !strings.Contains(name, "put") {
				continue
			}
			selected[name] = kind
			continue
		}
		if kind != stats.KindTotal {
			continue
		}

		// respective counter
		ncounter := stats.LatencyToCounter(name)
		if ncounter == "" {
			continue
		}
		selected[name] = kind
		// show counter itself as well (todo: maybe only when verbose)
		selected[ncounter] = stats.KindCounter
	}

	// `true` to show (and put request latency numbers in perspective)
	return showPerfTab(c, selected, _latency, cmdShowLatency, nil /*totals*/, true)
}

// update mapBegin <= (elapsed/num-samples)
func _latency(c *cli.Context, metrics cos.StrKVs, mapBegin, mapEnd teb.StstMap, _ time.Duration) (idle bool) {
	var num int // num computed latencies
	for tid, begin := range mapBegin {
		end := mapEnd[tid]
		if end == nil {
			warn := fmt.Sprintf("missing %s in the get-stats-and-status results\n", meta.Tname(tid))
			actionWarn(c, warn)
			continue
		}
		for name, v := range begin.Tracker {
			kind, ok := metrics[name]
			if !ok {
				continue
			}
			if kind != stats.KindLatency && kind != stats.KindTotal {
				continue
			}
			vend := end.Tracker[name]
			ncounter := stats.LatencyToCounter(name)
			if ncounter == "" {
				continue
			}
			if cntBegin, ok1 := begin.Tracker[ncounter]; ok1 {
				if cntEnd, ok2 := end.Tracker[ncounter]; ok2 && cntEnd.Value > cntBegin.Value {
					if cntEnd.Value-cntBegin.Value >= miLatencyCntChange {
						// (cumulative-end-time - cumulative-begin-time) / num-requests
						v.Value = (vend.Value - v.Value) / (cntEnd.Value - cntBegin.Value)
						begin.Tracker[name] = v
						num++
						continue
					}
				}
			}
			// no changes, nothing to show
			v.Value = 0
			begin.Tracker[name] = v
		}
	}
	idle = num == 0
	return idle
}

// (main method)
func showPerfTab(c *cli.Context, metrics cos.StrKVs, cb perfcb, tag string, totals map[string]int64, inclAvgSize bool) error {
	var (
		regex       *regexp.Regexp
		regexStr    = parseStrFlag(c, regexColsFlag)
		hideHeader  = flagIsSet(c, noHeaderFlag)
		units, errU = parseUnitsFlag(c, unitsFlag)
	)
	if errU != nil {
		return errU
	}
	avgSize := flagIsSet(c, averageSizeFlag)
	if inclAvgSize {
		avgSize = true // caller override
	}
	var (
		tid          string
		node, _, err = arg0Node(c)
	)
	if err != nil {
		return err
	}
	if node != nil {
		debug.Assert(node.IsTarget())
		tid = node.ID()
	}
	if regexStr != "" {
		regex, err = regexp.Compile(regexStr)
		if err != nil {
			return err
		}
	}

	// TODO: target only - won't show proxies' put/get etc. counters and error counters (e.g. keep-alive)
	smap, tstatusMap, _, err := fillNodeStatusMap(c, apc.Target)
	if err != nil {
		return err
	}

	params := getLongRunParams(c)
	if params != nil {
		if params.mapBegin == nil {
			params.mapBegin = tstatusMap
		} else {
			params.mapEnd = tstatusMap
		}
	}

	if numTs := smap.CountActiveTs(); numTs == 1 || tid != "" {
		totals = nil // sum implies multiple
	} else if numTs == 0 {
		return cmn.NewErrNoNodes(apc.Target, smap.CountTargets())
	}

	// (1) no recompute, no totals; "long-run" (if spec-ed) via app.go
	if cb == nil {
		lfooter := 72
		if allPerfTabs {
			lfooter = 0
		}
		setLongRunParams(c, lfooter)

		ctx := teb.PerfTabCtx{Smap: smap, Sid: tid, Metrics: metrics, Regex: regex, Units: units, AvgSize: avgSize, NoColor: cfg.NoColor}
		table, num, err := ctx.MakeTab(tstatusMap)
		if err != nil {
			return err
		}

		if allPerfTabs {
			perfCptn(c, tag)
		}
		if num == 0 && tag == cmdShowCounters {
			if regex == nil {
				actionNote(c, "the cluster is completely idle: all collected counters have zero values\n")
			}
		}

		out := table.Template(hideHeader)
		return teb.Print(tstatusMap, out)
	}

	// (2) `cb` recompute at each cycle
	if params != nil && params.mapEnd == nil {
		return nil // won't be nil starting next long-run iteration
	}
	var (
		refresh = flagIsSet(c, refreshFlag)
		sleep   = _refreshRate(c)
		cntRun  = &longRun{mapBegin: tstatusMap}
	)
	if sleep < time.Second || sleep > time.Minute {
		return fmt.Errorf("invalid %s value, got %v, expecting [1s - 1m]", qflprn(refreshFlag), sleep)
	}

	cntRun.init(c, true /*run once unless*/)
	for countdown := cntRun.count; countdown > 0 || cntRun.isForever(); countdown-- {
		var mapBegin, mapEnd teb.StstMap

		for name := range totals { // reset
			totals[name] = 0
		}

		if params != nil {
			mapBegin, mapEnd = params.mapBegin, params.mapEnd
		} else {
			mapBegin, mapEnd, err = _cluStatusBeginEnd(c, cntRun.mapBegin, sleep)
			if err != nil {
				return err
			}
			cntRun.mapBegin = mapEnd
		}

		idle := cb(c, metrics, mapBegin, mapEnd, sleep) // call back to recompute
		perfCptn(c, tag)

		// tally up recomputed
		totalsHdr := teb.ClusterTotal
		if totals != nil {
			for _, begin := range mapBegin {
				_ = begin.DeploymentType
				for name, v := range begin.Tracker {
					if _, ok := totals[name]; ok {
						totals[name] += v.Value // (each target separately reporting; compare ref 152408)
					}
				}
			}
		}

		ctx := teb.PerfTabCtx{Smap: smap, Sid: tid, Metrics: metrics, Regex: regex, Units: units,
			Totals: totals, TotalsHdr: totalsHdr, AvgSize: avgSize, Idle: idle, NoColor: cfg.NoColor}
		table, _, err := ctx.MakeTab(mapBegin)
		if err != nil {
			return err
		}

		out := table.Template(hideHeader)
		err = teb.Print(mapBegin, out)
		if err != nil || !refresh || allPerfTabs {
			return err
		}
	}
	return nil
}

func showMpathCapHandler(c *cli.Context) error {
	var (
		tid         string
		regex       *regexp.Regexp
		regexStr    = parseStrFlag(c, regexColsFlag)
		hideHeader  = flagIsSet(c, noHeaderFlag)
		showMpaths  = flagIsSet(c, mountpathFlag)
		units, errU = parseUnitsFlag(c, unitsFlag)
	)
	if errU != nil {
		return errU
	}
	node, _, err := arg0Node(c)
	if err != nil {
		return err
	}
	if node != nil {
		tid = node.ID()
	}
	if regexStr != "" {
		regex, err = regexp.Compile(regexStr)
		if err != nil {
			return err
		}
	}

	setLongRunParams(c, 72)

	smap, tstatusMap, _, err := fillNodeStatusMap(c, apc.Target)
	if err != nil {
		return err
	}

	ctx := teb.PerfTabCtx{Smap: smap, Sid: tid, Regex: regex, Units: units, NoColor: cfg.NoColor}
	table := teb.NewMpathCapTab(tstatusMap, &ctx, showMpaths)

	out := table.Template(hideHeader)
	return teb.Print(tstatusMap, out)
}
