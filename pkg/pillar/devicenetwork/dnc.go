// Copyright (c) 2017-2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package devicenetwork

import (
	"fmt"
	"net"
	"os"
	"reflect"
	"time"

	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/cipher"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/satori/go.uuid"
)

const (
	MaxDPCRetestCount  = 5
	MaxDPCCheckIfCount = 2
)

type DPCPending struct {
	Inprogress bool
	PendDPC    types.DevicePortConfig
	OldDPC     types.DevicePortConfig
	PendDNS    types.DeviceNetworkStatus
	PendTimer  *time.Timer
	TestCount  uint
}

type DeviceNetworkContext struct {
	DecryptCipherContext     cipher.DecryptCipherContext
	AgentName                string
	UsableAddressCount       int
	DevicePortConfig         *types.DevicePortConfig // Currently in use
	DevicePortConfigList     *types.DevicePortConfigList
	AssignableAdapters       *types.AssignableAdapters
	DevicePortConfigTime     time.Time
	DeviceNetworkStatus      *types.DeviceNetworkStatus
	SubDevicePortConfigA     pubsub.Subscription
	SubDevicePortConfigO     pubsub.Subscription
	SubDevicePortConfigS     pubsub.Subscription
	SubAssignableAdapters    pubsub.Subscription
	PubDevicePortConfig      pubsub.Publication
	PubDummyDevicePortConfig pubsub.Publication // For logging
	PubDevicePortConfigList  pubsub.Publication
	PubCipherBlockStatus     pubsub.Publication
	PubDeviceNetworkStatus   pubsub.Publication
	Changed                  bool
	SubGlobalConfig          pubsub.Subscription

	Pending                DPCPending
	NetworkTestTimer       *time.Timer
	NetworkTestBetterTimer *time.Timer
	NextDPCIndex           int
	CloudConnectivityWorks bool
	Iteration              int // Start with different interfaces each time

	// Timers in seconds
	DPCTestDuration           uint32 // Wait for DHCP address
	NetworkTestInterval       uint32 // Test interval in minutes.
	NetworkTestBetterInterval uint32 // Look for lower/better index
	TestSendTimeout           uint32 // Timeout for HTTP/Send
	Log                       *base.LogObject
}

func UpdateLastResortPortConfig(ctx *DeviceNetworkContext, ports []string) {
	if ports == nil || len(ports) == 0 {
		return
	}
	config := LastResortDevicePortConfig(ctx, ports)
	config.Key = "lastresort"
	ctx.PubDevicePortConfig.Publish("lastresort", config)
}

func RemoveLastResortPortConfig(ctx *DeviceNetworkContext) {
	ctx.PubDevicePortConfig.Unpublish("lastresort")
}

func SetupVerify(ctx *DeviceNetworkContext, index int) {

	log := ctx.Log
	log.Infof("SetupVerify: Setting up verification for DPC at index %d",
		index)
	ctx.NextDPCIndex = index
	ctx.DevicePortConfigList.CurrentIndex = ctx.NextDPCIndex

	pending := &ctx.Pending
	pending.Inprogress = true
	pending.PendDPC = ctx.DevicePortConfigList.PortConfigList[ctx.NextDPCIndex]
	pend2 := MakeDeviceNetworkStatus(log, pending.PendDPC, pending.PendDNS)
	pending.PendDNS = pend2
	pending.TestCount = 0
	log.Infof("SetupVerify: Started testing DPC (index %d): %v",
		ctx.NextDPCIndex,
		ctx.DevicePortConfigList.PortConfigList[ctx.NextDPCIndex])
}

func RestartVerify(ctx *DeviceNetworkContext, caller string) {

	log := ctx.Log
	log.Infof("RestartVerify: Caller %s initialized DPC list verify at %v",
		caller, time.Now())

	pending := &ctx.Pending
	if pending.Inprogress {
		log.Infof("RestartVerify: DPC list verification in progress")
		return
	}
	// Restart at index zero, then skip entries with LastFailed after
	// LastSucceeded and a recent LastFailed (a minute or less).
	nextIndex := getNextTestableDPCIndex(ctx, 0)
	if nextIndex == -1 {
		log.Infof("RestartVerify: nothing testable")
		// Need to publish so that other agents see we have initialized
		// even if we have no IPs
		UpdateResolvConf(log, *ctx.DeviceNetworkStatus)
		UpdatePBR(log, *ctx.DeviceNetworkStatus)
		if ctx.PubDeviceNetworkStatus != nil {
			ctx.DeviceNetworkStatus.Testing = false
			log.Infof("PublishDeviceNetworkStatus: %+v\n",
				ctx.DeviceNetworkStatus)
			ctx.PubDeviceNetworkStatus.Publish("global",
				*ctx.DeviceNetworkStatus)
		}
		return
	}
	SetupVerify(ctx, nextIndex)

	VerifyDevicePortConfig(ctx)
	*ctx.DevicePortConfigList = compressAndPublishDevicePortConfigList(ctx)
}

func compressAndPublishDevicePortConfigList(ctx *DeviceNetworkContext) types.DevicePortConfigList {

	log := ctx.Log
	dpcl := compressDPCL(ctx)
	if ctx.PubDevicePortConfigList != nil {
		log.Infof("publishing DevicePortConfigList compressed: %+v\n", dpcl)
		ctx.PubDevicePortConfigList.Publish("global", dpcl)
	}
	return dpcl
}

