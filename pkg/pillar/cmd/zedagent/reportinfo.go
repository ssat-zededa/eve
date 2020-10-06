// Copyright (c) 2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

// Pushes info to zedcloud

package zedagent

import (
	"bytes"
	"fmt"
	"github.com/lf-edge/eve/pkg/pillar/base"
	"os"
	"strings"
	"time"

	"github.com/eriknordmark/ipinfo"
	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes"
	"github.com/lf-edge/eve/api/go/evecommon"
	"github.com/lf-edge/eve/api/go/info"
	etpm "github.com/lf-edge/eve/pkg/pillar/evetpm"
	"github.com/lf-edge/eve/pkg/pillar/hardware"
	"github.com/lf-edge/eve/pkg/pillar/netclone"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/utils"
	"github.com/lf-edge/eve/pkg/pillar/vault"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
	"github.com/shirou/gopsutil/host"
)

var (
	nilIPInfo = ipinfo.IPInfo{}
)

func deviceInfoTask(ctxPtr *zedagentContext, triggerDeviceInfo <-chan struct{}) {

	// Run a periodic timer so we always update StillRunning
	stillRunning := time.NewTicker(25 * time.Second)

	for {
		select {
		case <-triggerDeviceInfo:
			start := time.Now()
			log.Info("deviceInfoTask got message")

			PublishDeviceInfoToZedCloud(ctxPtr)
			ctxPtr.iteration++
			log.Info("deviceInfoTask done with message")
			ctxPtr.ps.CheckMaxTimeTopic(agentName+"devinfo", "PublishDeviceInfo", start,
				warningTime, errorTime)
		case <-stillRunning.C:
		}
		ctxPtr.ps.StillRunning(agentName+"devinfo", warningTime, errorTime)
	}
}

