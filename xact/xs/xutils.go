// Package xs is a collection of eXtended actions (xactions), including multi-object
// operations, list-objects, (cluster) rebalance and (target) resilver, ETL, and more.
/*
 * Copyright (c) 2025, NVIDIA CORPORATION. All rights reserved.
 */
package xs

import (
	"time"

	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/mono"
	"github.com/NVIDIA/aistore/core"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/stats"
)

func rgetstats(bp core.Backend, vlabs map[string]string, size, started int64) {
	tstats := core.T.StatsUpdater()
	delta := mono.SinceNano(started)
	tstats.IncWith(bp.MetricName(stats.GetCount), vlabs)
	tstats.AddWith(
		cos.NamedVal64{Name: bp.MetricName(stats.GetLatencyTotal), Value: delta, VarLabs: vlabs},
		cos.NamedVal64{Name: bp.MetricName(stats.GetSize), Value: size, VarLabs: vlabs},
	)
}

func onmanyreq(arl *cos.AdaptRateLim, vlabs map[string]string) {
	tstats := core.T.StatsUpdater()
	sleep := arl.OnErr()
	tstats.AddWith(
		cos.NamedVal64{Name: stats.RateRetryLatencyTotal, Value: int64(sleep), VarLabs: vlabs},
	)
}

////////////
// tcrate //
////////////

// TODO: support RateLimitConf.Verbs, here and elsewhere

type tcrate struct {
	src struct {
		rl    *cos.AdaptRateLim
		sleep time.Duration
	}
	dst struct {
		rl    *cos.AdaptRateLim
		sleep time.Duration
	}
}

func newrate(src, dst *meta.Bck, nat int) *tcrate {
	var rate tcrate
	rate.src.rl, rate.src.sleep = src.NewBackendRateLim(nat)
	if dst.Props != nil { // destination may not exist
		rate.dst.rl, rate.dst.sleep = dst.NewBackendRateLim(nat)
	}
	if rate.src.rl == nil && rate.dst.rl == nil {
		return nil
	}
	return &rate
}

// NOTE: destination rate-limiter takes precedence if both defined
func (rate *tcrate) onerr(vlabs map[string]string) {
	arl := rate.dst.rl
	if arl == nil {
		arl = rate.src.rl
	}
	debug.Assert(arl != nil)
	onmanyreq(arl, vlabs)
}