// Make DevicePortConfig have at most two zedagent entries;
// 1. the highest priority (whether it has lastSucceeded after lastFailed or not)
// 2. the next priority with lastSucceeded after lastFailed
// and make it have a single item for the other keys
func compressDPCL(ctx *DeviceNetworkContext) types.DevicePortConfigList {

	var newConfig []types.DevicePortConfig

	log := ctx.Log
	dpcl := ctx.DevicePortConfigList

	if ctx.Pending.Inprogress || dpcl.CurrentIndex != 0 ||
		len(dpcl.PortConfigList) == 0 {
		log.Debugf("compressDPCL: DPCL still changing - ctx.Pending.Inprogress: %t, "+
			"dpcl.CurrentIndex: %d, len(PortConfigList): %d",
			ctx.Pending.Inprogress, dpcl.CurrentIndex, len(dpcl.PortConfigList))
		return *dpcl
	}
	firstEntry := dpcl.PortConfigList[0]
	if firstEntry.Key != "zedagent" || !firstEntry.WasDPCWorking() {
		log.Debugf("compressDPCL: firstEntry not stable. key: %s, "+
			"WasWorking: %t, firstEntry: %+v",
			firstEntry.Key, firstEntry.WasDPCWorking(), firstEntry)
		return *dpcl
	}
	log.Debugf("compressDPCL: numEntries: %d, dpcl: %+v",
		len(dpcl.PortConfigList), dpcl)
	for i, dpc := range dpcl.PortConfigList {
		if i == 0 {
			// Always add Current Index ( index 0 )
			newConfig = append(newConfig, dpc)
			log.Debugf("compressDPCL: Adding Current Index: i = %d, dpc: %+v",
				i, dpc)
		} else {
			// Retain the lastresort. Delete everything else.
			if dpc.Key == "lastresort" {
				log.Debugf("compressDPCL: Retaining last resort. i = %d, dpc: %+v",
					i, dpc)
				newConfig = append(newConfig, dpc)
				// last resort also found.. discard all remaining entries
				break
			}
			log.Infof("compressDPCL: Ignoring - i = %d, dpc: %+v", i, dpc)
		}
	}

	return types.DevicePortConfigList{
		CurrentIndex:   0,
		PortConfigList: newConfig,
	}
}

var nilUUID = uuid.UUID{} // Really a const

func VerifyPending(ctx *DeviceNetworkContext, pending *DPCPending,
	aa *types.AssignableAdapters, timeout uint32) types.PendDPCStatus {

	log := ctx.Log
	log.Infof("VerifyPending()\n")
	// Stop pending timer if its running.
	pending.PendTimer.Stop()

	// Check if all the ports in the config are out of pciBack.
	// If yes, apply config.
	// If not, wait for all the ports to come out of PCIBack.
	portInPciBack, ifName, usedByUUID := pending.PendDPC.IsAnyPortInPciBack(log, aa)
	if portInPciBack {
		if usedByUUID != nilUUID {
			errStr := fmt.Sprintf("port %s in PCIBack "+
				"used by %s", ifName, usedByUUID.String())
			log.Errorf("VerifyPending: %s\n", errStr)
			pending.PendDPC.RecordFailure(errStr)
			pending.PendDPC.RecordPortFailure(ifName, errStr)
			return types.DPC_FAIL
		}
		log.Infof("VerifyPending: port %s still in PCIBack. "+
			"wait for it to come out before re-parsing device port config list.\n",
			ifName)
		return types.DPC_PCI_WAIT
	}
	log.Infof("VerifyPending: No required ports held in pciBack. " +
		"parsing device port config list")

	ifname, err := checkInterfacesExists(log, pending.PendDPC)
	if err != nil {
		// Still waiting for a network interface to appear
		if pending.TestCount < MaxDPCCheckIfCount {
			log.Warnf("VerifyPending: interface check: retry due to ifname %s at count %d: %s",
				ifname, pending.TestCount, err)
			pending.TestCount++
			return types.DPC_INTF_WAIT
		}
		log.Warnf("VerifyPending: interface check: failed due to ifname %s: %s",
			ifname, err)
		pending.PendDPC.RecordPortFailure(ifname, err.Error())
		pending.PendDPC.RecordFailure(err.Error())
		// Proceed trying other interfaces
	}
	log.Infof("VerifyPending: No required ports missing. " +
		"parsing device port config list")

	if !pending.PendDPC.MostlyEqual(&pending.OldDPC) {
		log.Infof("VerifyPending: DPC changed. check Wireless %v\n", pending.PendDPC)
		checkAndUpdateWireless(ctx, &pending.OldDPC, &pending.PendDPC)

		log.Infof("VerifyPending: DPC changed. update DhcpClient.\n")
		UpdateDhcpClient(log, pending.PendDPC, pending.OldDPC)
		pending.OldDPC = pending.PendDPC
	}
	pend2 := MakeDeviceNetworkStatus(log, pending.PendDPC, pending.PendDNS)
	pending.PendDNS = pend2

	// We want connectivity to zedcloud via atleast one Management port.
	// Hard-coded at 1 for now; at least one interface needs to work
	const successCount uint = 1
	ctx.Iteration++
	rtf, intfStatusMap, err := VerifyDeviceNetworkStatus(log, pending.PendDNS,
		successCount, ctx.Iteration, timeout)
	// Use TestResults to update the DevicePortConfigList and DeviceNetworkStatus
	// Note that the TestResults will at least have an updated timestamp
	// for one of the ports.
	dpc := &pending.PendDPC
	dpc.UpdatePortStatusFromIntfStatusMap(intfStatusMap)
	ctx.PubDummyDevicePortConfig.Publish(dpc.PubKey(), *dpc)
	pending.PendDNS.UpdatePortStatusFromIntfStatusMap(intfStatusMap)
	if err == nil {
		if checkIfMgmtPortsHaveIPandDNS(log, pending.PendDNS) {
			pending.PendDPC.LastIPAndDNS = time.Now()
		}
		pending.PendDPC.RecordSuccess()
		log.Infof("VerifyPending: DPC passed network test: %+v",
			pending.PendDPC)
		return types.DPC_SUCCESS
	}
	errStr := fmt.Sprintf("Failed network test: %s", err)
	if rtf {
		log.Errorf("VerifyPending: remoteTemporaryFailure %s", errStr)
		// NOTE: do not increase TestCount; we retry until e.g., the
		// certificate or ECONNREFUSED is fixed on the server side.
		return types.DPC_REMOTE_WAIT
	}
	if !checkIfMgmtPortsHaveIPandDNS(log, pending.PendDNS) {
		// Still waiting for IP or DNS
		if pending.TestCount < MaxDPCRetestCount {
			pending.TestCount++
			log.Infof("VerifyPending no IP/DNS: TestCount %d: %s for %+v\n",
				pending.TestCount, errStr, pending.PendDNS)
			return types.DPC_IPDNS_WAIT
		} else {
			log.Errorf("VerifyPending no IP/DNS: exceeded TestCount: %s for %+v\n",
				errStr, pending.PendDNS)
			pending.PendDPC.RecordFailure(errStr)
			return types.DPC_FAIL
		}
	}
	log.Errorf("VerifyPending: %s\n", errStr)
	pending.TestCount = MaxDPCRetestCount
	pending.PendDPC.RecordFailure(errStr)
	pending.PendDPC.LastIPAndDNS = pending.PendDPC.LastFailed
	return types.DPC_FAIL_WITH_IPANDDNS
}