// PublishDeviceInfoToZedCloud This function is called per change, hence needs to try over all management ports
func PublishDeviceInfoToZedCloud(ctx *zedagentContext) {
	aa := ctx.assignableAdapters
	iteration := ctx.iteration
	subBaseOsStatus := ctx.subBaseOsStatus

	var ReportInfo = &info.ZInfoMsg{}

	deviceType := new(info.ZInfoTypes)
	*deviceType = info.ZInfoTypes_ZiDevice
	ReportInfo.Ztype = *deviceType
	deviceUUID := zcdevUUID.String()
	ReportInfo.DevId = *proto.String(deviceUUID)
	ReportInfo.AtTimeStamp = ptypes.TimestampNow()
	log.Infof("PublishDeviceInfoToZedCloud uuid %s", deviceUUID)

	ReportDeviceInfo := new(info.ZInfoDevice)

	var machineArch string
	stdout, err := base.Exec(log, "uname", "-m").Output()
	if err != nil {
		log.Errorf("uname -m failed %s", err)
	} else {
		machineArch = string(stdout)
		ReportDeviceInfo.MachineArch = *proto.String(strings.TrimSpace(machineArch))
	}

	stdout, err = base.Exec(log, "uname", "-p").Output()
	if err != nil {
		log.Errorf("uname -p failed %s", err)
	} else {
		cpuArch := string(stdout)
		ReportDeviceInfo.CpuArch = *proto.String(strings.TrimSpace(cpuArch))
	}

	stdout, err = base.Exec(log, "uname", "-i").Output()
	if err != nil {
		log.Errorf("uname -i failed %s", err)
	} else {
		platform := string(stdout)
		ReportDeviceInfo.Platform = *proto.String(strings.TrimSpace(platform))
	}

	sub := ctx.getconfigCtx.subHostMemory
	m, _ := sub.Get("global")
	if m != nil {
		metric := m.(types.HostMemory)
		ReportDeviceInfo.Ncpu = *proto.Uint32(metric.Ncpus)
		ReportDeviceInfo.Memory = *proto.Uint64(metric.TotalMemoryMB)
	}
	// Find all disks and partitions
	for _, diskMetric := range getAllDiskMetrics(ctx) {
		var diskPath, mountPath string
		if diskMetric.IsDir {
			mountPath = diskMetric.DiskPath
		} else {
			diskPath = diskMetric.DiskPath
		}
		is := info.ZInfoStorage{
			Device:    diskPath,
			MountPath: mountPath,
			Total:     utils.RoundToMbytes(diskMetric.TotalBytes),
		}
		if diskMetric.DiskPath == types.PersistDir {
			is.StorageLocation = true
			ReportDeviceInfo.Storage += *proto.Uint64(utils.RoundToMbytes(diskMetric.TotalBytes))
		}

		ReportDeviceInfo.StorageList = append(ReportDeviceInfo.StorageList, &is)
	}

	ReportDeviceManufacturerInfo := new(info.ZInfoManufacturer)
	if strings.Contains(machineArch, "x86") {
		productManufacturer, productName, productVersion, productSerial, productUUID := hardware.GetDeviceManufacturerInfo(log)
		ReportDeviceManufacturerInfo.Manufacturer = *proto.String(strings.TrimSpace(productManufacturer))
		ReportDeviceManufacturerInfo.ProductName = *proto.String(strings.TrimSpace(productName))
		ReportDeviceManufacturerInfo.Version = *proto.String(strings.TrimSpace(productVersion))
		ReportDeviceManufacturerInfo.SerialNumber = *proto.String(strings.TrimSpace(productSerial))
		ReportDeviceManufacturerInfo.UUID = *proto.String(strings.TrimSpace(productUUID))

		biosVendor, biosVersion, biosReleaseDate := hardware.GetDeviceBios(log)
		ReportDeviceManufacturerInfo.BiosVendor = *proto.String(strings.TrimSpace(biosVendor))
		ReportDeviceManufacturerInfo.BiosVersion = *proto.String(strings.TrimSpace(biosVersion))
		ReportDeviceManufacturerInfo.BiosReleaseDate = *proto.String(strings.TrimSpace(biosReleaseDate))
	}
	compatible := hardware.GetCompatible(log)
	ReportDeviceManufacturerInfo.Compatible = *proto.String(compatible)
	ReportDeviceInfo.Minfo = ReportDeviceManufacturerInfo

	// Report BaseOs Status for the two partitions
	getBaseOsStatus := func(partLabel string) *types.BaseOsStatus {
		// Look for a matching IMGA/IMGB in baseOsStatus
		items := subBaseOsStatus.GetAll()
		for _, st := range items {
			bos := st.(types.BaseOsStatus)
			if bos.PartitionLabel == partLabel {
				return &bos
			}
		}
		return nil
	}
	getSwInfo := func(partLabel string) *info.ZInfoDevSW {
		swInfo := new(info.ZInfoDevSW)
		if bos := getBaseOsStatus(partLabel); bos != nil {
			// Get current state/version which is different than
			// what is on disk
			swInfo.Activated = bos.Activated
			swInfo.PartitionLabel = bos.PartitionLabel
			swInfo.PartitionDevice = bos.PartitionDevice
			swInfo.PartitionState = bos.PartitionState
			swInfo.Status = bos.State.ZSwState()
			swInfo.ShortVersion = bos.BaseOsVersion
			swInfo.LongVersion = "" // XXX
			if len(bos.ContentTreeStatusList) > 0 {
				// Assume one - pick first ContentTreeStatus
				swInfo.DownloadProgress = uint32(bos.ContentTreeStatusList[0].Progress)
			}
			if !bos.ErrorTime.IsZero() {
				log.Debugf("reportMetrics sending error time %v error %v for %s",
					bos.ErrorTime, bos.Error,
					bos.BaseOsVersion)
				swInfo.SwErr = encodeErrorInfo(bos.ErrorAndTime)
			}
			if swInfo.ShortVersion == "" {
				swInfo.Status = info.ZSwState_INITIAL
				swInfo.DownloadProgress = 0
			}
		} else {
			partStatus := getZbootPartitionStatus(ctx, partLabel)
			swInfo.PartitionLabel = partLabel
			if partStatus != nil {
				swInfo.Activated = partStatus.CurrentPartition
				swInfo.PartitionDevice = partStatus.PartitionDevname
				swInfo.PartitionState = partStatus.PartitionState
				swInfo.ShortVersion = partStatus.ShortVersion
				swInfo.LongVersion = partStatus.LongVersion
			}
			if swInfo.ShortVersion != "" {
				swInfo.Status = info.ZSwState_INSTALLED
				swInfo.DownloadProgress = 100
			} else {
				swInfo.Status = info.ZSwState_INITIAL
				swInfo.DownloadProgress = 0
			}
		}
		addUserSwInfo(ctx, swInfo)
		return swInfo
	}

	ReportDeviceInfo.SwList = make([]*info.ZInfoDevSW, 2)
	ReportDeviceInfo.SwList[0] = getSwInfo(getZbootCurrentPartition(ctx))
	ReportDeviceInfo.SwList[1] = getSwInfo(getZbootOtherPartition(ctx))
	// Report any other BaseOsStatus which might have errors
	items := subBaseOsStatus.GetAll()
	for _, st := range items {
		bos := st.(types.BaseOsStatus)
		if bos.PartitionLabel != "" {
			// Already reported above
			continue
		}
		log.Debugf("reportMetrics sending unattached bos for %s",
			bos.BaseOsVersion)
		swInfo := new(info.ZInfoDevSW)
		swInfo.Status = bos.State.ZSwState()
		swInfo.ShortVersion = bos.BaseOsVersion
		swInfo.LongVersion = "" // XXX
		if len(bos.ContentTreeStatusList) > 0 {
			// Assume one - pick first ContentTreeStatus
			swInfo.DownloadProgress = uint32(bos.ContentTreeStatusList[0].Progress)
		}
		if !bos.ErrorTime.IsZero() {
			log.Debugf("reportMetrics sending error time %v error %v for %s",
				bos.ErrorTime, bos.Error, bos.BaseOsVersion)
			swInfo.SwErr = encodeErrorInfo(bos.ErrorAndTime)
		}
		addUserSwInfo(ctx, swInfo)
		ReportDeviceInfo.SwList = append(ReportDeviceInfo.SwList,
			swInfo)
	}

	// We report all the ports in DeviceNetworkStatus
	labelList := types.ReportLogicallabels(*deviceNetworkStatus)
	for _, label := range labelList {
		p := deviceNetworkStatus.GetPortByLogicallabel(label)
		if p == nil {
			continue
		}
		ReportDeviceNetworkInfo := encodeNetInfo(*p)
		// XXX rename DevName to Logicallabel in proto file
		ReportDeviceNetworkInfo.DevName = *proto.String(label)
		ReportDeviceInfo.Network = append(ReportDeviceInfo.Network,
			ReportDeviceNetworkInfo)
	}
	// Fill in global ZInfoDNS dns from /etc/resolv.conf
	// Note that "domain" is returned in search, hence DNSdomain is
	// not filled in.
	dc := netclone.DnsReadConfig("/etc/resolv.conf")
	log.Debugf("resolv.conf servers %v", dc.Servers)
	log.Debugf("resolv.conf search %v", dc.Search)

	ReportDeviceInfo.Dns = new(info.ZInfoDNS)
	ReportDeviceInfo.Dns.DNSservers = dc.Servers
	ReportDeviceInfo.Dns.DNSsearch = dc.Search

	// Report AssignableAdapters.
	// Domainmgr excludes adapters which do not currently exist in
	// what it publishes.
	// We also mark current management ports as such.
	var seenBundles []string
	for _, ib := range aa.IoBundleList {
		// Report each group once
		seen := false
		for _, s := range seenBundles {
			if s == ib.AssignmentGroup {
				seen = true
				break
			}
		}
		if seen && ib.AssignmentGroup != "" {
			continue
		}
		seenBundles = append(seenBundles, ib.AssignmentGroup)
		reportAA := new(info.ZioBundle)
		reportAA.Type = evecommon.PhyIoType(ib.Type)
		reportAA.Name = ib.AssignmentGroup
		// XXX - Cast is needed because PhyIoMemberUsage was replicated in info
		//  When this is fixed, we can remove this case.
		reportAA.Usage = evecommon.PhyIoMemberUsage(ib.Usage)
		list := aa.LookupIoBundleGroup(ib.AssignmentGroup)
		if len(list) == 0 {
			if ib.AssignmentGroup != "" {
				log.Infof("Nothing to report for %d %s",
					ib.Type, ib.AssignmentGroup)
				continue
			}
			// Singleton
			list = append(list, &ib)
		}
		for _, b := range list {
			if b == nil {
				continue
			}
			reportAA.Members = append(reportAA.Members,
				b.Phylabel)
			if b.MacAddr != "" {
				reportMac := new(info.IoAddresses)
				reportMac.MacAddress = b.MacAddr
				reportAA.IoAddressList = append(reportAA.IoAddressList,
					reportMac)
			}
		}
		if ib.IsPort {
			reportAA.UsedByBaseOS = true
		} else if ib.UsedByUUID != nilUUID {
			reportAA.UsedByAppUUID = ib.UsedByUUID.String()
		}
		log.Debugf("AssignableAdapters for %s macs %v",
			reportAA.Name, reportAA.IoAddressList)
		ReportDeviceInfo.AssignableAdapters = append(ReportDeviceInfo.AssignableAdapters,
			reportAA)
	}

	hinfo, err := host.Info()
	if err != nil {
		log.Fatalf("host.Info(): %s", err)
	}
	log.Debugf("uptime %d = %d days",
		hinfo.Uptime, hinfo.Uptime/(3600*24))
	log.Debugf("Booted at %v", time.Unix(int64(hinfo.BootTime), 0).UTC())

	bootTime, _ := ptypes.TimestampProto(
		time.Unix(int64(hinfo.BootTime), 0).UTC())
	ReportDeviceInfo.BootTime = bootTime
	hostname, err := os.Hostname()
	if err != nil {
		log.Errorf("HostName failed: %s", err)
	} else {
		ReportDeviceInfo.HostName = hostname
	}

	// Note that these are associated with the device and not with a
	// device name like ppp0 or wwan0
	lte := readLTEInfo()
	lteNets := readLTENetworks()
	if lteNets != nil {
		lte = append(lte, lteNets...)
	}
	for _, i := range lte {
		item := new(info.DeprecatedMetricItem)
		item.Key = i.Key
		item.Type = info.DepMetricItemType(i.Type)
		// setDeprecatedMetricAnyValue(item, i.Value)
		ReportDeviceInfo.MetricItems = append(ReportDeviceInfo.MetricItems, item)
	}

	ReportDeviceInfo.LastRebootReason = ctx.rebootReason
	ReportDeviceInfo.LastRebootStack = ctx.rebootStack
	if !ctx.rebootTime.IsZero() {
		rebootTime, _ := ptypes.TimestampProto(ctx.rebootTime)
		ReportDeviceInfo.LastRebootTime = rebootTime
	}

	ReportDeviceInfo.SystemAdapter = encodeSystemAdapterInfo(ctx)

	ReportDeviceInfo.RestartCounter = ctx.restartCounter
	ReportDeviceInfo.RebootConfigCounter = ctx.rebootConfigCounter

	//Operational information about TPM presence/absence/usage.
	ReportDeviceInfo.HSMStatus = etpm.FetchTpmSwStatus()
	ReportDeviceInfo.HSMInfo, _ = etpm.FetchTpmHwInfo()

	//Operational information about Data Security At Rest
	ReportDataSecAtRestInfo := getDataSecAtRestInfo(ctx)

	//This will be removed after new fields propagate to Controller.
	ReportDataSecAtRestInfo.Status, ReportDataSecAtRestInfo.Info =
		vault.GetOperInfo(log)
	ReportDeviceInfo.DataSecAtRestInfo = ReportDataSecAtRestInfo

	// Add SecurityInfo
	ReportDeviceInfo.SecInfo = getSecurityInfo(ctx)

	ReportInfo.InfoContent = new(info.ZInfoMsg_Dinfo)
	if x, ok := ReportInfo.GetInfoContent().(*info.ZInfoMsg_Dinfo); ok {
		x.Dinfo = ReportDeviceInfo
	}

	// Add ConfigItems to the DeviceInfo
	ReportDeviceInfo.ConfigItemStatus = createConfigItemStatus(ctx.globalStatus)

	// Add AppInstances to the DeviceInfo. We send a list of all AppInstances
	// currently on the device - even if the corresponding AppInstanceConfig
	// is deleted.
	createAppInstances(ctx, ReportDeviceInfo)

	log.Debugf("PublishDeviceInfoToZedCloud sending %v", ReportInfo)
	data, err := proto.Marshal(ReportInfo)
	if err != nil {
		log.Fatal("PublishDeviceInfoToZedCloud proto marshaling error: ", err)
	}

	statusUrl := zedcloud.URLPathString(serverNameAndPort, zedcloudCtx.V2API, devUUID, "info")
	zedcloud.RemoveDeferred(zedcloudCtx, deviceUUID)
	buf := bytes.NewBuffer(data)
	if buf == nil {
		log.Fatal("malloc error")
	}
	size := int64(proto.Size(ReportInfo))
	err = SendProtobuf(statusUrl, buf, size, iteration)
	if err != nil {
		log.Errorf("PublishDeviceInfoToZedCloud failed: %s", err)
		// Try sending later
		// The buf might have been consumed
		buf := bytes.NewBuffer(data)
		if buf == nil {
			log.Fatal("malloc error")
		}
		zedcloud.SetDeferred(zedcloudCtx, deviceUUID, buf, size,
			statusUrl, true)
	} else {
		writeSentDeviceInfoProtoMessage(data)
	}
}

