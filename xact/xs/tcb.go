// Package xs is a collection of eXtended actions (xactions), including multi-object
// operations, list-objects, (cluster) rebalance and (target) resilver, ETL, and more.
/*
 * Copyright (c) 2018-2025, NVIDIA CORPORATION. All rights reserved.
 */
package xs

import (
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/mono"
	"github.com/NVIDIA/aistore/cmn/nlog"
	"github.com/NVIDIA/aistore/core"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/ext/etl"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/fs/mpather"
	"github.com/NVIDIA/aistore/memsys"
	"github.com/NVIDIA/aistore/stats"
	"github.com/NVIDIA/aistore/transport"
	"github.com/NVIDIA/aistore/transport/bundle"
	"github.com/NVIDIA/aistore/xact"
	"github.com/NVIDIA/aistore/xact/xreg"
)

type (
	tcbFactory struct {
		xreg.RenewBase
		xctn *XactTCB
		kind string
	}
	tcbworker struct {
		r *XactTCB
	}
	XactTCB struct {
		// sub-function
		transform etl.Session
		copier
		prune prune
		sntl  sentinel
		xact.BckJog
		// copying parallelism
		numwp struct {
			workers []tcbworker
			workCh  chan core.LIF
			stopCh  *cos.StopCh
			wg      sync.WaitGroup
		}
		// args
		args *xreg.TCBArgs
		dm   *bundle.DM
		nam  string
		owt  cmn.OWT
	}
)

// interface guard
var (
	_ core.Xact      = (*XactTCB)(nil)
	_ xreg.Renewable = (*tcbFactory)(nil)
)

////////////////
// tcbFactory //
////////////////

func (p *tcbFactory) New(args xreg.Args, bck *meta.Bck) xreg.Renewable {
	return &tcbFactory{RenewBase: xreg.RenewBase{Args: args, Bck: bck}, kind: p.kind}
}

func (p *tcbFactory) Start() error {
	var (
		smap      = core.T.Sowner().Get()
		nat       = smap.CountActiveTs()
		config    = cmn.GCO.Get()
		slab, err = core.T.PageMM().GetSlab(memsys.MaxPageSlabSize) // estimate
		args      = p.Args.Custom.(*xreg.TCBArgs)
		msg       = args.Msg
	)
	debug.AssertNoErr(err)

	r := &XactTCB{args: args}
	r.init(p, slab, config, smap, nat)

	r.owt = cmn.OwtCopy
	if p.kind == apc.ActETLBck {
		// TODO upon abort: call r.transform.Finish() to cleanup communicator's state
		r.owt = cmn.OwtTransform
		r.copier.getROC, r.transform, err = etl.GetOfflineTransform(args.Msg.Transform.Name, r)
		if err != nil {
			return err
		}
	}

	if err := core.InMaintOrDecomm(smap, core.T.Snode(), r); err != nil {
		return err
	}

	// single-node cluster
	if nat <= 1 {
		return nil // ---->
	}

	if r.args.DisableDM {
		return nil
	}

	// data mover and sentinel
	var sizePDU int32
	if p.kind == apc.ActETLBck {
		sizePDU = memsys.DefaultBufSize // `transport` to generate PDU-based traffic
	}
	if err := r.newDM(sizePDU); err != nil {
		return err
	}

	// only need workers and sentinels when there's intra-cluster traffic
	// intra-cluster copying/transforming parallelism
	// tune-up num-workers, if specified
	numWorkers, err := throttleNwp(r.Name(), msg.NumWorkers)
	if err != nil {
		return err
	}
	// unlike tco and other lrit-based xactions,
	// tcb - via xact.BckJog - employs conventional system joggers;
	// `nwpNone` not supported
	if numWorkers > 0 {
		r._iniNwp(numWorkers)
	}

	// sentinels, to coordinate aborting and finishing
	r.sntl.init(r, smap, nat)

	return nil
}