// Check if all interfaces exist in the kernel
// Returns the ifname for the first missing if there is an error
func checkInterfacesExists(log *base.LogObject, dpc types.DevicePortConfig) (string, error) {

	for _, nuc := range dpc.Ports {
		// Check the ifname exists
		_, err := IfnameToIndex(log, nuc.IfName)
		if err != nil {
			return nuc.IfName, err
		}
	}
	return "", nil
}

func VerifyDevicePortConfig(ctx *DeviceNetworkContext) {
	log := ctx.Log
	log.Infof("VerifyDevicePortConfig()\n")
	if !ctx.Pending.Inprogress {
		log.Infof("VerifyDevicePortConfig() not Inprogress\n")
		return
	}
	// Stop network test timer.
	// It shall be resumed when we find working network configuration.
	ctx.NetworkTestTimer.Stop()

	ctx.NetworkTestBetterTimer.Stop()
	pending := &ctx.Pending

	passed := false
	for !passed {
		res := VerifyPending(ctx, &ctx.Pending, ctx.AssignableAdapters,
			ctx.TestSendTimeout)
		dpc := &ctx.Pending.PendDPC
		dpc.State = res
		ctx.PubDummyDevicePortConfig.Publish(dpc.PubKey(), *dpc)
		ctx.Pending.PendDNS.State = dpc.State
		UpdateResolvConf(log, ctx.Pending.PendDNS)
		UpdatePBR(log, ctx.Pending.PendDNS)
		// Publish in case we need a port back from domainmgr
		if ctx.PubDeviceNetworkStatus != nil {
			ctx.Pending.PendDNS.Testing = true
			ctx.Pending.PendDNS.State = res
			log.Infof("PublishDeviceNetworkStatus: pending %+v\n",
				ctx.Pending.PendDNS)
			ctx.PubDeviceNetworkStatus.Publish("global", ctx.Pending.PendDNS)
		}
		log.Infof("VerifyDevicePortConfig: %s for %d",
			res.String(), ctx.NextDPCIndex)
		switch res {
		case types.DPC_PCI_WAIT:
			// We have already published the new DNS for domainmgr.
			// Wait until we hear from domainmgr before applying (dhcp enable/disable)
			// and testing this new configuration.
			return
		case types.DPC_IPDNS_WAIT, types.DPC_INTF_WAIT:
			// Either addressChange or PendTimer will result in calling us again.
			duration := time.Duration(ctx.DPCTestDuration) * time.Second
			pending.PendTimer = time.NewTimer(duration)
			return
		case types.DPC_FAIL, types.DPC_FAIL_WITH_IPANDDNS:
			// Avoid clobbering wrong entry if insert/remove after verification
			// started
			tested, index := lookupPortConfig(ctx, pending.PendDPC)
			if tested != nil {
				log.Infof("At %d updating PortConfig %d on DPC_FAIL %+v\n",
					ctx.NextDPCIndex, index, tested)
				*tested = pending.PendDPC
			} else {
				log.Warnf("Not updating list on DPC_FAIL due key mismatch %s vs %s\n",
					ctx.DevicePortConfigList.PortConfigList[ctx.NextDPCIndex].Key,
					pending.PendDPC.Key)
			}
			*ctx.DevicePortConfigList = compressAndPublishDevicePortConfigList(ctx)
			if ctx.DevicePortConfigList.PortConfigList[0].IsDPCUntested() ||
				ctx.DevicePortConfigList.PortConfigList[0].WasDPCWorking() {
				log.Warn("VerifyDevicePortConfig DPC_FAIL: New DPC arrived " +
					"or an old working DPC ascended to the top of DPC list " +
					"while network testing was in progress. Restarting DPC verification.")
				SetupVerify(ctx, 0)
				continue
			}

			// Move to next index (including wrap around)
			// Skip entries with LastFailed after LastSucceeded and
			// a recent LastFailed (a minute or less).
			nextIndex := getNextTestableDPCIndex(ctx,
				ctx.NextDPCIndex+1)
			if nextIndex == -1 {
				log.Infof("VerifyDevicePortConfig: nothing testable")
				if res == types.DPC_FAIL_WITH_IPANDDNS {
					// publish what we have since applications
					// might need it
					ctx.DevicePortConfigList.CurrentIndex = ctx.NextDPCIndex
					*ctx.DevicePortConfig = pending.PendDPC
					*ctx.DeviceNetworkStatus = pending.PendDNS
					ctx.DeviceNetworkStatus.Testing = false
					*ctx.DevicePortConfigList = compressAndPublishDevicePortConfigList(ctx)
					DoDNSUpdate(ctx)
				}
				pending.Inprogress = false
				// Restart network test timer
				duration := time.Duration(ctx.NetworkTestInterval) * time.Second
				ctx.NetworkTestTimer = time.NewTimer(duration)
				return
			}
			SetupVerify(ctx, nextIndex)
			continue

		case types.DPC_SUCCESS, types.DPC_REMOTE_WAIT:
			// We treat DPC_REMOTE_WAIT as DPC_SUCCESS because we manage to connect to the controller
			// and we need to wait for certificate or ECONNREFUSED fix on the server side
			// Avoid clobbering wrong entry if insert/remove after verification
			// started
			tested, index := lookupPortConfig(ctx, pending.PendDPC)
			if tested != nil {
				log.Infof("At %d updating PortConfig %d on %s %+v\n",
					ctx.NextDPCIndex, index, res.String(), tested)
				*tested = pending.PendDPC
			} else {
				log.Warnf("Not updating list on %s due key mismatch %s vs %s\n",
					res.String(), ctx.DevicePortConfigList.PortConfigList[ctx.NextDPCIndex].Key,
					pending.PendDPC.Key)
			}
			passed = true
			if ctx.NextDPCIndex == 0 {
				log.Infof("VerifyDevicePortConfig: Working DPC configuration found "+
					"at index %d in DPC list",
					ctx.NextDPCIndex)
			} else {
				log.Warnf("VerifyDevicePortConfig: Working DPC configuration found "+
					"at index %d in DPC list",
					ctx.NextDPCIndex)
				if ctx.NetworkTestBetterInterval != 0 {
					// Look for a better choice in a while
					duration := time.Duration(ctx.NetworkTestBetterInterval) * time.Second
					ctx.NetworkTestBetterTimer = time.NewTimer(duration)
				}
			}
		}
	}
	// Found a working one
	ctx.DevicePortConfigList.CurrentIndex = ctx.NextDPCIndex
	*ctx.DevicePortConfig = pending.PendDPC
	*ctx.DeviceNetworkStatus = pending.PendDNS
	ctx.DeviceNetworkStatus.Testing = false
	*ctx.DevicePortConfigList = compressAndPublishDevicePortConfigList(ctx)
	DoDNSUpdate(ctx)

	pending.Inprogress = false

	// Did we get a new at index zero?
	if ctx.DevicePortConfigList.PortConfigList[0].IsDPCUntested() {
		log.Warn("VerifyDevicePortConfig DPC_SUCCESS: New DPC arrived " +
			"or a old working DPC moved up to top of DPC list while network testing " +
			"was in progress. Restarting DPC verification.")
		RestartVerify(ctx, "VerifyDevicePortConfig DPC_SUCCESS")
		return
	}

	// We just found a new DPC that restored our cloud connectivity.
	ctx.CloudConnectivityWorks = true

	// Restart network test timer
	duration := time.Duration(ctx.NetworkTestInterval) * time.Second
	ctx.NetworkTestTimer = time.NewTimer(duration)
}