// Convert the implementation details to the user-friendly userStatus and subStatus*
func addUserSwInfo(ctx *zedagentContext, swInfo *info.ZInfoDevSW) {
	switch swInfo.Status {
	case info.ZSwState_INITIAL:
		// If Unused and partitionLabel is set them it
		// is the uninitialized IMGB partition which we don't report
		if swInfo.PartitionState == "unused" &&
			swInfo.PartitionLabel != "" {

			swInfo.UserStatus = info.BaseOsStatus_NONE
		} else if swInfo.ShortVersion == "" {
			swInfo.UserStatus = info.BaseOsStatus_NONE
		} else {
			swInfo.UserStatus = info.BaseOsStatus_UPDATING
			swInfo.SubStatus = info.BaseOsSubStatus_UPDATE_INITIALIZING
			swInfo.SubStatusStr = "Initializing update"
		}
	case info.ZSwState_DOWNLOAD_STARTED:
		swInfo.UserStatus = info.BaseOsStatus_DOWNLOADING
		swInfo.SubStatus = info.BaseOsSubStatus_DOWNLOAD_INPROGRESS
		swInfo.SubStatusProgress = swInfo.DownloadProgress
		swInfo.SubStatusStr = fmt.Sprintf("Download %d%% done",
			swInfo.SubStatusProgress)
	case info.ZSwState_DOWNLOADED:
		if swInfo.Activated {
			swInfo.UserStatus = info.BaseOsStatus_DOWNLOADING
			swInfo.SubStatus = info.BaseOsSubStatus_DOWNLOAD_INPROGRESS
			swInfo.SubStatusProgress = 100
			swInfo.SubStatusStr = "Download 100% done"
		} else {
			swInfo.UserStatus = info.BaseOsStatus_NONE
		}
	case info.ZSwState_DELIVERED:
		if swInfo.Activated {
			swInfo.UserStatus = info.BaseOsStatus_DOWNLOAD_DONE
			swInfo.SubStatusStr = "Downloaded and verified"
		} else {
			swInfo.UserStatus = info.BaseOsStatus_NONE
		}
	case info.ZSwState_INSTALLED:
		switch swInfo.PartitionState {
		case "active":
			if swInfo.Activated {
				swInfo.UserStatus = info.BaseOsStatus_UPDATED
			} else {
				swInfo.UserStatus = info.BaseOsStatus_FALLBACK
			}
		case "updating":
			swInfo.UserStatus = info.BaseOsStatus_UPDATING
			swInfo.SubStatus = info.BaseOsSubStatus_UPDATE_REBOOTING
			// XXX progress based on time left??
			swInfo.SubStatusStr = "About to reboot"
		case "inprogress":
			if swInfo.Activated {
				swInfo.UserStatus = info.BaseOsStatus_UPDATING
				swInfo.SubStatus = info.BaseOsSubStatus_UPDATE_TESTING
				swInfo.SubStatusProgress = uint32(ctx.remainingTestTime / time.Second)
				swInfo.SubStatusStr = fmt.Sprintf("Testing for %d more seconds",
					swInfo.SubStatusProgress)
			} else {
				swInfo.UserStatus = info.BaseOsStatus_FAILED
			}

		case "unused":
			swInfo.UserStatus = info.BaseOsStatus_NONE
		}
	default:
		// The other states are use for app instances not for baseos
		swInfo.UserStatus = info.BaseOsStatus_NONE
	}
	if swInfo.SwErr != nil && swInfo.SwErr.Description != "" {
		swInfo.UserStatus = info.BaseOsStatus_FAILED
	}
}

