// Package integration contains AIS integration tests.
/*
 * Copyright (c) 2018-2020, NVIDIA CORPORATION. All rights reserved.
 */
package integration

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/transform"
	"github.com/NVIDIA/aistore/tutils"
	"github.com/NVIDIA/aistore/tutils/readers"
	"github.com/NVIDIA/aistore/tutils/tassert"
	"github.com/NVIDIA/go-tfdata/tfdata/core"
	jsoniter "github.com/json-iterator/go"
)

func startTar2TfTransformer(t *testing.T) (uuid string) {
	transformerTemplate := filepath.Join("templates", "transformer", tar2tf, "pod.yaml")
	spec, err := ioutil.ReadFile(transformerTemplate)
	tassert.CheckError(t, err)

	pod, err := transform.ParsePodSpec(spec)
	tassert.CheckError(t, err)
	spec, _ = jsoniter.Marshal(pod)

	// Starting transformer
	uuid, err = api.TransformInit(baseParams, spec)
	tassert.CheckFatal(t, err)
	return uuid
}

func TestKubeTar2TFS3(t *testing.T) {
	tutils.CheckSkip(t, tutils.SkipTestArgs{Kubernetes: true})

	const (
		tarObjName   = "small-mnist-3.tar"
		tfRecordFile = "small-mnist-3.record"
	)

	var (
		tarPath      = filepath.Join("data", tarObjName)
		tfRecordPath = filepath.Join("data", tfRecordFile)
		proxyURL     = tutils.RandomProxyURL()
		bck          = cmn.Bck{
			Name:     TestBucketName,
			Provider: cmn.ProviderAIS,
		}
		baseParams = tutils.BaseAPIParams(proxyURL)
	)

	tutils.CreateFreshBucket(t, proxyURL, bck)
	defer tutils.DestroyBucket(t, proxyURL, bck)

	// PUT TAR to the cluster
	f, err := readers.NewFileReaderFromFile(tarPath, cmn.ChecksumXXHash)
	tassert.CheckFatal(t, err)
	putArgs := api.PutObjectArgs{
		BaseParams: baseParams,
		Bck:        bck,
		Object:     tarObjName,
		Cksum:      f.Cksum(),
		Reader:     f,
	}
	tassert.CheckFatal(t, api.PutObject(putArgs))
	defer api.DeleteObject(baseParams, bck, tarObjName)

	uuid := startTar2TfTransformer(t)
	defer func() {
		tassert.CheckFatal(t, api.TransformStop(baseParams, uuid))
	}()

	// GET TFRecord from TAR
	outFileBuffer := bytes.NewBuffer(nil)

	_, err = api.GetObjectS3(baseParams, bck, tarObjName+"!"+uuid, api.GetObjectInput{Writer: outFileBuffer})
	tassert.CheckFatal(t, err)
	tassert.CheckFatal(t, err)

	// Comparing actual vs expected
	tfRecord, err := os.Open(tfRecordPath)
	tassert.CheckFatal(t, err)
	defer tfRecord.Close()

	expectedRecords, err := core.NewTFRecordReader(tfRecord).ReadAllExamples()
	tassert.CheckFatal(t, err)
	actualRecords, err := core.NewTFRecordReader(outFileBuffer).ReadAllExamples()
	tassert.CheckFatal(t, err)

	equal, err := tfRecordsEqual(expectedRecords, actualRecords)
	tassert.CheckFatal(t, err)
	tassert.Errorf(t, equal == true, "actual and expected records different")
}

func TestKubeTar2TFRanges(t *testing.T) {
	tutils.CheckSkip(t, tutils.SkipTestArgs{Kubernetes: true})

	type testCase struct {
		start, end int64
	}

	var (
		tarObjName = "small-mnist-3.tar"
		tarPath    = filepath.Join("data", tarObjName)
		proxyURL   = tutils.RandomProxyURL()
		bck        = cmn.Bck{
			Name:     TestBucketName,
			Provider: cmn.ProviderAIS,
		}
		baseParams     = tutils.BaseAPIParams(proxyURL)
		rangeBytesBuff = bytes.NewBuffer(nil)

		tcs = []testCase{
			{start: 0, end: 1},
			{start: 0, end: 50},
			{start: 1, end: 20},
			{start: 15, end: 100},
			{start: 120, end: 240},
			{start: 123, end: 1234},
		}
	)

	tutils.CreateFreshBucket(t, proxyURL, bck)
	defer tutils.DestroyBucket(t, proxyURL, bck)

	// PUT TAR to the cluster
	f, err := readers.NewFileReaderFromFile(tarPath, cmn.ChecksumXXHash)
	tassert.CheckFatal(t, err)
	putArgs := api.PutObjectArgs{
		BaseParams: baseParams,
		Bck:        bck,
		Object:     tarObjName,
		Cksum:      f.Cksum(),
		Reader:     f,
	}
	tassert.CheckFatal(t, api.PutObject(putArgs))

	uuid := startTar2TfTransformer(t)
	defer func() {
		tassert.CheckFatal(t, api.TransformStop(baseParams, uuid))
	}()

	// GET TFRecord from TAR
	wholeTFRecord := bytes.NewBuffer(nil)
	_, err = api.GetObjectS3(baseParams, bck, tarObjName+"!"+uuid, api.GetObjectInput{Writer: wholeTFRecord})
	tassert.CheckFatal(t, err)

	for _, tc := range tcs {
		rangeBytesBuff.Reset()

		// Request only a subset of bytes
		header := http.Header{}
		header.Set(cmn.HeaderRange, fmt.Sprintf("bytes=%d-%d", tc.start, tc.end))
		_, err = api.GetObjectS3(baseParams, bck, tarObjName+"!"+uuid, api.GetObjectInput{Writer: rangeBytesBuff, Header: header})
		tassert.CheckFatal(t, err)

		tassert.Errorf(t, bytes.Equal(rangeBytesBuff.Bytes(), wholeTFRecord.Bytes()[tc.start:tc.end+1]), "[start: %d, end: %d] bytes different", tc.start, tc.end)
	}
}