// Move to next index (including wrap around)
// Skip entries with LastFailed after LastSucceeded and
// a recent LastFailed (a minute or less).
// Also skip entries with no management IP addresses
func getNextTestableDPCIndex(ctx *DeviceNetworkContext, start int) int {

	log := ctx.Log
	log.Infof("getNextTestableDPCIndex: start %d\n", start)
	// We want to wrap around, but should not keep looping around.
	// We do one loop of the entire list searching for a testable candidate.
	// If no suitable test candidate is found, we reset the test index to -1.
	dpcListLen := len(ctx.DevicePortConfigList.PortConfigList)
	if dpcListLen == 0 {
		newIndex := -1
		log.Infof("getNextTestableDPCIndex: empty list; current index %d new %d\n", ctx.NextDPCIndex,
			newIndex)
		return newIndex
	}
	found := false
	count := 0
	newIndex := start % dpcListLen
	for !found && count < dpcListLen {
		ok := ctx.DevicePortConfigList.PortConfigList[newIndex].IsDPCTestable()
		if ok {
			break
		}
		log.Infof("getNextTestableDPCIndex: DPC %v is not testable",
			ctx.DevicePortConfigList.PortConfigList[newIndex])
		newIndex = (newIndex + 1) % dpcListLen
		count += 1
	}
	if count == dpcListLen {
		newIndex = -1
	}
	log.Infof("getNextTestableDPCIndex: current index %d new %d\n", ctx.NextDPCIndex,
		newIndex)
	return newIndex
}