// encodeNetInfo encodes info from the port
func encodeNetInfo(port types.NetworkPortStatus) *info.ZInfoNetwork {

	networkInfo := new(info.ZInfoNetwork)
	networkInfo.LocalName = *proto.String(port.IfName)
	networkInfo.IPAddrs = make([]string, len(port.AddrInfoList))
	for index, ai := range port.AddrInfoList {
		networkInfo.IPAddrs[index] = *proto.String(ai.Addr.String())
	}
	networkInfo.Up = port.Up
	networkInfo.MacAddr = *proto.String(port.MacAddr)

	// In case caller doesn't override
	networkInfo.DevName = *proto.String(port.IfName)

	networkInfo.Alias = *proto.String(port.Alias)
	// Default routers from kernel whether or not we are using DHCP
	networkInfo.DefaultRouters = make([]string, len(port.DefaultRouters))
	for index, dr := range port.DefaultRouters {
		networkInfo.DefaultRouters[index] = *proto.String(dr.String())
	}

	networkInfo.Uplink = port.IsMgmt
	// fill in ZInfoDNS from what is currently used
	networkInfo.Dns = new(info.ZInfoDNS)
	networkInfo.Dns.DNSdomain = port.DomainName
	for _, server := range port.DNSServers {
		networkInfo.Dns.DNSservers = append(networkInfo.Dns.DNSservers,
			server.String())
	}

	// XXX we potentially have geoloc information for each IP
	// address.
	// For now fill in using the first IP address which has location
	// info.
	for _, ai := range port.AddrInfoList {
		if ai.Geo == nilIPInfo {
			continue
		}
		geo := new(info.GeoLoc)
		geo.UnderlayIP = *proto.String(ai.Geo.IP)
		geo.Hostname = *proto.String(ai.Geo.Hostname)
		geo.City = *proto.String(ai.Geo.City)
		geo.Country = *proto.String(ai.Geo.Country)
		geo.Loc = *proto.String(ai.Geo.Loc)
		geo.Org = *proto.String(ai.Geo.Org)
		geo.Postal = *proto.String(ai.Geo.Postal)
		networkInfo.Location = geo
		break
	}
	// Any error or test result?
	networkInfo.NetworkErr = encodeTestResults(port.TestResults)

	networkInfo.Proxy = encodeProxyStatus(&port.ProxyConfig)
	return networkInfo
}

