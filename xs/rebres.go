// Package xs contains eXtended actions (xactions) except storage services
// (mirror, ec) and extensions (downloader, lru).
/*
 * Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.
 */
package xs

import (
	"sync"

	"github.com/NVIDIA/aistore/3rdparty/glog"
	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/stats"
	"github.com/NVIDIA/aistore/xaction"
	"github.com/NVIDIA/aistore/xreg"
)

// rebalance & resilver xactions

type (
	rebFactory struct {
		xreg.RenewBase
		xact         *Rebalance
		statsTracker stats.Tracker
	}
	Rebalance struct {
		xaction.XactBase
		statsTracker stats.Tracker // extended stats
	}
	rslvrFactory struct {
		xreg.RenewBase
		xact *Resilver
	}
	Resilver struct {
		xaction.XactBase
	}
)

// interface guard
var (
	_ cluster.Xact   = (*Rebalance)(nil)
	_ xreg.Renewable = (*rebFactory)(nil)
	_ cluster.Xact   = (*Resilver)(nil)
	_ xreg.Renewable = (*rslvrFactory)(nil)
)

///////////////
// Rebalance //
///////////////

func (*rebFactory) New(args xreg.Args, _ *cluster.Bck) xreg.Renewable {
	return &rebFactory{RenewBase: xreg.RenewBase{Args: args}, statsTracker: args.Custom.(stats.Tracker)}
}

func (p *rebFactory) Start() error {
	p.xact = NewRebalance(p.Args.UUID, p.Kind(), p.statsTracker)
	return nil
}

func (*rebFactory) Kind() string        { return cmn.ActRebalance }
func (p *rebFactory) Get() cluster.Xact { return p.xact }

func (p *rebFactory) WhenPrevIsRunning(prevEntry xreg.Renewable) (wpr xreg.WPR, err error) {
	xreb := prevEntry.(*rebFactory)
	wpr = xreg.WprAbort
	if xreb.Args.UUID > p.Args.UUID {
		glog.Errorf("(reb: %s) %s is greater than %s", xreb.xact, xreb.Args.UUID, p.Args.UUID)
		wpr = xreg.WprUse
	} else if xreb.Args.UUID == p.Args.UUID {
		if verbose {
			glog.Infof("%s already running, nothing to do", xreb.xact)
		}
		wpr = xreg.WprUse
	}
	return
}

func NewRebalance(id, kind string, statTracker stats.Tracker) (xact *Rebalance) {
	xact = &Rebalance{statsTracker: statTracker}
	xact.InitBase(id, kind, nil)
	return
}

func (*Rebalance) Run(*sync.WaitGroup) { debug.Assert(false) }

func (xact *Rebalance) Snap() cluster.XactionSnap {
	var (
		baseSnap    = xact.XactBase.Snap().(*xaction.Snap)
		rebSnap     = stats.RebalanceTargetSnap{Snap: *baseSnap}
		statsRunner = xact.statsTracker
	)
	rebSnap.Stats.OutObjs = statsRunner.Get(stats.OutObjCount)
	rebSnap.Stats.OutBytes = statsRunner.Get(stats.OutObjSize)
	rebSnap.Stats.InObjs = statsRunner.Get(stats.InObjCount)
	rebSnap.Stats.InBytes = statsRunner.Get(stats.InObjSize)
	if marked := xreg.GetRebMarked(); marked.Xact != nil {
		var err error
		rebSnap.RebID, err = xaction.S2RebID(marked.Xact.ID())
		debug.AssertNoErr(err)
	} else {
		rebSnap.RebID = 0
	}
	rebSnap.Stats.Objs = rebSnap.Stats.OutObjs + rebSnap.Stats.InObjs
	rebSnap.Stats.Bytes = rebSnap.Stats.OutBytes + rebSnap.Stats.InBytes
	return &rebSnap
}

//////////////
// Resilver //
//////////////

func (*rslvrFactory) New(args xreg.Args, _ *cluster.Bck) xreg.Renewable {
	return &rslvrFactory{RenewBase: xreg.RenewBase{Args: args}}
}

func (p *rslvrFactory) Start() error {
	p.xact = NewResilver(p.UUID(), p.Kind())
	return nil
}

func (*rslvrFactory) Kind() string                                       { return cmn.ActResilver }
func (p *rslvrFactory) Get() cluster.Xact                                { return p.xact }
func (*rslvrFactory) WhenPrevIsRunning(xreg.Renewable) (xreg.WPR, error) { return xreg.WprAbort, nil }

func NewResilver(id, kind string) (xact *Resilver) {
	xact = &Resilver{}
	xact.InitBase(id, kind, nil)
	return
}

func (*Resilver) Run(*sync.WaitGroup) { debug.Assert(false) }

// TODO -- FIXME: check "resilver-marked" and unify with rebalance
func (xact *Resilver) Snap() cluster.XactionSnap {
	baseStats := xact.XactBase.Snap().(*xaction.Snap)
	return baseStats
}