func (r *XactTCB) _iniNwp(numWorkers int) {
	r.numwp.workers = make([]tcbworker, 0, numWorkers)
	for range numWorkers {
		r.numwp.workers = append(r.numwp.workers, tcbworker{r})
	}
	r.numwp.workCh = make(chan core.LIF, numWorkers*nwpBurst)
	r.numwp.stopCh = cos.NewStopCh()
	nlog.Infoln(r.Name(), "workers:", numWorkers)
}

func (p *tcbFactory) Kind() string   { return p.kind }
func (p *tcbFactory) Get() core.Xact { return p.xctn }

func (p *tcbFactory) WhenPrevIsRunning(prevEntry xreg.Renewable) (wpr xreg.WPR, err error) {
	prev := prevEntry.(*tcbFactory)
	if p.UUID() != prev.UUID() {
		return wpr, cmn.NewErrXactUsePrev(prevEntry.Get().String())
	}
	bckEq := prev.xctn.args.BckFrom.Equal(p.xctn.args.BckFrom, true /*same BID*/, true /*same backend*/)
	debug.Assert(bckEq)
	return xreg.WprUse, nil
}

/////////////
// XactTCB copies one bucket _into_ another with or without transformation
/////////////

func (r *XactTCB) newDM(sizePDU int32) error {
	const trname = "tcb-"
	config := r.BckJog.Config
	dmExtra := bundle.Extra{
		RecvAck:     nil, // no ACKs
		Config:      config,
		Compression: config.TCB.Compression,
		Multiplier:  config.TCB.SbundleMult,
		SizePDU:     sizePDU,
	}
	// in re cmn.OwtPut: see comment inside _recv()
	dm := bundle.NewDM(trname+r.ID(), r.recv, r.owt, dmExtra)
	if err := dm.RegRecv(); err != nil {
		return err
	}
	dm.SetXact(r)
	r.dm = dm

	return nil
}

// limited pre-run abort
func (r *XactTCB) TxnAbort(err error) {
	err = cmn.NewErrAborted(r.Name(), "tcb: txn-abort", err)
	if r.dm != nil {
		r.dm.Close(err)
		r.dm.UnregRecv()
	}
	r.AddErr(err)
	r.Base.Finish()
}

func (r *XactTCB) init(p *tcbFactory, slab *memsys.Slab, config *cmn.Config, smap *meta.Smap, nat int) {
	var (
		args   = r.args
		msg    = r.args.Msg
		mpopts = &mpather.JgroupOpts{
			CTs:      []string{fs.ObjectType},
			VisitObj: r.do,
			Prefix:   msg.Prefix,
			Slab:     slab,
			DoLoad:   mpather.Load,
			Throttle: false, // superseded by destination rate-limiting (v3.28)
		}
	)
	mpopts.Bck.Copy(args.BckFrom.Bucket())

	// ctlmsg
	var (
		sb        strings.Builder
		fromCname = args.BckFrom.Cname(msg.Prefix)
		toCname   = args.BckTo.Cname(msg.Prepend)
	)
	sb.Grow(80)
	msg.Str(&sb, fromCname, toCname)

	// init base
	r.BckJog.Init(p.UUID(), p.kind, sb.String() /*ctlmsg*/, args.BckTo, mpopts, config)

	// xname
	r._name(fromCname, toCname, r.BckJog.NumJoggers())

	r.rate.init(args.BckFrom, args.BckTo, nat)

	if msg.Sync {
		debug.Assert(msg.Prepend == "", msg.Prepend) // validated (cli, P)
		{
			r.prune.r = r
			r.prune.smap = smap
			r.prune.bckFrom = args.BckFrom
			r.prune.bckTo = args.BckTo
			r.prune.prefix = msg.Prefix
		}
		r.prune.init(config)
	}

	r.copier.r = r

	debug.Assert(args.BckFrom.Props != nil)
	// (rgetstats)
	if bck := args.BckFrom; bck.IsRemote() {
		r.bp = core.T.Backend(bck)
		r.vlabs = map[string]string{
			stats.VlabBucket: bck.Cname(""),
			stats.VlabXkind:  r.Kind(),
		}
	}

	p.xctn = r
}