func encodeSystemAdapterInfo(ctx *zedagentContext) *info.SystemAdapterInfo {
	dpcl := ctx.devicePortConfigList
	sainfo := new(info.SystemAdapterInfo)
	sainfo.CurrentIndex = uint32(dpcl.CurrentIndex)
	sainfo.Status = make([]*info.DevicePortStatus, len(dpcl.PortConfigList))
	for i, dpc := range dpcl.PortConfigList {
		dps := new(info.DevicePortStatus)
		dps.Version = uint32(dpc.Version)
		dps.Key = dpc.Key
		ts, _ := ptypes.TimestampProto(dpc.TimePriority)
		dps.TimePriority = ts
		if !dpc.LastFailed.IsZero() {
			ts, _ := ptypes.TimestampProto(dpc.LastFailed)
			dps.LastFailed = ts
		}
		if !dpc.LastSucceeded.IsZero() {
			ts, _ := ptypes.TimestampProto(dpc.LastSucceeded)
			dps.LastSucceeded = ts
		}
		dps.LastError = dpc.LastError

		dps.Ports = make([]*info.DevicePort, len(dpc.Ports))
		for j, p := range dpc.Ports {
			dps.Ports[j] = encodeNetworkPortConfig(ctx, &p)
		}
		sainfo.Status[i] = dps
	}
	log.Debugf("encodeSystemAdapterInfo: %+v", sainfo)
	return sainfo
}