func getCurrentDPC(ctx *DeviceNetworkContext) *types.DevicePortConfig {
	if len(ctx.DevicePortConfigList.PortConfigList) == 0 ||
		ctx.NextDPCIndex < 0 ||
		ctx.NextDPCIndex >= len(ctx.DevicePortConfigList.PortConfigList) {
		return nil
	}
	return &ctx.DevicePortConfigList.PortConfigList[ctx.NextDPCIndex]
}

// Handle three different sources in this priority order:
// 1. zedagent with any key
// 2. "override" key from build or USB stick file
// 3. "lastresort" derived from the set of network interfaces
// We determine the priority from TimePriority in the config.
func HandleDPCModify(ctxArg interface{}, key string, configArg interface{}) {

	portConfig := configArg.(types.DevicePortConfig)
	ctx := ctxArg.(*DeviceNetworkContext)
	log := ctx.Log

	log.Infof("HandleDPCModify: key: %s, Current Config: %+v, portConfig: %+v\n",
		key, ctx.DevicePortConfig, portConfig)

	portConfig.DoSanitize(log, true, true, key, true)
	mgmtCount := portConfig.CountMgmtPorts()
	if mgmtCount == 0 {
		// This DPC will be ignored when we check IsDPCUsable which
		// is called from IsDPCTestable and IsDPCUntested.
		log.Warnf("Received DevicePortConfig key %s has no management ports; will be ignored",
			portConfig.Key)
	}

	// XXX really need to know whether anything with current or lower
	// index has changed. We don't care about inserts at the end of the list.

	configChanged := ctx.doUpdatePortConfigListAndPublish(&portConfig, false)
	// We could have just booted up and not run RestartVerify even once.
	// If we see a DPC configuration that we already have in the persistent
	// DPC list that we load from storage, we will return with out testing it.
	// In such case we end up not having any working DeviceNetworkStatus (no ips).
	// When the current DeviceNetworkStatus does not have any usable IP addresses,
	// we should go ahead and call RestartVerify even when "configChanged" is false.
	// Also if we have no working one (index -1) we restart.
	ipAddrCount := types.CountLocalIPv4AddrAnyNoLinkLocal(*ctx.DeviceNetworkStatus)
	numDNSServers := types.CountDNSServers(*ctx.DeviceNetworkStatus, "")
	if !configChanged && ipAddrCount > 0 && numDNSServers > 0 && ctx.DevicePortConfigList.CurrentIndex != -1 {
		log.Infof("HandleDPCModify: Config already current. No changes to process\n")
		return
	}

	RestartVerify(ctx, "HandleDPCModify")
	log.Infof("HandleDPCModify done for %s\n", key)
}

//
func HandleDPCDelete(ctxArg interface{}, key string, configArg interface{}) {

	ctx := ctxArg.(*DeviceNetworkContext)
	log := ctx.Log

	log.Infof("HandleDPCDelete for %s\n", key)
	portConfig := configArg.(types.DevicePortConfig)

	log.Infof("HandleDPCDelete for %s current time %v deleted time %v\n",
		key, ctx.DevicePortConfig.TimePriority, portConfig.TimePriority)

	portConfig.DoSanitize(log, false, true, key, true)

	configChanged := ctx.doUpdatePortConfigListAndPublish(&portConfig, true)
	if !configChanged {
		log.Infof("HandleDPCDelete: System current. No change detected.\n")
		return
	}

	RestartVerify(ctx, "HandleDPCDelete")
	log.Infof("HandleDPCDelete done for %s\n", key)
}