func (r *XactTCB) Run(wg *sync.WaitGroup) {
	// make sure `nat` hasn't changed between Start and now (highly unlikely)
	if r.dm != nil {
		if err := r.sntl.checkSmap(nil); err != nil {
			r.Abort(err)
			wg.Done()
			return
		}

		r.dm.SetXact(r)
		r.dm.Open()
	}
	wg.Done()

	for _, worker := range r.numwp.workers {
		buf, slab := core.T.PageMM().Alloc()
		r.numwp.wg.Add(1)
		go worker.run(buf, slab)
	}

	r.BckJog.Run()
	if r.args.Msg.Sync {
		r.prune.run() // the 2nd jgroup
	}
	nlog.Infoln(core.T.String(), "run:", r.Name())

	err := r.BckJog.Wait()
	if err == nil {
		err = r.AbortErr()
	}

	if r.dm != nil {
		r.sntl.bcast(r.dm, err) // broadcast: done | abort
		if !r.IsAborted() {
			r.sntl.initLast(mono.NanoTime())
			qui := r.Base.Quiesce(r.qival(), r.qcb) // when done: wait for others
			if qui == core.QuiAborted {
				err := r.AbortErr()
				debug.Assert(err != nil)
				r.sntl.bcast(r.dm, err) // broadcast: abort
			}
		}
		// close
		r.dm.Close(err)
		r.dm.UnregRecv()
	}
	if r.args.Msg.Sync {
		r.prune.wait()
	}

	if r.numwp.workers != nil {
		r.numwp.stopCh.Close()
		close(r.numwp.workCh)
		r.numwp.wg.Wait()
	}

	r.sntl.cleanup()
	r.Finish()
}

func (r *XactTCB) qival() time.Duration {
	return min(max(r.Config.Timeout.MaxHostBusy.D(), 10*time.Second), time.Minute)
}

func (r *XactTCB) qcb(tot time.Duration) core.QuiRes {
	nwait := r.sntl.pend.n.Load()
	if nwait > 0 {
		progressTimeout := max(r.Config.Timeout.SendFile.D(), time.Minute)
		return r.sntl.qcb(r.dm, tot, r.qival(), progressTimeout, r.ErrCnt())
	}
	return core.QuiDone
}

func (r *XactTCB) do(lom *core.LOM, buf []byte) error {
	if r.numwp.workers == nil {
		args := r.args // TCBArgs
		a := r.copier.prepare(lom, args.BckTo, args.Msg, r.Config, buf, r.owt)

		err := r.copier.do(a, lom, r.dm)
		if err == nil && args.Msg.Sync {
			r.prune.filter.Insert(cos.UnsafeB(lom.Uname()))
		}
		return err
	}

	r.numwp.workCh <- lom.LIF()
	return nil
}

// NOTE: strict(est) error handling: abort on any of the errors below
func (r *XactTCB) recv(hdr *transport.ObjHdr, objReader io.Reader, err error) error {
	if err != nil && !cos.IsEOF(err) {
		nlog.Errorln(err)
		return err
	}

	// control; // TODO -- FIXME: must become a shared code w/ tco
	if hdr.Opcode != 0 {
		switch hdr.Opcode {
		case opDone:
			r.sntl.rxDone(hdr)
		case opAbort:
			r.sntl.rxAbort(hdr)
		case opRequest:
			o := transport.AllocSend()
			o.Hdr.Opcode = opResponse
			b := make([]byte, cos.SizeofI64)
			binary.BigEndian.PutUint64(b, uint64(r.BckJog.NumVisits())) // report progress
			o.Hdr.Opaque = b
			r.dm.Bcast(o, nil) // TODO: consider limiting this broadcast to only quiescing (waiting) targets
		case opResponse:
			r.sntl.rxProgress(hdr) // handle response: progress by others
		default:
			err := fmt.Errorf("invalid header opcode %d", hdr.Opcode)
			debug.AssertNoErr(err)
			r.Abort(err)
			return err
		}
		return nil
	}

	// data
	lom := core.AllocLOM(hdr.ObjName)
	err = r._recv(hdr, objReader, lom)
	core.FreeLOM(lom)
	transport.DrainAndFreeReader(objReader)
	return err
}