//getDataSecAtRestInfo prepares status related to Data security at Rest
func getDataSecAtRestInfo(ctx *zedagentContext) *info.DataSecAtRest {
	subVaultStatus := ctx.subVaultStatus
	ReportDataSecAtRestInfo := new(info.DataSecAtRest)
	ReportDataSecAtRestInfo.VaultList = make([]*info.VaultInfo, 0)
	vaultList := subVaultStatus.GetAll()
	for _, vaultItem := range vaultList {
		vault := vaultItem.(types.VaultStatus)
		vaultInfo := new(info.VaultInfo)
		vaultInfo.Name = vault.Name
		vaultInfo.Status = vault.Status
		if !vault.ErrorTime.IsZero() {
			vaultInfo.VaultErr = encodeErrorInfo(vault.ErrorAndTime)
		}
		ReportDataSecAtRestInfo.VaultList = append(ReportDataSecAtRestInfo.VaultList, vaultInfo)
	}
	return ReportDataSecAtRestInfo
}

func createConfigItemStatus(
	status types.GlobalStatus) *info.ZInfoConfigItemStatus {

	cfgItemsPtr := new(info.ZInfoConfigItemStatus)

	// Copy ConfigItems
	cfgItemsPtr.ConfigItems = make(map[string]*info.ZInfoConfigItem)
	for key, statusCfgItem := range status.ConfigItems {
		if statusCfgItem.Err != nil {
			cfgItemsPtr.ConfigItems[key] = &info.ZInfoConfigItem{
				Value: statusCfgItem.Value,
				Error: statusCfgItem.Err.Error()}
		} else {
			cfgItemsPtr.ConfigItems[key] = &info.ZInfoConfigItem{
				Value: statusCfgItem.Value}
		}
	}

	// Copy Unknown Config Items
	cfgItemsPtr.UnknownConfigItems = make(map[string]*info.ZInfoConfigItem)
	for key, statusUnknownCfgItem := range status.UnknownConfigItems {
		cfgItemsPtr.UnknownConfigItems[key] = &info.ZInfoConfigItem{
			Value: statusUnknownCfgItem.Value,
			Error: statusUnknownCfgItem.Err.Error()}
	}
	return cfgItemsPtr
}

func createAppInstances(ctxPtr *zedagentContext,
	zinfoDevice *info.ZInfoDevice) {

	addAppInstanceFunc := func(key string, value interface{}) bool {
		ais := value.(types.AppInstanceStatus)
		zinfoAppInst := new(info.ZInfoAppInstance)
		zinfoAppInst.Uuid = ais.UUIDandVersion.UUID.String()
		zinfoAppInst.Name = ais.DisplayName
		zinfoAppInst.DomainName = ais.DomainName
		zinfoDevice.AppInstances = append(zinfoDevice.AppInstances,
			zinfoAppInst)
		return true
	}
	ctxPtr.getconfigCtx.subAppInstanceStatus.Iterate(
		addAppInstanceFunc)
}