// HandleAssignableAdaptersModify - Handle Assignable Adapter list modifications
func HandleAssignableAdaptersModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*DeviceNetworkContext)
	log := ctx.Log

	if key != "global" {
		log.Infof("HandleAssignableAdaptersModify: ignoring %s\n", key)
		return
	}
	newAssignableAdapters := statusArg.(types.AssignableAdapters)
	log.Infof("HandleAssignableAdaptersModify() %+v\n", newAssignableAdapters)

	// ctxArg is DeviceNetworkContext
	for _, ioBundle := range newAssignableAdapters.IoBundleList {
		if !ioBundle.Type.IsNet() {
			continue
		}
		if ctx.AssignableAdapters != nil {
			currentIoBundle := ctx.AssignableAdapters.LookupIoBundlePhylabel(
				ioBundle.Phylabel)
			if currentIoBundle != nil &&
				ioBundle.IsPCIBack == currentIoBundle.IsPCIBack {
				log.Infof("HandleAssignableAdaptersModify(): ioBundle (%+v) "+
					"PCIBack status (%+v) unchanged\n",
					ioBundle.Phylabel, ioBundle.IsPCIBack)
				continue
			}
		} else {
			log.Infof("HandleAssignableAdaptersModify(): " +
				"ctx.AssignableAdapters = nil\n")
		}
		if ioBundle.IsPCIBack {
			log.Infof("HandleAssignableAdaptersModify(): ioBundle (%+v) changed "+
				"to pciBack", ioBundle.Phylabel)
			// Interface put back in pciBack list.
			// Stop dhcp and update DeviceNetworkStatus
			//doDhcpClientInactivate()  KALYAN- FIXTHIS BEFORE MERGE
		} else {
			log.Infof("HandleAssignableAdaptersModify(): ioBundle (%+v) changed "+
				"to pciBack=false", ioBundle.Phylabel)
			// Interface moved out of PciBack mode.
		}
	}
	*ctx.AssignableAdapters = newAssignableAdapters
	// In case a verification is in progress and is waiting for return from pciback
	if ctx.Pending.Inprogress {
		VerifyDevicePortConfig(ctx)
	}
	log.Infof("handleAssignableAdaptersModify() done\n")
}

// HandleAssignableAdaptersModify - Handle Assignable Adapter list deletions
func HandleAssignableAdaptersDelete(ctxArg interface{}, key string,
	configArg interface{}) {

	ctx := ctxArg.(*DeviceNetworkContext)
	log := ctx.Log

	// this usually happens only at restart - as any changes to assignable
	//   adapters results in domain restart and takes affect only after
	//   the restart.

	// UsbAccess can change dynamically - but it is not network device,
	// so can be ignored. Assuming there are no USB based network interfaces.
	log.Infof("HandleAssignableAdaptersDelete done for %s\n", key)
}

// IngestPortConfigList creates and republishes the initial list
// Removes useless ones (which might be re-added by the controller/zedagent
// later but at least they are not in the way during boot)
func IngestPortConfigList(ctx *DeviceNetworkContext) {
	log := ctx.Log
	log.Infof("IngestPortConfigList")
	item, err := ctx.PubDevicePortConfigList.Get("global")
	var storedDpcl types.DevicePortConfigList
	if err != nil {
		log.Errorf("No global key for DevicePortConfigList")
		storedDpcl = types.DevicePortConfigList{}
	} else {
		storedDpcl = item.(types.DevicePortConfigList)
	}
	log.Infof("Initial DPCL %v", storedDpcl)
	var dpcl types.DevicePortConfigList
	for _, portConfig := range storedDpcl.PortConfigList {
		if portConfig.CountMgmtPorts() == 0 {
			log.Warnf("Stored DevicePortConfig key %s has no management ports; ignored",
				portConfig.Key)
			continue
		}
		dpcl.PortConfigList = append(dpcl.PortConfigList, portConfig)
	}
	ctx.DevicePortConfigList = &dpcl
	log.Infof("Sanitized DPCL %v", dpcl)
	*ctx.DevicePortConfigList = compressAndPublishDevicePortConfigList(ctx)
	ctx.DevicePortConfigList.CurrentIndex = -1 // No known working one
	log.Infof("Published DPCL %v", ctx.DevicePortConfigList)
	log.Infof("IngestPortConfigList len %d", len(ctx.DevicePortConfigList.PortConfigList))
}

// First look for matching timestamp, then compare for identical content
// This is needed since after a restart zedagent will provide new timestamps
// even if we persisted the DevicePortConfig before the restart.
func lookupPortConfig(ctx *DeviceNetworkContext,
	portConfig types.DevicePortConfig) (*types.DevicePortConfig, int) {

	log := ctx.Log
	for i, port := range ctx.DevicePortConfigList.PortConfigList {
		if port.Version == portConfig.Version &&
			port.Key == portConfig.Key &&
			port.TimePriority == portConfig.TimePriority {

			log.Infof("lookupPortConfig timestamp found +%v\n",
				port)
			return &ctx.DevicePortConfigList.PortConfigList[i], i
		}
	}
	for i, port := range ctx.DevicePortConfigList.PortConfigList {
		if port.Version == portConfig.Version &&
			port.MostlyEqual(&portConfig) {
			log.Infof("lookupPortConfig MostlyEqual found +%v\n",
				port)
			return &ctx.DevicePortConfigList.PortConfigList[i], i
		}
	}
	return nil, 0
}

