// Copyright (c) 2017-2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package zedmanager

import (
	"fmt"
	"time"

	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/utils"
	"github.com/lf-edge/eve/pkg/pillar/uuidtonum"
	"github.com/satori/go.uuid"
	"github.com/shirou/gopsutil/disk"
	log "github.com/sirupsen/logrus"
)

// Find all the config which refer to this imageID.
func updateAIStatusWithStorageImageID(ctx *zedmanagerContext, imageID uuid.UUID) {

	log.Infof("updateAIStatusWithStorageImageID for %s", imageID)

	pub := ctx.pubAppInstanceStatus
	items := pub.GetAll()
	found := false
	for _, st := range items {
		status := st.(types.AppInstanceStatus)
		log.Debugf("updateAIStatusWithStorageImageID Processing "+
			"AppInstanceConfig for UUID %s",
			status.UUIDandVersion.UUID)
		for ssIndx := range status.StorageStatusList {
			ssPtr := &status.StorageStatusList[ssIndx]
			if uuid.Equal((*ssPtr).ImageID, imageID) {
				log.Infof("Found StorageStatus URL %s imageID %s",
					ssPtr.Name, imageID)
				updateAIStatusUUID(ctx, status.Key())
				found = true
			}
		}
	}
	if !found {
		log.Warnf("updateAIStatusWithStorageImageID for %s not found", imageID)
	}
}

// Update this AppInstanceStatus generate config updates to
// the microservices
func updateAIStatusUUID(ctx *zedmanagerContext, uuidStr string) {

	log.Infof("updateAIStatusUUID(%s)", uuidStr)
	status := lookupAppInstanceStatus(ctx, uuidStr)
	if status == nil {
		log.Infof("updateAIStatusUUID for %s: Missing AppInstanceStatus",
			uuidStr)
		return
	}
	config := lookupAppInstanceConfig(ctx, uuidStr)
	if config == nil || (status.PurgeInprogress == types.BringDown) {
		removeAIStatus(ctx, status)
		return
	}
	changed := doUpdate(ctx, *config, status)
	if changed {
		log.Infof("updateAIStatusUUID status change %d for %s",
			status.State, uuidStr)
		publishAppInstanceStatus(ctx, status)
	}
}

// Remove this AppInstanceStatus and generate config removes for
// the microservices
func removeAIStatusUUID(ctx *zedmanagerContext, uuidStr string) {

	log.Infof("removeAIStatusUUID(%s)", uuidStr)
	status := lookupAppInstanceStatus(ctx, uuidStr)
	if status == nil {
		log.Infof("removeAIStatusUUID for %s: Missing AppInstanceStatus",
			uuidStr)
		return
	}
	removeAIStatus(ctx, status)
}

func removeAIStatus(ctx *zedmanagerContext, status *types.AppInstanceStatus) {
	uuidStr := status.Key()
	uninstall := (status.PurgeInprogress != types.BringDown)
	changed, done := doRemove(ctx, status, uninstall)
	if changed {
		log.Infof("removeAIStatus status change for %s",
			uuidStr)
		publishAppInstanceStatus(ctx, status)
	}
	if !done {
		if uninstall {
			log.Infof("removeAIStatus(%s) waiting for removal",
				status.Key())
		} else {
			log.Infof("removeAIStatus(%s): PurgeInprogress waiting for removal",
				status.Key())
		}
		return
	}

	if uninstall {
		log.Infof("removeAIStatus(%s) remove done", uuidStr)
		// Write out what we modified to AppInstanceStatus aka delete
		unpublishAppInstanceStatus(ctx, status)
		return
	}
	log.Infof("removeAIStatus(%s): PurgeInprogress bringing it up",
		status.Key())
	status.PurgeInprogress = types.BringUp
	publishAppInstanceStatus(ctx, status)
	config := lookupAppInstanceConfig(ctx, uuidStr)
	if config != nil {
		changed := doUpdate(ctx, *config, status)
		if changed {
			publishAppInstanceStatus(ctx, status)
		}
	} else {
		log.Errorf("removeAIStatus(%s): PurgeInprogress no config!",
			status.Key())
	}
}

// Find all the Status which refer to this imageID
func removeAIStatusImageID(ctx *zedmanagerContext, imageID uuid.UUID) {

	log.Infof("removeAIStatusImageID for %s", imageID)
	pub := ctx.pubAppInstanceStatus
	items := pub.GetAll()
	found := false
	for _, st := range items {
		status := st.(types.AppInstanceStatus)
		log.Debugf("Processing AppInstanceStatus for UUID %s",
			status.UUIDandVersion.UUID)
		for _, ss := range status.StorageStatusList {
			if uuid.Equal(ss.ImageID, imageID) {
				log.Debugf("Found StorageStatus URL %s imageID %s",
					ss.Name, ss.ImageID)
				updateOrRemove(ctx, status)
				found = true
			}
		}
	}
	if !found {
		log.Warnf("removeAIStatusImageID for %s not found", imageID)
	}
}

// If we have an AIConfig we update it - the image might have disappeared.
// Otherwise we proceeed with remove.
func updateOrRemove(ctx *zedmanagerContext, status types.AppInstanceStatus) {
	uuidStr := status.Key()
	log.Infof("updateOrRemove(%s)", uuidStr)
	config := lookupAppInstanceConfig(ctx, uuidStr)
	if config == nil || (status.PurgeInprogress == types.BringDown) {
		log.Infof("updateOrRemove: remove for %s", uuidStr)
		removeAIStatus(ctx, &status)
	} else {
		log.Infof("updateOrRemove: update for %s", uuidStr)
		changed := doUpdate(ctx, *config, &status)
		if changed {
			log.Infof("updateOrRemove status change for %s",
				uuidStr)
			publishAppInstanceStatus(ctx, &status)
		}
	}
}

