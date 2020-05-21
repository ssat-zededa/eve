// Copyright (c) 2019 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	zcommon "github.com/lf-edge/eve/api/go/evecommon"
	"github.com/stretchr/testify/assert"
)

var aa AssignableAdapters = AssignableAdapters{
	Initialized: true,
	IoBundleList: []IoBundle{
		{
			Type:            IoNetEth,
			AssignmentGroup: "eth0-1",
			Phylabel:        "eth0",
			Ifname:          "eth0",
		},
		{
			Type:            IoNetEth,
			AssignmentGroup: "eth0-1",
			Phylabel:        "eth1",
			Ifname:          "eth1",
		},
		{
			Type:            IoNetEth,
			AssignmentGroup: "eth2",
			Phylabel:        "eth2",
			Ifname:          "eth2",
		},
		{
			Type:            IoNetEth,
			AssignmentGroup: "eTH4-7",
			Phylabel:        "eth4",
			Ifname:          "eth4",
		},
		{
			Type:            IoNetEth,
			AssignmentGroup: "eTH4-7",
			Phylabel:        "eth5",
			Ifname:          "eth5",
		},
		{
			Type:            IoNetEth,
			AssignmentGroup: "eTH4-7",
			Phylabel:        "eth6",
			Ifname:          "eth6",
		},
		{
			Type:            IoNetEth,
			AssignmentGroup: "eTH4-7",
			Phylabel:        "eth7",
			Ifname:          "eth7",
		},
	},
}

func TestLookupIoBundleGroup(t *testing.T) {
	testMatrix := map[string]struct {
		ioType             IoType
		lookupName         string
		expectedBundleName string
	}{
		"IoType: IoNetEth, LookupName: eth0-1": {
			ioType:             IoNetEth,
			lookupName:         "eth0-1",
			expectedBundleName: "eth0-1",
		},
		"IoType: IoNetEth LookupName: eth1": {
			ioType:             IoNetEth,
			lookupName:         "eth1",
			expectedBundleName: "",
		},
		"IoType: IoNetEth LookupName: eth2": {
			ioType:             IoNetEth,
			lookupName:         "eth2",
			expectedBundleName: "eth2",
		},
		"IoType: IoNetEth LookupName: eth4-7": {
			ioType:             IoNetEth,
			lookupName:         "eth4-7",
			expectedBundleName: "eTH4-7",
		},
	}

	for testname, test := range testMatrix {
		t.Logf("Running test case %s", testname)
		list := aa.LookupIoBundleGroup(test.lookupName)
		if len(list) == 0 {
			assert.Equal(t, test.expectedBundleName, "")
		} else {
			assert.Equal(t, test.expectedBundleName,
				list[0].AssignmentGroup)
		}
	}
}

func TestLookupIoBundlePhylabel(t *testing.T) {
	testMatrix := map[string]struct {
		ioType             IoType
		lookupName         string
		expectedBundleName string
	}{
		"ioType: IoNetEth, lookupName: eth1": {
			ioType:             IoNetEth,
			lookupName:         "eth1",
			expectedBundleName: "eth1",
		},
		"ioType: IoNetEth, lookupName: eth3": {
			ioType:             IoNetEth,
			lookupName:         "eth3",
			expectedBundleName: "",
		}, // No such member
		"ioType: IoNetEth, lookupName: eth7": {
			ioType:             IoNetEth,
			lookupName:         "eth7",
			expectedBundleName: "eth7",
		},
		// Test Ignore case
		"ioType: IoNetEth, lookupName: ETH7": {
			ioType:             IoNetEth,
			lookupName:         "ETH7",
			expectedBundleName: "eth7",
		},
	}

	// Basic test
	for testname, test := range testMatrix {
		t.Logf("Running test case %s", testname)
		ioBundle := aa.LookupIoBundlePhylabel(test.lookupName)
		if ioBundle == nil {
			assert.Equal(t, test.expectedBundleName, "")
		} else {
			assert.Equal(t, test.expectedBundleName, ioBundle.Phylabel)
		}
	}
}

func TestIoBundleFromPhyAdapter(t *testing.T) {
	phyAdapter := PhysicalIOAdapter{
		Ptype:        zcommon.PhyIoType_PhyIoNetEth,
		Phylabel:     "ethernet0",
		Logicallabel: "shopfloor",
		Assigngrp:    "eth-grp-1",
		Phyaddr: PhysicalAddress{
			Ifname:  "eth0",
			PciLong: "0000:04:00.0",
			Irq:     "5",
			Ioports: "3f8-3ff",
			Serial:  "/dev/ttyS0",
		},
		Usage: zcommon.PhyIoMemberUsage_PhyIoUsageMgmtAndApps,
		UsagePolicy: PhyIOUsagePolicy{
			FreeUplink: true,
		},
	}
	ibPtr := IoBundleFromPhyAdapter(phyAdapter)
	assert.NotEqual(t, ibPtr, nil)
	assert.Equal(t, IoType(phyAdapter.Ptype), ibPtr.Type)
	assert.Equal(t, phyAdapter.Phylabel, ibPtr.Phylabel)
	assert.Equal(t, phyAdapter.Logicallabel, ibPtr.Logicallabel)
	assert.Equal(t, phyAdapter.Assigngrp, ibPtr.AssignmentGroup)
	assert.Equal(t, phyAdapter.Phyaddr.Ifname, ibPtr.Ifname)
	assert.Equal(t, phyAdapter.Phyaddr.PciLong, ibPtr.PciLong)
	assert.Equal(t, phyAdapter.Phyaddr.Irq, ibPtr.Irq)
	assert.Equal(t, phyAdapter.Phyaddr.Ioports, ibPtr.Ioports)
	assert.Equal(t, phyAdapter.Phyaddr.Serial, ibPtr.Serial)
	assert.Equal(t, phyAdapter.Usage, ibPtr.Usage)
	assert.Equal(t, phyAdapter.UsagePolicy.FreeUplink, ibPtr.FreeUplink)
}