// doUpdatePortConfigListAndPublish
//		Returns if the current config has actually changed.
func (ctx *DeviceNetworkContext) doUpdatePortConfigListAndPublish(
	portConfig *types.DevicePortConfig, delete bool) bool {
	// Look up based on timestamp, then content

	log := ctx.Log
	current := getCurrentDPC(ctx) // Used to determine if index needs to change
	currentIndex := ctx.DevicePortConfigList.CurrentIndex
	oldConfig, _ := lookupPortConfig(ctx, *portConfig)

	if delete {
		if oldConfig == nil {
			log.Errorf("doUpdatePortConfigListAndPublish - Delete. "+
				"Config not found: %+v\n", portConfig)
			return false
		}
		log.Infof("doUpdatePortConfigListAndPublish: Delete. "+
			"oldCOnfig %+v found: %+v\n", *oldConfig, portConfig)
		removePortConfig(ctx, *oldConfig)
	} else if oldConfig != nil {
		// Compare everything but TimePriority since that is
		// modified by zedagent even if there are no changes.
		// If we modify the timestamp for other than current
		// then treat as a change since it could have moved up
		// in the list.
		if oldConfig.MostlyEqual(portConfig) {
			log.Infof("doUpdatePortConfigListAndPublish: no change but timestamps %v %v\n",
				oldConfig.TimePriority, portConfig.TimePriority)

			// If this is current and current is in use (index=0)
			// then no work needed. Otherwise we reorder
			if current != nil && current.MostlyEqual(oldConfig) &&
				currentIndex == 0 {

				log.Infof("doUpdatePortConfigListAndPublish: no change and same Ports as currentIndex=0")
				return false
			}
			log.Infof("doUpdatePortConfigListAndPublish: changed ports from current; reorder\n")
		} else {
			log.Infof("doUpdatePortConfigListAndPublish: change from %+v to %+v\n",
				*oldConfig, portConfig)
		}
		updatePortConfig(ctx, oldConfig, *portConfig)
	} else {
		insertPortConfig(ctx, *portConfig)
	}
	// Check if current moved to a different index or was deleted
	if current == nil {
		// No current index to update
		log.Infof("doUpdatePortConfigListAndPublish: no current %d",
			currentIndex)
		*ctx.DevicePortConfigList = compressAndPublishDevicePortConfigList(ctx)
		return true
	}
	newplace, newIndex := lookupPortConfig(ctx, *current)
	if newplace == nil {
		// Current Got deleted. If [0] was working we stick to it, otherwise we
		// restart looking through the list.
		if len(ctx.DevicePortConfigList.PortConfigList) != 0 &&
			ctx.DevicePortConfigList.PortConfigList[0].WasDPCWorking() {
			ctx.DevicePortConfigList.CurrentIndex = 0
		} else {
			ctx.DevicePortConfigList.CurrentIndex = -1
		}
	} else if newIndex != currentIndex {
		log.Infof("doUpdatePortConfigListAndPublish: current %d moved to %d",
			currentIndex, newIndex)
		if ctx.DevicePortConfigList.PortConfigList[newIndex].WasDPCWorking() {
			ctx.DevicePortConfigList.CurrentIndex = newIndex
		} else {
			ctx.DevicePortConfigList.CurrentIndex = -1
		}
	}
	*ctx.DevicePortConfigList = compressAndPublishDevicePortConfigList(ctx)
	return true
}

func checkAndUpdateWireless(ctx *DeviceNetworkContext, oCfg *types.DevicePortConfig, portCfg *types.DevicePortConfig) {

	log := ctx.Log
	log.Infof("checkAndUpdateWireless: oCfg type %v, nil %v, portCfg Ports %v\n", portCfg.Key, oCfg == nil, portCfg.Ports)
	for _, pCfg := range portCfg.Ports {
		var oldPortCfg *types.NetworkPortConfig
		if oCfg != nil {
			for _, old := range oCfg.Ports {
				if old.IfName == pCfg.IfName {
					oldPortCfg = &old
					break
				}
			}
		}
		if oldPortCfg == nil || !reflect.DeepEqual(oldPortCfg.WirelessCfg, pCfg.WirelessCfg) {
			if pCfg.WirelessCfg.WType == types.WirelessTypeCellular ||
				oldPortCfg != nil && oldPortCfg.WirelessCfg.WType == types.WirelessTypeCellular {
				devPortInstallAPname(log, pCfg.IfName, pCfg.WirelessCfg)
			} else if pCfg.WirelessCfg.WType == types.WirelessTypeWifi ||
				oldPortCfg != nil && oldPortCfg.WirelessCfg.WType == types.WirelessTypeWifi {
				status := devPortInstallWifiConfig(ctx, pCfg.IfName, pCfg.WirelessCfg)
				log.Infof("checkAndUpdateWireless: updated wpa file ok %v\n", status)
			}
		}
	}
}

// Update content and move if the timestamp changed
func updatePortConfig(ctx *DeviceNetworkContext, oldConfig *types.DevicePortConfig, portConfig types.DevicePortConfig) {

	log := ctx.Log
	if oldConfig.TimePriority == portConfig.TimePriority {
		log.Infof("updatePortConfig: same time update %+v\n",
			portConfig)
		*oldConfig = portConfig
		return
	}
	// Preserve TestResults and Last*
	portConfig.TestResults = oldConfig.TestResults
	portConfig.LastIPAndDNS = oldConfig.LastIPAndDNS
	log.Infof("updatePortConfig: diff time remove+add  %+v\n",
		portConfig)
	removePortConfig(ctx, *oldConfig)
	insertPortConfig(ctx, portConfig)
}