func dom0DiskReservedSize(ctxPtr *zedmanagerContext, deviceDiskSize uint64) uint64 {
	dom0MinDiskUsagePercent := ctxPtr.globalConfig.GlobalValueInt(
		types.Dom0MinDiskUsagePercent)
	diskReservedForDom0 := uint64(float64(deviceDiskSize) *
		(float64(dom0MinDiskUsagePercent) * 0.01))
	maxDom0DiskSize := uint64(ctxPtr.globalConfig.GlobalValueInt(
		types.Dom0DiskUsageMaxBytes))
	if diskReservedForDom0 > maxDom0DiskSize {
		log.Debugf("diskSizeReservedForDom0 - diskReservedForDom0 adjusted to "+
			"maxDom0DiskSize (%d)", maxDom0DiskSize)
		diskReservedForDom0 = maxDom0DiskSize
	}
	return diskReservedForDom0
}

// getRemainingAppDiskSpace returns how many bytes remain for app usage
// plus a string for printing the current app disk usage (latter used
// if there isn't enough)
func getRemainingAppDiskSpace(ctxPtr *zedmanagerContext) (uint64, string, error) {

	var totalAppDiskSize uint64
	appDiskSizeList := "" // In case caller wants to print an error

	pub := ctxPtr.pubAppInstanceStatus
	items := pub.GetAll()
	for _, iterStatusJSON := range items {
		iterStatus := iterStatusJSON.(types.AppInstanceStatus)
		if iterStatus.State < types.CREATED_VOLUME {
			// XXX look at MaxSizeBytes? Need to skip caller?
			log.Debugf("App %s State %d < CREATED_VOLUME",
				iterStatus.UUIDandVersion, iterStatus.State)
			continue
		}
		// XXX does this work for a container??
		appDiskSize, diskSizeList, err := utils.GetDiskSizeForAppInstance(iterStatus)
		if err != nil {
			log.Warnf("getRemainingAppDiskSpace: err: %s", err.Error())
		}
		totalAppDiskSize += appDiskSize
		appDiskSizeList += fmt.Sprintf("App: %s (Size: %d)\n%s\n",
			iterStatus.UUIDandVersion.UUID.String(), appDiskSize, diskSizeList)
	}
	deviceDiskUsage, err := disk.Usage(types.PersistDir)
	if err != nil {
		err := fmt.Errorf("Failed to get diskUsage for /persist. err: %s", err)
		log.Errorf("diskSizeReservedForDom0: err:%s", err)
		return 0, appDiskSizeList, err
	}
	deviceDiskSize := deviceDiskUsage.Total
	diskReservedForDom0 := dom0DiskReservedSize(ctxPtr, deviceDiskSize)
	var allowedDeviceDiskSizeForApps uint64
	if deviceDiskSize < diskReservedForDom0 {
		err = fmt.Errorf("Total Disk Size(%d) <=  diskReservedForDom0(%d)",
			deviceDiskSize, diskReservedForDom0)
		log.Errorf("***getRemainingAppDiskSpace: err: %s", err)
		return ^uint64(0), appDiskSizeList, err
	}
	allowedDeviceDiskSizeForApps = deviceDiskSize - diskReservedForDom0
	if allowedDeviceDiskSizeForApps < totalAppDiskSize {
		return 0, appDiskSizeList, nil
	} else {
		return allowedDeviceDiskSizeForApps - totalAppDiskSize, appDiskSizeList, nil
	}
}

func doUpdate(ctx *zedmanagerContext,
	config types.AppInstanceConfig,
	status *types.AppInstanceStatus) bool {

	uuidStr := status.Key()

	log.Infof("doUpdate: UUID:%s, Name", uuidStr)

	// The existence of Config is interpreted to mean the
	// AppInstance should be INSTALLED. Activate is checked separately.
	changed, done := doInstall(ctx, config, status)
	if !done {
		return changed
	}

	// Are we doing a purge?
	if status.PurgeInprogress == types.RecreateVolumes {
		log.Infof("PurgeInprogress(%s) volumemgr done",
			status.Key())
		status.PurgeInprogress = types.BringDown
		changed = true
		// Keep the old volumes in place
		_, done := doRemove(ctx, status, false)
		if !done {
			log.Infof("PurgeInprogress(%s) waiting for removal",
				status.Key())
			return changed
		}
		log.Infof("PurgeInprogress(%s) bringing it up",
			status.Key())
	}
	c, done := doPrepare(ctx, config, status)
	changed = changed || c
	if !done {
		return changed
	}

	if !config.Activate {
		if status.Activated || status.ActivateInprogress {
			c := doInactivateHalt(ctx, config, status)
			changed = changed || c
		} else {
			// If we have a !ReadOnly disk this will create a copy
			err := MaybeAddDomainConfig(ctx, config, *status, nil)
			if err != nil {
				log.Errorf("Error from MaybeAddDomainConfig for %s: %s",
					uuidStr, err)
				status.SetErrorWithSource(err.Error(),
					types.DomainStatus{}, time.Now())
				changed = true
			}
		}
		log.Infof("Waiting for config.Activate for %s", uuidStr)
		return changed
	}
	log.Infof("Have config.Activate for %s", uuidStr)
	c = doActivate(ctx, uuidStr, config, status)
	changed = changed || c
	log.Infof("doUpdate done for %s", uuidStr)
	return changed
}