func (r *XactTCB) _recv(hdr *transport.ObjHdr, objReader io.Reader, lom *core.LOM) error {
	if err := lom.InitBck(&hdr.Bck); err != nil {
		r.AddErr(err, 0)
		return err
	}
	lom.CopyAttrs(&hdr.ObjAttrs, true /*skip cksum*/)
	params := core.AllocPutParams()
	{
		params.WorkTag = fs.WorkfilePut
		params.Reader = io.NopCloser(objReader)
		params.Cksum = hdr.ObjAttrs.Cksum
		params.Xact = r
		params.Size = hdr.ObjAttrs.Size
		params.OWT = r.owt
	}
	if lom.AtimeUnix() == 0 {
		// TODO: sender must be setting it, remove this `if` when fixed
		lom.SetAtimeUnix(time.Now().UnixNano())
	}
	params.Atime = lom.Atime()

	erp := core.T.PutObject(lom, params)
	core.FreePutParams(params)
	if erp != nil {
		r.AddErr(erp, 0)
		return erp // NOTE: non-nil signals transport to terminate
	}

	return nil
}

func (r *XactTCB) Abort(err error) bool {
	if !r.Base.Abort(err) { // already aborted?
		return false
	}
	if r.numwp.stopCh != nil {
		debug.Assert(r.numwp.workers != nil)
		r.numwp.stopCh.Close()
	}
	return true
}

func (r *XactTCB) Args() *xreg.TCBArgs { return r.args }

func (r *XactTCB) _name(fromCname, toCname string, numJoggers int) {
	var sb strings.Builder
	sb.Grow(80)
	sb.WriteString(r.Base.Cname())
	sb.WriteString("-p") // as in: "parallelism"
	sb.WriteString(strconv.Itoa(numJoggers))
	sb.WriteByte('-')
	sb.WriteString(fromCname)
	sb.WriteString("=>")
	sb.WriteString(toCname)

	r.nam = sb.String()
}

func (r *XactTCB) String() string { return r.nam }
func (r *XactTCB) Name() string   { return r.nam }

func (r *XactTCB) FromTo() (*meta.Bck, *meta.Bck) {
	return r.args.BckFrom, r.args.BckTo
}

func (r *XactTCB) Snap() (snap *core.Snap) {
	snap = &core.Snap{}
	r.ToSnap(snap)

	snap.IdleX = r.IsIdle()
	f, t := r.FromTo()
	snap.SrcBck, snap.DstBck = f.Clone(), t.Clone()
	return
}

///////////////
// tcbworker //
///////////////

func (worker *tcbworker) run(buf []byte, slab *memsys.Slab) {
	p := &worker.r.numwp
outer:
	for {
		select {
		case lif, ok := <-p.workCh:
			if !ok {
				break outer
			}
			if aborted := worker.do(lif, buf); aborted {
				break outer
			}
		case <-p.stopCh.Listen():
			break outer
		}
	}
	p.wg.Done()
	slab.Free(buf)
}

func (worker *tcbworker) do(lif core.LIF, buf []byte) bool {
	var (
		r    = worker.r
		args = r.args // TCBArgs
	)
	lom, err := lif.LOM()
	if err != nil {
		nlog.Warningln(r.Name(), lif.Name(), err)
		r.Abort(err)
		return true
	}

	a := r.copier.prepare(lom, args.BckTo, args.Msg, r.Config, buf, r.owt)
	if err := r.copier.do(a, lom, r.dm); err != nil {
		return r.IsAborted()
	}
	if args.Msg.Sync {
		r.prune.filter.Insert(cos.UnsafeB(lom.Uname()))
	}
	return false
}