// Insert in reverse timestamp order
func insertPortConfig(ctx *DeviceNetworkContext, portConfig types.DevicePortConfig) {

	log := ctx.Log
	var newConfig []types.DevicePortConfig
	inserted := false
	for _, port := range ctx.DevicePortConfigList.PortConfigList {
		if !inserted && portConfig.TimePriority.After(port.TimePriority) {
			log.Infof("insertPortConfig: %+v before %+v\n",
				portConfig, port)
			newConfig = append(newConfig, portConfig)
			inserted = true
		}
		newConfig = append(newConfig, port)
	}
	if !inserted {
		log.Infof("insertPortConfig: at end %+v\n", portConfig)
		newConfig = append(newConfig, portConfig)
	}
	ctx.DevicePortConfigList.PortConfigList = newConfig
}

// Remove by matching TimePriority and Key
func removePortConfig(ctx *DeviceNetworkContext, portConfig types.DevicePortConfig) {
	log := ctx.Log
	var newConfig []types.DevicePortConfig
	removed := false
	for _, port := range ctx.DevicePortConfigList.PortConfigList {
		if !removed && portConfig.TimePriority == port.TimePriority &&
			portConfig.Key == port.Key {
			log.Infof("removePortConfig: found %+v for %+v\n",
				port, portConfig)
			removed = true
		} else {
			newConfig = append(newConfig, port)
		}
	}
	if !removed {
		log.Errorf("removePortConfig: not found %+v\n", portConfig)
		return
	}
	ctx.DevicePortConfigList.PortConfigList = newConfig
}

// DoDNSUpdate
//	Update the device network status and publish it.
func DoDNSUpdate(ctx *DeviceNetworkContext) {
	log := ctx.Log
	// Did we loose all usable addresses or gain the first usable
	// address?
	newAddrCount := types.CountLocalIPv4AddrAnyNoLinkLocal(*ctx.DeviceNetworkStatus)
	if newAddrCount != ctx.UsableAddressCount {
		log.Infof("DeviceNetworkStatus from %d to %d addresses\n",
			ctx.UsableAddressCount, newAddrCount)
		// ledmanager subscribes to DeviceNetworkStatus to see changes
		ctx.UsableAddressCount = newAddrCount
	}
	UpdateResolvConf(log, *ctx.DeviceNetworkStatus)
	UpdatePBR(log, *ctx.DeviceNetworkStatus)
	if ctx.PubDeviceNetworkStatus != nil {
		ctx.DeviceNetworkStatus.Testing = false
		log.Infof("PublishDeviceNetworkStatus: %+v\n",
			ctx.DeviceNetworkStatus)
		ctx.PubDeviceNetworkStatus.Publish("global",
			*ctx.DeviceNetworkStatus)
	}
	ctx.Changed = true
}

const destFilename = "/etc/resolv.conf"

// Track changes in DNS servers.
var lastServers []net.IP

// UpdateResolvConf produces a /etc/resolv.conf based on the management ports
// in DeviceNetworkStatus
func UpdateResolvConf(log *base.LogObject, globalStatus types.DeviceNetworkStatus) int {

	log.Infof("UpdateResolvConf")
	servers := types.GetDNSServers(globalStatus, "")
	if reflect.DeepEqual(lastServers, servers) {
		log.Infof("UpdateResolvConf: no change: %d", len(lastServers))
		return len(lastServers)
	}
	destfile, err := os.Create(destFilename)
	if err != nil {
		log.Errorln("Create ", err)
		return 0
	}
	defer destfile.Close()

	numAddrs := generateResolvConf(log, globalStatus, destfile)
	log.Infof("UpdateResolvConf DONE %d addrs", numAddrs)
	lastServers = servers
	return numAddrs
}

// Note that we don't add a search nor domainname option since
// it seems to mess up the retry logic
func generateResolvConf(log *base.LogObject, globalStatus types.DeviceNetworkStatus, destfile *os.File) int {
	destfile.WriteString("# Generated by nim\n")
	destfile.WriteString("# Do not edit\n")
	var written []net.IP
	log.Infof("generateResolvConf %d ports", len(globalStatus.Ports))
	for _, us := range globalStatus.Ports {
		if !us.IsMgmt {
			continue
		}
		log.Infof("generateResolvConf %s has %d servers: %v",
			us.IfName, len(us.DNSServers), us.DNSServers)
		destfile.WriteString(fmt.Sprintf("# From %s\n", us.IfName))
		// Avoid duplicate IP addresses for nameservers.
		for _, server := range us.DNSServers {
			duplicate := false
			for _, a := range written {
				if a.Equal(server) {
					duplicate = true
				}
			}
			if duplicate {
				destfile.WriteString(fmt.Sprintf("# nameserver %s\n",
					server))
			} else {
				destfile.WriteString(fmt.Sprintf("nameserver %s\n",
					server))
				written = append(written, server)
			}
		}
	}
	destfile.WriteString("options rotate\n")
	destfile.WriteString("options attempts:5\n")
	destfile.Sync()
	return len(written)
}