func doInstall(ctx *zedmanagerContext,
	config types.AppInstanceConfig,
	status *types.AppInstanceStatus) (bool, bool) {

	uuidStr := status.Key()

	log.Infof("doInstall: UUID: %s", uuidStr)
	allErrors := ""
	var errorSource interface{}
	var errorTime time.Time
	changed := false

	if len(config.StorageConfigList) != len(status.StorageStatusList) {
		errString := fmt.Sprintf("Mismatch in storageConfig vs. Status length: %d vs %d",
			len(config.StorageConfigList),
			len(status.StorageStatusList))
		if status.PurgeInprogress == types.NotInprogress {
			log.Errorln(errString)
			status.SetError(errString, time.Now())
			return true, false
		}
		log.Warnln(errString)
	}

	// If we are purging and we failed to activate due some images
	// which are now removed from StorageConfigList we remove them
	if status.PurgeInprogress == types.RecreateVolumes && !status.Activated {
		removed := false
		newSs := []types.StorageStatus{}
		for i := range status.StorageStatusList {
			ss := &status.StorageStatusList[i]
			sc := lookupStorageConfig(&config, *ss)
			if sc != nil {
				newSs = append(newSs, *ss)
				continue
			}
			log.Infof("Removing potentially bad StorageStatus %v",
				ss)
			if status.IsErrorSource(ss.ErrorSourceType) {
				log.Infof("Removing error %s", status.Error)
				status.ClearErrorWithSource()
			}
			c := MaybeRemoveStorageStatus(ctx, config.UUIDandVersion.UUID, ss)
			if c {
				// Keep in StorageStatus until we get an update
				// from volumemgr
				newSs = append(newSs, *ss)
				removed = true
			}
			deleteAppAndImageHash(ctx, status.UUIDandVersion.UUID,
				ss.ImageID, ss.PurgeCounter)
		}
		log.Infof("purge inactive (%s) storageStatus from %d to %d",
			config.Key(), len(status.StorageStatusList), len(newSs))
		status.StorageStatusList = newSs
		if removed {
			log.Infof("Waiting for bad StorageStatus to go away for AppInst %s",
				status.Key())
			return removed, false
		}
	}

	// Any StorageStatus to add?
	for _, sc := range config.StorageConfigList {
		ss := lookupStorageStatus(status, sc)
		if ss != nil {
			continue
		}
		if status.PurgeInprogress == types.NotInprogress {
			errString := fmt.Sprintf("New storageConfig (Name: %s, "+
				"ImageSha256: %s, ImageID: %s, PurgeCounter: %d) found. New Storage configs are "+
				"not allowed unless purged",
				sc.Name, sc.ImageSha256, sc.ImageID, sc.PurgeCounter)
			log.Error(errString)
			status.SetError(errString, time.Now())
			return true, false
		}
		newSs := types.StorageStatus{}
		newSs.UpdateFromStorageConfig(sc)
		log.Infof("Adding new StorageStatus %v", newSs)
		status.StorageStatusList = append(status.StorageStatusList, newSs)
		changed = true
	}

	// Do we have some Volume work?
	if status.State < types.CREATED_VOLUME || status.PurgeInprogress != types.NotInprogress {
		for i := range status.StorageStatusList {
			ss := &status.StorageStatusList[i]
			c := doInstallStorageStatus(ctx, config, ss)
			if c {
				changed = true
			}
		}
	}
	// Determine minimum state and errors across all of StorageStatus
	minState := types.MAXSTATE
	for _, ss := range status.StorageStatusList {
		if ss.State < minState {
			minState = ss.State
		}
		if ss.HasError() {
			errorSource = ss.ErrorSourceType
			errorTime = ss.ErrorTime
			allErrors = appendError(allErrors, ss.Error)
		}
	}
	if minState == types.MAXSTATE {
		// No StorageStatus
		minState = types.INITIAL
	}
	if status.State >= types.BOOTING {
		// Leave unchanged
	} else {
		status.State = minState
		changed = true
	}

	if allErrors == "" {
		status.ClearErrorWithSource()
	} else if errorSource == nil {
		status.SetError(allErrors, errorTime)
	} else {
		status.SetErrorWithSource(allErrors, errorSource, errorTime)
	}
	if allErrors != "" {
		log.Errorf("Volumemgr error for %s: %s", uuidStr, allErrors)
		return changed, false
	}

	if minState < types.CREATED_VOLUME {
		log.Infof("Waiting for all volumes for %s", uuidStr)
		return changed, false
	}
	log.Infof("Done with volumes for %s", uuidStr)
	log.Infof("doInstall done for %s", uuidStr)
	return changed, true
}

