// Package etl provides utilities to initialize and use transformation pods.
/*
 * Copyright (c) 2021-2025, NVIDIA CORPORATION. All rights reserved.
 */
package etl

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/NVIDIA/aistore/cmn/jsp"
	jsoniter "github.com/json-iterator/go"
)

type (
	Instance struct {
		InitMsg `json:"init_msg,omitempty"`
		Stage   `json:"stage"` // enum ETL lifecycle stage
		XactID  string         `json:"xid,omitempty"`
	}
	ETLs map[string]Instance

	// ETL metadata
	MD struct {
		Ext     any
		ETLs    ETLs
		Version int64
	}

	jsonETL struct {
		Type   string              `json:"type,string"`
		XactID string              `json:"xid"`
		Msg    jsoniter.RawMessage `json:"msg"`
	}
	jsonMD struct {
		Ext     any                `json:"ext,omitempty"` // within meta-version extensions
		ETLs    map[string]jsonETL `json:"etls"`
		Version int64              `json:"version"`
	}
)

var etlMDJspOpts = jsp.CCSign(cmn.MetaverEtlMD)

// interface guard
var (
	_ json.Marshaler   = (*MD)(nil)
	_ json.Unmarshaler = (*MD)(nil)

	_ jsp.Opts = (*MD)(nil)
)

////////
// MD //
////////

func (e *MD) Init(l int) { e.ETLs = make(ETLs, l) }
func (e *MD) Add(msg InitMsg, xid string, stage Stage) {
	var etlName string
	if msg != nil {
		etlName = msg.Name()
	} else {
		// if msg is not provided, find the match by xid
		for name, inst := range e.ETLs {
			if inst.XactID == xid {
				etlName = name
				msg = inst.InitMsg
				break
			}
		}
	}
	debug.Assertf(etlName != "", "No matching ETL found in MD, msg=%v, xid=%s, stage=%s, e.ETLs=%v\n", msg, xid, stage.String(), e.ETLs)
	pre, ok := e.ETLs[etlName]
	if !ok {
		e.ETLs[etlName] = Instance{InitMsg: msg, XactID: xid, Stage: stage}
		return
	}

	// preserve previous values if new ones are not provided
	debug.Assertf(msg == nil || reflect.DeepEqual(msg, pre.InitMsg), "msg should be empty or unchanged, %s vs %s", msg, pre.InitMsg)
	if msg == nil {
		msg = pre.InitMsg
	}

	debug.Assertf(xid == "" || xid == pre.XactID, "xid should be empty or unchanged, %s vs %s", xid, pre.XactID)
	if xid == "" {
		xid = pre.XactID
	}

	if stage == Unknown {
		stage = pre.Stage
	}

	e.ETLs[etlName] = Instance{InitMsg: msg, XactID: xid, Stage: stage}
}
func (*MD) JspOpts() jsp.Options { return etlMDJspOpts }

func (e *MD) Get(id string) (InitMsg, bool) {
	if e == nil {
		return nil, false
	}
	inst, present := e.ETLs[id]
	return inst.InitMsg, present
}

func (e *MD) Del(id string) (deleted bool) {
	if _, present := e.ETLs[id]; !present {
		return
	}
	delete(e.ETLs, id)
	return true
}

func (e *MD) String() string {
	if e == nil {
		return "EtlMD <nil>"
	}
	return fmt.Sprintf("EtlMD v%d(%d)", e.Version, len(e.ETLs))
}

func (e *MD) MarshalJSON() ([]byte, error) {
	jsonMD := jsonMD{
		Version: e.Version,
		ETLs:    make(map[string]jsonETL, len(e.ETLs)),
		Ext:     e.Ext,
	}
	for k, v := range e.ETLs {
		jsonMD.ETLs[k] = jsonETL{v.MsgType(), v.XactID, cos.MustMarshal(v.InitMsg)}
	}
	return jsoniter.Marshal(jsonMD)
}

func (e *MD) UnmarshalJSON(data []byte) (err error) {
	jsonMD := &jsonMD{}
	if err = jsoniter.Unmarshal(data, jsonMD); err != nil {
		return
	}
	e.Version, e.Ext = jsonMD.Version, jsonMD.Ext
	e.ETLs = make(ETLs, len(jsonMD.ETLs))
	for k, v := range jsonMD.ETLs {
		switch v.Type {
		case Code:
			e.ETLs[k] = Instance{InitMsg: &InitCodeMsg{}, XactID: v.XactID}
		case Spec:
			e.ETLs[k] = Instance{InitMsg: &InitSpecMsg{}, XactID: v.XactID}
		default:
			err = fmt.Errorf("invalid InitMsg type %q", v.Type)
			debug.AssertNoErr(err)
			return
		}
		if err = jsoniter.Unmarshal(v.Msg, e.ETLs[k].InitMsg); err != nil {
			break
		}
	}
	return
}
