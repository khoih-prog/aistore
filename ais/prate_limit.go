// Package ais provides AIStore's proxy and target nodes.
/*
 * Copyright (c) 2025, NVIDIA CORPORATION. All rights reserved.
 */
package ais

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/hk"
)

// TODO: support RateLimitConf.Frontend.Verbs; optimize uname-ing

const prateName = "prate-limit"

type ratelim struct {
	sync.Map
	now int64
}

func (rl *ratelim) init() {
	hk.Reg(prateName+hk.NameSuffix, rl.housekeep, hk.PruneFrontendRL)
}

func (rl *ratelim) apply(bck *meta.Bck, verb string, smap *smapX) error {
	if !bck.Props.RateLimit.Frontend.Enabled {
		return nil
	}
	var (
		brl   *cos.BurstRateLim // bursty
		uhash = bck.HashUname(verb)
		v, ok = rl.Load(uhash)
	)
	if ok {
		brl = v.(*cos.BurstRateLim)
	} else {
		// ignore sleep time - only relevant for clients
		brl, _ = bck.NewFrontendRateLim(smap.CountActivePs())
		rl.Store(uhash, brl)
	}
	if !brl.TryAcquire() {
		return errors.New(http.StatusText(http.StatusTooManyRequests))
	}
	return nil
}

func (rl *ratelim) housekeep(now int64) time.Duration {
	rl.now = now
	rl.Range(rl.cleanup)
	return hk.PruneFrontendRL
}

func (rl *ratelim) cleanup(k, v any) bool {
	brl := v.(*cos.BurstRateLim)
	if time.Duration(rl.now-brl.LastUsed()) >= hk.PruneFrontendRL>>1 {
		rl.Delete(k)
	}
	return true // keep going
}