// If StorageStatus was updated we return true
func doInstallStorageStatus(ctx *zedmanagerContext,
	config types.AppInstanceConfig, ss *types.StorageStatus) bool {

	changed := false
	log.Infof("StorageStatus Name: %s, imageID %s, ImageSha256: %s, purgeCounter %d",
		ss.Name, ss.ImageID, ss.ImageSha256, ss.PurgeCounter)

	if !ss.HasVolumemgrRef {
		if ss.IsContainer {
			maybeLatchImageSha(ctx, config, ss)
		}
		if ss.IsContainer && ss.ImageSha256 == "" {
			rs := lookupResolveStatus(ctx, ss.ResolveKey())
			if rs == nil {
				log.Infof("Resolve status not found for %s",
					ss.ImageID)
				ss.HasResolverRef = true
				MaybeAddResolveConfig(ctx, ss)
				ss.State = types.RESOLVING_TAG
				changed = true
				return changed
			}
			log.Infof("Processing ResolveStatus for storage status (%v) in app instance (%s)",
				ss.ImageID, config.Key())
			ss.State = types.RESOLVED_TAG
			changed = true
			if rs.HasError() {
				errStr := fmt.Sprintf("Received error from resolver for %s, SHA (%s): %s",
					ss.ResolveKey(), rs.ImageSha256, rs.Error)
				log.Error(errStr)
				ss.SetErrorWithSource(errStr, types.ResolveStatus{},
					rs.ErrorTime)
				changed = true
				return changed
			} else if rs.ImageSha256 == "" {
				errStr := fmt.Sprintf("Received empty SHA from resolver for %s, SHA (%s): %s",
					ss.ResolveKey(), rs.ImageSha256, rs.Error)
				log.Error(errStr)
				ss.SetErrorWithSource(errStr, types.ResolveStatus{},
					rs.ErrorTime)
				changed = true
				return changed
			} else if ss.IsErrorSource(types.ResolveStatus{}) {
				log.Infof("Clearing resolver error %s", ss.Error)
				ss.ClearErrorWithSource()
				changed = true
			}
			log.Infof("Added Image SHA (%s) for storage status (%s)",
				rs.ImageSha256, ss.ImageID)
			ss.ImageSha256 = rs.ImageSha256
			ss.HasResolverRef = false
			ss.Name = maybeInsertSha(ss.Name, ss.ImageSha256)
			addAppAndImageHash(ctx, config.UUIDandVersion.UUID,
				ss.ImageID, ss.ImageSha256, ss.PurgeCounter)
			maybeLatchImageSha(ctx, config, ss)
			deleteResolveConfig(ctx, rs.Key())
			changed = true
		}
		log.Infof("doInstall !HasVolumemgrRef for %s", ss.Name)
		// XXX note that we use the ImageID for the VolumeID
		// argument since we do not have a VolumeID
		AddOrRefcountVolumeConfig(ctx, ss.ImageSha256,
			config.UUIDandVersion.UUID, ss.ImageID,
			ss.PurgeCounter, *ss)
		ss.HasVolumemgrRef = true
		changed = true
	}
	vs := lookupVolumeStatus(ctx, ss.ImageSha256,
		config.UUIDandVersion.UUID, ss.ImageID, ss.PurgeCounter)
	if vs == nil || vs.RefCount == 0 {
		if vs == nil {
			log.Infof("VolumeStatus not found. name: %s",
				ss.Name)
		} else {
			log.Infof("VolumeStatus RefCount zero. name: %s",
				ss.Name)
		}
		// Downloader/verifier will fill in more specific state
		// once we get a VolumeStatus from the volumemgr.
		// To avoid jumping to CREATING_VOLUME to DOWNLOADING,
		// DOWNLOADED, VERIFYING, VERIFIED, and then back to
		// CREATING_VOLUME we don't set CREATING_VOLUME for all
		// types
		// XXX but we only do types.OriginTypeDownload from
		// zedmanager so no change to ss.State
		return changed
	}
	if vs.FileLocation != ss.ActiveFileLocation {
		ss.ActiveFileLocation = vs.FileLocation
		changed = true
		log.Infof("From VolumeStatus set ActiveFileLocation to %s for %s",
			vs.FileLocation, ss.Name)
	}
	if vs.State != ss.State {
		ss.State = vs.State
		changed = true
	}
	if vs.Progress != ss.Progress {
		ss.Progress = vs.Progress
		changed = true
	}
	if vs.Pending() {
		log.Infof("lookupVolumeStatus %s Pending", ss.Name)
		return changed
	}
	if vs.HasError() {
		log.Errorf("Received error from volumemgr for %s: %s",
			ss.Name, vs.Error)
		ss.SetErrorWithSource(vs.Error,
			types.VolumeStatus{}, vs.ErrorTime)
		changed = true
	} else if ss.IsErrorSource(types.VolumeStatus{}) {
		log.Infof("Clearing volumemgr error %s", ss.Error)
		ss.ClearErrorWithSource()
		changed = true
	}
	return changed
}

func doPrepare(ctx *zedmanagerContext,
	config types.AppInstanceConfig, status *types.AppInstanceStatus) (bool, bool) {

	uuidStr := status.Key()
	log.Infof("doPrepare for %s", uuidStr)
	changed := false

	if len(config.OverlayNetworkList) != len(status.EIDList) {
		errString := fmt.Sprintf("Mismatch in OLList config vs. status length: %d vs %d",
			len(config.OverlayNetworkList),
			len(status.EIDList))
		log.Error(errString)
		status.SetError(errString, time.Now())
		changed = true
		return changed, false
	}

	// Make sure we have an EIDConfig for each overlay
	for _, ec := range config.OverlayNetworkList {
		MaybeAddEIDConfig(ctx, config.UUIDandVersion,
			config.DisplayName, &ec)
	}
	// Check EIDStatus for each overlay; update AppInstanceStatus
	eidsAllocated := true
	for i, ec := range config.OverlayNetworkList {
		key := types.EidKey(config.UUIDandVersion, ec.IID)
		es := lookupEIDStatus(ctx, key)
		if es == nil || es.Pending() {
			log.Infof("lookupEIDStatus %s failed",
				key)
			eidsAllocated = false
			continue
		}
		status.EIDList[i] = es.EIDStatusDetails
		if status.EIDList[i].EID == nil {
			log.Infof("Missing EID for %s", key)
			eidsAllocated = false
		} else {
			log.Infof("Found EID %v for %s",
				status.EIDList[i].EID, key)
			changed = true
		}
	}
	if !eidsAllocated {
		log.Infof("Waiting for all EID allocations for %s", uuidStr)
		return changed, false
	}
	// Automatically move from VERIFIED to INSTALLED
	if status.State >= types.BOOTING {
		// Leave unchanged
	} else {
		status.State = types.INSTALLED
		changed = true
	}
	changed = true
	log.Infof("Done with EID allocations for %s", uuidStr)
	log.Infof("doPrepare done for %s", uuidStr)
	return changed, true
}

// Really a constant
var nilUUID uuid.UUID

// doActivate - Returns if the status has changed. Doesn't publish any changes.
// It is caller's responsibility to publish.
func doActivate(ctx *zedmanagerContext, uuidStr string,
	config types.AppInstanceConfig, status *types.AppInstanceStatus) bool {

	log.Infof("doActivate for %s", uuidStr)
	changed := false

	// Are we doing a restart and it came down?
	switch status.RestartInprogress {
	case types.BringDown:
		// If !status.Activated e.g, due to error, then
		// need to bring down first.
		ds := lookupDomainStatus(ctx, config.Key())
		if ds != nil {
			if status.DomainName != ds.DomainName {
				status.DomainName = ds.DomainName
				changed = true
			}
			if status.BootTime != ds.BootTime {
				log.Infof("Update boottime to %s for %s",
					ds.BootTime.Format(time.RFC3339Nano),
					status.Key())
				status.BootTime = ds.BootTime
				changed = true
			}
			c := updateVifUsed(status, *ds)
			if c {
				changed = true
			}
			if !ds.Activated && !ds.HasError() {
				log.Infof("RestartInprogress(%s) came down - set bring up",
					status.Key())
				status.RestartInprogress = types.BringUp
				changed = true
			}
		}
	}

	// Track that we have cleanup work in case something fails
	status.ActivateInprogress = true

	// Make sure we have an AppNetworkConfig
	MaybeAddAppNetworkConfig(ctx, config, status)

	// Check AppNetworkStatus
	ns := lookupAppNetworkStatus(ctx, uuidStr)
	if ns == nil {
		log.Infof("Waiting for AppNetworkStatus for %s", uuidStr)
		return changed
	}
	if ns.Pending() {
		log.Infof("Waiting for AppNetworkStatus !Pending for %s", uuidStr)
		return changed
	}
	if ns.HasError() {
		log.Errorf("Received error from zedrouter for %s: %s",
			uuidStr, ns.Error)
		status.SetErrorWithSource(ns.Error, types.AppNetworkStatus{},
			ns.ErrorTime)
		changed = true
		return changed
	}
	updateAppNetworkStatus(status, ns)
	if !ns.Activated {
		log.Infof("Waiting for AppNetworkStatus Activated for %s", uuidStr)
		return changed
	}
	if status.IsErrorSource(types.AppNetworkStatus{}) {
		log.Infof("Clearing zedrouter error %s", status.Error)
		status.ClearErrorWithSource()
		changed = true
	}
	log.Debugf("Done with AppNetworkStatus for %s", uuidStr)

	// Make sure we have a DomainConfig
	err := MaybeAddDomainConfig(ctx, config, *status, ns)
	if err != nil {
		log.Errorf("Error from MaybeAddDomainConfig for %s: %s",
			uuidStr, err)
		status.SetErrorWithSource(err.Error(), types.DomainStatus{},
			time.Now())
		changed = true
		log.Infof("Waiting for DomainStatus Activated for %s",
			uuidStr)
		return changed
	}

	// Check DomainStatus; update AppInstanceStatus if error
	ds := lookupDomainStatus(ctx, uuidStr)
	if ds == nil {
		log.Infof("Waiting for DomainStatus for %s", uuidStr)
		return changed
	}
	if status.DomainName != ds.DomainName {
		status.DomainName = ds.DomainName
		changed = true
	}
	if status.BootTime != ds.BootTime {
		log.Infof("Update boottime to %s for %s",
			ds.BootTime.Format(time.RFC3339Nano), status.Key())
		status.BootTime = ds.BootTime
		changed = true
	}
	c := updateVifUsed(status, *ds)
	if c {
		changed = true
	}
	// Are we doing a restart?
	if status.RestartInprogress == types.BringDown {
		dc := lookupDomainConfig(ctx, config.Key())
		if dc == nil {
			log.Errorf("RestartInprogress(%s) No DomainConfig",
				status.Key())
		} else if dc.Activate {
			log.Infof("RestartInprogress(%s) Clear Activate",
				status.Key())
			dc.Activate = false
			publishDomainConfig(ctx, dc)
		} else if !ds.Activated {
			log.Infof("RestartInprogress(%s) Set Activate",
				status.Key())
			status.RestartInprogress = types.BringUp
			changed = true
			dc.Activate = true
			publishDomainConfig(ctx, dc)
		} else {
			log.Infof("RestartInprogress(%s) waiting for domain down",
				status.Key())
		}
	}
	// Look for xen errors. Ignore if we are going down
	if status.RestartInprogress != types.BringDown {
		if ds.HasError() {
			log.Errorf("Received error from domainmgr for %s: %s",
				uuidStr, ds.Error)
			status.SetErrorWithSource(ds.Error, types.DomainStatus{},
				ds.ErrorTime)
			changed = true
		} else if status.IsErrorSource(types.DomainStatus{}) {
			log.Infof("Clearing domainmgr error %s", status.Error)
			status.ClearErrorWithSource()
			changed = true
		}
	} else {
		if ds.HasError() {
			log.Warnf("bringDown sees error from domainmgr for %s: %s",
				uuidStr, ds.Error)
		}
		if status.IsErrorSource(types.DomainStatus{}) {
			log.Infof("Clearing domainmgr error %s", status.Error)
			status.ClearErrorWithSource()
			changed = true
		}
	}
	if ds.State != status.State {
		switch status.State {
		case types.RESTARTING, types.PURGING:
			// Leave unchanged
		default:
			log.Infof("Set State from DomainStatus from %d to %d",
				status.State, ds.State)
			status.State = ds.State
			changed = true
		}
	}
	// XXX compare with equal before setting changed?
	status.IoAdapterList = ds.IoAdapterList
	changed = true
	if ds.State < types.BOOTING {
		log.Infof("Waiting for DomainStatus to BOOTING for %s",
			uuidStr)
		return changed
	}
	if ds.Pending() {
		log.Infof("Waiting for DomainStatus !Pending for %s", uuidStr)
		return changed
	}
	// Update Vdev from DiskStatus
	for _, disk := range ds.DiskStatusList {
		found := false
		for i := range status.StorageStatusList {
			ss := &status.StorageStatusList[i]
			if uuid.Equal(ss.ImageID, disk.ImageID) {
				found = true
				if ss.Vdev != disk.Vdev {
					log.Infof("Update SSL Vdev for %s: %s",
						uuidStr, disk.Vdev)
					ss.Vdev = disk.Vdev
					changed = true
				}
			}
		}
		if !found {
			log.Infof("No vdev for %s", uuidStr)
		}
	}
	log.Infof("Done with DomainStatus for %s", uuidStr)

	if !status.Activated {
		status.Activated = true
		status.ActivateInprogress = false
		changed = true
	}
	// Are we doing a restart?
	if status.RestartInprogress == types.BringUp {
		if ds.Activated {
			log.Infof("RestartInprogress(%s) activated",
				status.Key())
			status.RestartInprogress = types.NotInprogress
			status.State = types.RUNNING
			changed = true
		} else {
			log.Infof("RestartInprogress(%s) waiting for Activated",
				status.Key())
		}
	}
	if status.PurgeInprogress == types.BringUp {
		if ds.Activated {
			log.Infof("PurgeInprogress(%s) activated",
				status.Key())
			status.PurgeInprogress = types.NotInprogress
			status.State = types.RUNNING
			_ = purgeCmdDone(ctx, config, status)
			changed = true
		} else {
			log.Infof("PurgeInprogress(%s) waiting for Activated",
				status.Key())
		}
	}
	log.Infof("doActivate done for %s", uuidStr)
	return changed
}

// Check if VifUsed has changed and return true if it has
func updateVifUsed(statusPtr *types.AppInstanceStatus, ds types.DomainStatus) bool {
	changed := false
	for i := range statusPtr.UnderlayNetworks {
		ulStatus := &statusPtr.UnderlayNetworks[i]
		net := ds.VifInfoByVif(ulStatus.Vif)
		if net != nil && net.VifUsed != ulStatus.VifUsed {
			log.Infof("Found VifUsed %s for Vif %s", net.VifUsed, ulStatus.Vif)
			ulStatus.VifUsed = net.VifUsed
			changed = true
		}
	}
	for i := range statusPtr.OverlayNetworks {
		olStatus := &statusPtr.OverlayNetworks[i]
		net := ds.VifInfoByVif(olStatus.Vif)
		if net != nil && net.VifUsed != olStatus.VifUsed {
			log.Infof("Found VifUsed %s for Vif %s", net.VifUsed, olStatus.Vif)
			olStatus.VifUsed = net.VifUsed
			changed = true
		}
	}

	// for _, ec := range config.OverlayNetworkList {
	return changed
}

func lookupStorageStatus(status *types.AppInstanceStatus, sc types.StorageConfig) *types.StorageStatus {

	for i := range status.StorageStatusList {
		ss := &status.StorageStatusList[i]
		if ss.ImageID == sc.ImageID && ss.PurgeCounter == sc.PurgeCounter {
			log.Debugf("lookupStorageStatus found %s %s purgeCounter %d",
				ss.Name, ss.ImageID, ss.PurgeCounter)
			return ss
		}
	}
	return nil
}

func lookupStorageConfig(config *types.AppInstanceConfig, ss types.StorageStatus) *types.StorageConfig {

	for i := range config.StorageConfigList {
		sc := &config.StorageConfigList[i]
		if ss.ImageID == sc.ImageID && ss.PurgeCounter == sc.PurgeCounter {
			log.Debugf("lookupStorageConfig found SC %s %s purgeCounter %d",
				sc.Name, sc.ImageID, sc.PurgeCounter)
			return sc
		}
	}
	return nil
}

func purgeCmdDone(ctx *zedmanagerContext, config types.AppInstanceConfig,
	status *types.AppInstanceStatus) bool {

	log.Infof("purgeCmdDone(%s) for %s", config.Key(), config.DisplayName)

	changed := false
	// Process the StorageStatusList items which are not in StorageConfigList
	newSs := []types.StorageStatus{}
	for i := range status.StorageStatusList {
		ss := &status.StorageStatusList[i]
		sc := lookupStorageConfig(&config, *ss)
		if sc != nil {
			newSs = append(newSs, *ss)
			continue
		}
		log.Infof("purgeCmdDone(%s) unused SS %s %s purgeCounter %d",
			config.Key(), ss.Name, ss.ImageID, ss.PurgeCounter)
		c := MaybeRemoveStorageStatus(ctx, config.UUIDandVersion.UUID, ss)
		if c {
			changed = true
		}
		deleteAppAndImageHash(ctx, status.UUIDandVersion.UUID,
			ss.ImageID, ss.PurgeCounter)
	}
	log.Infof("purgeCmdDone(%s) storageStatus from %d to %d",
		config.Key(), len(status.StorageStatusList), len(newSs))
	status.StorageStatusList = newSs
	// Update persistent counter
	uuidtonum.UuidToNumAllocate(ctx.pubUuidToNum,
		status.UUIDandVersion.UUID,
		int(status.PurgeCmd.Counter),
		false, "purgeCmdCounter")
	return changed
}

// MaybeRemoveStorageStatus returns changed bool and updates StorageStatus
func MaybeRemoveStorageStatus(ctx *zedmanagerContext, AppInstID uuid.UUID,
	ss *types.StorageStatus) bool {

	changed := false
	log.Infof("MaybeRemoveStorageStatus: removing StorageStatus for %s",
		ss.Name)

	// Decrease refcount if we had increased it
	if ss.HasVolumemgrRef {
		MaybeRemoveVolumeConfig(ctx, ss.ImageSha256, AppInstID, ss.ImageID, ss.PurgeCounter)
		ss.HasVolumemgrRef = false
		changed = true
	}
	return changed
}

func doRemove(ctx *zedmanagerContext,
	status *types.AppInstanceStatus, uninstall bool) (bool, bool) {

	appInstID := status.UUIDandVersion.UUID
	uuidStr := appInstID.String()
	log.Infof("doRemove for %s uninstall %t", appInstID, uninstall)

	changed := false
	done := false
	c, done := doInactivate(ctx, appInstID, status)
	changed = changed || c
	if !done {
		log.Infof("doRemove waiting for inactivate for %s", uuidStr)
		return changed, done
	}
	if !status.Activated {
		c := doUnprepare(ctx, uuidStr, status)
		changed = changed || c
		if uninstall {
			c, d := doUninstall(ctx, appInstID, status)
			changed = changed || c
			done = done || d
		} else {
			done = true
		}
	}
	log.Infof("doRemove done for %s", uuidStr)
	return changed, done
}

func doInactivate(ctx *zedmanagerContext, appInstID uuid.UUID,
	status *types.AppInstanceStatus) (bool, bool) {

	uuidStr := appInstID.String()
	log.Infof("doInactivate for %s", uuidStr)
	changed := false
	done := false
	uninstall := (status.PurgeInprogress != types.BringDown)

	if uninstall {
		log.Infof("doInactivate uninstall for %s", uuidStr)
		// First halt the domain by deleting
		if lookupDomainConfig(ctx, uuidStr) != nil {
			unpublishDomainConfig(ctx, uuidStr)
		}

	} else {
		log.Infof("doInactivate NOT uninstall for %s", uuidStr)
		// First half the domain by clearing Activate
		dc := lookupDomainConfig(ctx, uuidStr)
		if dc == nil {
			log.Warnf("doInactivate: No DomainConfig for %s",
				uuidStr)
		} else if dc.Activate {
			log.Infof("doInactivate: Clearing Activate for DomainConfig for %s",
				uuidStr)
			dc.Activate = false
			publishDomainConfig(ctx, dc)
		}
	}
	// Check if DomainStatus !Activated; update AppInstanceStatus if error
	ds := lookupDomainStatus(ctx, uuidStr)
	if ds != nil && (uninstall || ds.Activated) {
		if uninstall {
			log.Infof("Waiting for DomainStatus removal for %s",
				uuidStr)
		} else {
			log.Infof("Waiting for DomainStatus !Activated for %s",
				uuidStr)
		}
		// Update state
		if status.DomainName != ds.DomainName {
			status.DomainName = ds.DomainName
			changed = true
		}
		if status.BootTime != ds.BootTime {
			log.Infof("Update boottime to %v for %s",
				ds.BootTime.Format(time.RFC3339Nano),
				status.Key())
			status.BootTime = ds.BootTime
			changed = true
		}
		c := updateVifUsed(status, *ds)
		if c {
			changed = true
		}
		// Look for errors
		if ds.HasError() {
			log.Errorf("Received error from domainmgr for %s: %s",
				uuidStr, ds.Error)
			status.SetErrorWithSource(ds.Error, types.DomainStatus{},
				ds.ErrorTime)
			changed = true
		} else if status.IsErrorSource(types.DomainStatus{}) {
			log.Infof("Clearing domainmgr error %s",
				status.Error)
			status.ClearErrorWithSource()
			changed = true
		}
		return changed, done
	}
	log.Infof("Done with DomainStatus removal/deactivate for %s", uuidStr)

	if uninstall {
		if lookupAppNetworkConfig(ctx, uuidStr) != nil {
			unpublishAppNetworkConfig(ctx, uuidStr)
		}
	} else {
		m := lookupAppNetworkConfig(ctx, status.Key())
		if m == nil {
			log.Warnf("doInactivate: No AppNetworkConfig for %s",
				uuidStr)
		} else if m.Activate {
			log.Infof("doInactivate: Clearing Activate for AppNetworkConfig for %s",
				uuidStr)
			m.Activate = false
			publishAppNetworkConfig(ctx, m)
		}
	}
	// Check if AppNetworkStatus gone or !Activated
	ns := lookupAppNetworkStatus(ctx, uuidStr)
	if ns != nil && (uninstall || ns.Activated) {
		if uninstall {
			log.Infof("Waiting for AppNetworkStatus removal for %s",
				uuidStr)
		} else {
			log.Infof("Waiting for AppNetworkStatus !Activated for %s",
				uuidStr)
		}
		if ns.HasError() {
			log.Errorf("Received error from zedrouter for %s: %s",
				uuidStr, ns.Error)
			status.SetErrorWithSource(ns.Error, types.AppNetworkStatus{},
				ns.ErrorTime)
			changed = true
		} else if status.IsErrorSource(types.AppNetworkStatus{}) {
			log.Infof("Clearing zedrouter error %s", status.Error)
			status.ClearErrorWithSource()
			changed = true
		}
		return changed, done
	}
	log.Infof("Done with AppNetworkStatus removal/deactivaye for %s", uuidStr)
	done = true
	status.Activated = false
	status.ActivateInprogress = false
	log.Infof("doInactivate done for %s", uuidStr)
	return changed, done
}

func doUnprepare(ctx *zedmanagerContext, uuidStr string,
	status *types.AppInstanceStatus) bool {

	log.Infof("doUnprepare for %s", uuidStr)
	changed := false

	// Remove the EIDConfig for each overlay
	for _, es := range status.EIDList {
		unpublishEIDConfig(ctx, status.UUIDandVersion, &es)
	}
	// Check EIDStatus for each overlay; update AppInstanceStatus
	eidsFreed := true
	for i, es := range status.EIDList {
		key := types.EidKey(status.UUIDandVersion, es.IID)
		es := lookupEIDStatus(ctx, key)
		if es != nil {
			log.Infof("lookupEIDStatus not gone on remove for %s",
				key)
			// Could it have changed?
			changed = true
			status.EIDList[i] = es.EIDStatusDetails
			eidsFreed = false
			continue
		}
		changed = true
	}
	if !eidsFreed {
		log.Infof("Waiting for all EID frees for %s", uuidStr)
		return changed
	}
	log.Debugf("Done with EID frees for %s", uuidStr)

	log.Infof("doUnprepare done for %s", uuidStr)
	return changed
}

func doUninstall(ctx *zedmanagerContext, appInstID uuid.UUID,
	status *types.AppInstanceStatus) (bool, bool) {

	log.Infof("doUninstall for %s", appInstID)
	changed := false
	del := false

	for i := range status.StorageStatusList {
		ss := &status.StorageStatusList[i]
		c := MaybeRemoveStorageStatus(ctx, appInstID, ss)
		if c {
			changed = true
		}
		deleteAppAndImageHash(ctx, status.UUIDandVersion.UUID,
			ss.ImageID, ss.PurgeCounter)
	}
	log.Debugf("Done with all volumemgr removes for %s",
		appInstID)

	del = true
	log.Infof("doUninstall done for %s", appInstID)
	return changed, del
}

// Handle Activate=false which is different than doInactivate
// Keep DomainConfig around so the vdisks stay around
// Keep AppInstanceConfig around and with Activate set.
func doInactivateHalt(ctx *zedmanagerContext,
	config types.AppInstanceConfig, status *types.AppInstanceStatus) bool {

	uuidStr := status.Key()
	log.Infof("doInactivateHalt for %s", uuidStr)
	changed := false

	// Check AppNetworkStatus
	ns := lookupAppNetworkStatus(ctx, uuidStr)
	if ns == nil {
		log.Infof("Waiting for AppNetworkStatus for %s", uuidStr)
		return changed
	}
	updateAppNetworkStatus(status, ns)
	if ns.Pending() {
		log.Infof("Waiting for AppNetworkStatus !Pending for %s", uuidStr)
		return changed
	}
	// XXX should we make it not Activated?
	if ns.HasError() {
		log.Errorf("Received error from zedrouter for %s: %s",
			uuidStr, ns.Error)
		status.SetErrorWithSource(ns.Error, types.AppNetworkStatus{},
			ns.ErrorTime)
		changed = true
		return changed
	} else if status.IsErrorSource(types.AppNetworkStatus{}) {
		log.Infof("Clearing zedrouter error %s", status.Error)
		status.ClearErrorWithSource()
		changed = true
	}
	log.Debugf("Done with AppNetworkStatus for %s", uuidStr)

	// Make sure we have a DomainConfig. Clears dc.Activate based
	// on the AppInstanceConfig's Activate
	err := MaybeAddDomainConfig(ctx, config, *status, ns)
	if err != nil {
		log.Errorf("Error from MaybeAddDomainConfig for %s: %s",
			uuidStr, err)
		status.SetError(err.Error(), time.Now())
		changed = true
		log.Infof("Waiting for DomainStatus Activated for %s",
			uuidStr)
		return changed
	}

	// Check DomainStatus; update AppInstanceStatus if error
	ds := lookupDomainStatus(ctx, uuidStr)
	if ds == nil {
		log.Infof("Waiting for DomainStatus for %s", uuidStr)
		return changed
	}
	if status.DomainName != ds.DomainName {
		status.DomainName = ds.DomainName
		changed = true
	}
	if status.BootTime != ds.BootTime {
		log.Infof("Update boottime to %v for %s",
			ds.BootTime.Format(time.RFC3339Nano), status.Key())
		status.BootTime = ds.BootTime
		changed = true
	}
	c := updateVifUsed(status, *ds)
	if c {
		changed = true
	}
	if ds.State != status.State {
		switch status.State {
		case types.RESTARTING, types.PURGING:
			// Leave unchanged
		default:
			log.Infof("Set State from DomainStatus from %d to %d",
				status.State, ds.State)
			status.State = ds.State
			changed = true
		}
	}
	// Ignore errors during a halt
	if ds.HasError() {
		log.Warnf("doInactivateHalt sees error from domainmgr for %s: %s",
			uuidStr, ds.Error)
	}
	if status.IsErrorSource(types.DomainStatus{}) {
		log.Infof("Clearing domainmgr error %s", status.Error)
		status.ClearErrorWithSource()
		changed = true
	}
	// XXX compare with equal before setting changed?
	status.IoAdapterList = ds.IoAdapterList
	changed = true
	if ds.Pending() {
		log.Infof("Waiting for DomainStatus !Pending for %s", uuidStr)
		return changed
	}
	if ds.Activated {
		log.Infof("Waiting for Not Activated for DomainStatus %s",
			uuidStr)
		return changed
	}
	// XXX network is still around! Need to call doInactivate in doRemove?
	// XXX fix assymetry?
	status.Activated = false
	status.ActivateInprogress = false
	changed = true
	log.Infof("doInactivateHalt done for %s", uuidStr)
	return changed
}

func appendError(allErrors string, lasterr string) string {
	return fmt.Sprintf("%s%s\n\n", allErrors, lasterr)
}
