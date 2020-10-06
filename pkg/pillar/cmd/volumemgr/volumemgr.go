// Copyright (c) 2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

// Process input in the form of a collection of VolumeConfig structs
// from zedmanager and baseosmgr. Publish the status as VolumeStatus

package volumemgr

import (
	"flag"
	"fmt"
	"os"
	"time"

	zconfig "github.com/lf-edge/eve/api/go/config"
	"github.com/lf-edge/eve/pkg/pillar/agentlog"
	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/cas"
	"github.com/lf-edge/eve/pkg/pillar/flextimer"
	"github.com/lf-edge/eve/pkg/pillar/pidfile"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/utils"
	"github.com/lf-edge/eve/pkg/pillar/worker"
	"github.com/sirupsen/logrus"
)

const (
	agentName              = "volumemgr"
	diskMetricsAgentName   = agentName + "metrics"
	diskMetricsWDTouchFile = types.WatchdogFileDir + "/" + diskMetricsAgentName + ".touch"
	runDirname             = "/var/run/" + agentName
	ciDirname              = runDirname + "/cloudinit"    // For cloud-init volumes XXX change?
	volumeEncryptedDirName = types.VolumeEncryptedDirName // We store encrypted VM and OCI volumes here
	volumeClearDirName     = types.VolumeClearDirName     // We store un-encrypted VM and OCI volumes here
	// Time limits for event loop handlers
	errorTime     = 3 * time.Minute
	warningTime   = 40 * time.Second
	casClientType = "containerd"
)

// Set from Makefile
var Version = "No version specified"

var volumeFormat = make(map[string]zconfig.Format)

type volumemgrContext struct {
	ps                         *pubsub.PubSub
	subBaseOsContentTreeConfig pubsub.Subscription
	pubBaseOsContentTreeStatus pubsub.Publication
	subGlobalConfig            pubsub.Subscription
	subZedAgentStatus          pubsub.Subscription

	pubDownloaderConfig  pubsub.Publication
	subDownloaderStatus  pubsub.Subscription
	pubVerifyImageConfig pubsub.Publication
	subVerifyImageStatus pubsub.Subscription

	subResolveStatus        pubsub.Subscription
	pubResolveConfig        pubsub.Publication
	subContentTreeConfig    pubsub.Subscription
	pubContentTreeStatus    pubsub.Publication
	subVolumeConfig         pubsub.Subscription
	pubVolumeStatus         pubsub.Publication
	subVolumeRefConfig      pubsub.Subscription
	pubVolumeRefStatus      pubsub.Publication
	pubContentTreeToHash    pubsub.Publication
	pubBlobStatus           pubsub.Publication
	pubDiskMetric           pubsub.Publication
	pubAppDiskMetric        pubsub.Publication
	subDatastoreConfig      pubsub.Subscription
	diskMetricsTickerHandle interface{}
	gc                      *time.Ticker

	worker *worker.Worker // For background work

	verifierRestarted    bool // Wait for verifier to restart
	contentTreeRestarted bool // Wait to receive all contentTree after restart
	usingConfig          bool // From zedagent
	gcRunning            bool
	initGced             bool // Will be marked true after initObjects are garbage collected

	globalConfig  *types.ConfigItemValueMap
	GCInitialized bool
	vdiskGCTime   uint32 // In seconds; XXX delete when OldVolumeStatus is deleted

	// Common CAS client which can be used by multiple routines.
	// There is no shared data so its safe to be used by multiple goroutines
	casClient cas.CAS
}

var debug = false
var debugOverride bool // From command line arg
var logger *logrus.Logger
var log *base.LogObject

// Run - the main function invoked by zedbox
func Run(ps *pubsub.PubSub, loggerArg *logrus.Logger, logArg *base.LogObject) int {
	logger = loggerArg
	log = logArg
	versionPtr := flag.Bool("v", false, "Version")
	debugPtr := flag.Bool("d", false, "Debug flag")
	flag.Parse()
	debug = *debugPtr
	debugOverride = debug
	if debugOverride {
		logger.SetLevel(logrus.TraceLevel)
	} else {
		logger.SetLevel(logrus.InfoLevel)
	}
	if *versionPtr {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return 0
	}
	if err := pidfile.CheckAndCreatePidfile(log, agentName); err != nil {
		log.Fatal(err)
	}
	// These settings can be overridden by GlobalConfig
	ctx := volumemgrContext{
		ps:           ps,
		vdiskGCTime:  3600,
		globalConfig: types.DefaultConfigItemValueMap(),
	}

	log.Infof("Starting %s", agentName)

	// Run a periodic timer so we always update StillRunning
	stillRunning := time.NewTicker(25 * time.Second)
	ps.StillRunning(agentName, warningTime, errorTime)

	// Look for global config such as log levels
	subGlobalConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "",
		MyAgentName:   agentName,
		TopicImpl:     types.ConfigItemValueMap{},
		Activate:      false,
		Ctx:           &ctx,
		CreateHandler: handleGlobalConfigModify,
		ModifyHandler: handleGlobalConfigModify,
		DeleteHandler: handleGlobalConfigDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subGlobalConfig = subGlobalConfig
	subGlobalConfig.Activate()

	// Create the background worker
	ctx.worker = InitHandleWork(&ctx)

	// Set up our publications before the subscriptions so ctx is set
	pubDownloaderConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.DownloaderConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubDownloaderConfig = pubDownloaderConfig

	pubVerifyImageConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.VerifyImageConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubVerifyImageConfig = pubVerifyImageConfig

	pubResolveConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.ResolveConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubResolveConfig = pubResolveConfig

	pubContentTreeStatus, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName:  agentName,
		AgentScope: types.AppImgObj,
		TopicType:  types.ContentTreeStatus{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubContentTreeStatus = pubContentTreeStatus

	pubVolumeStatus, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName:  agentName,
		AgentScope: types.AppImgObj,
		TopicType:  types.VolumeStatus{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubVolumeStatus = pubVolumeStatus

	pubVolumeRefStatus, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName:  agentName,
		AgentScope: types.AppImgObj,
		TopicType:  types.VolumeRefStatus{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubVolumeRefStatus = pubVolumeRefStatus

	pubContentTreeToHash, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName:  agentName,
		Persistent: true,
		TopicType:  types.AppAndImageToHash{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubContentTreeToHash = pubContentTreeToHash

	pubBaseOsContentTreeStatus, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName:  agentName,
		AgentScope: types.BaseOsObj,
		TopicType:  types.ContentTreeStatus{},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubBaseOsContentTreeStatus = pubBaseOsContentTreeStatus

	pubBlobStatus, err := ps.NewPublication(
		pubsub.PublicationOptions{
			AgentName: agentName,
			TopicType: types.BlobStatus{},
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubBlobStatus = pubBlobStatus

	pubDiskMetric, err := ps.NewPublication(
		pubsub.PublicationOptions{
			AgentName: agentName,
			TopicType: types.DiskMetric{},
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubDiskMetric = pubDiskMetric

	pubAppDiskMetric, err := ps.NewPublication(
		pubsub.PublicationOptions{
			AgentName: agentName,
			TopicType: types.AppDiskMetric{},
		},
	)
	if err != nil {
		log.Fatal(err)
	}
	ctx.pubAppDiskMetric = pubAppDiskMetric

	// Look for global config such as log levels
	subZedAgentStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "zedagent",
		MyAgentName:   agentName,
		TopicImpl:     types.ZedAgentStatus{},
		Activate:      false,
		Ctx:           &ctx,
		CreateHandler: handleZedAgentStatusModify,
		ModifyHandler: handleZedAgentStatusModify,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subZedAgentStatus = subZedAgentStatus
	subZedAgentStatus.Activate()

	// Look for DownloaderStatus from downloader
	subDownloaderStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "downloader",
		MyAgentName:   agentName,
		TopicImpl:     types.DownloaderStatus{},
		Activate:      false,
		Ctx:           &ctx,
		CreateHandler: handleDownloaderStatusModify,
		ModifyHandler: handleDownloaderStatusModify,
		DeleteHandler: handleDownloaderStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subDownloaderStatus = subDownloaderStatus
	subDownloaderStatus.Activate()

	// Look for VerifyImageStatus from verifier
	subVerifyImageStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:      "verifier",
		MyAgentName:    agentName,
		TopicImpl:      types.VerifyImageStatus{},
		Activate:       false,
		Ctx:            &ctx,
		CreateHandler:  handleVerifyImageStatusModify,
		ModifyHandler:  handleVerifyImageStatusModify,
		DeleteHandler:  handleVerifyImageStatusDelete,
		RestartHandler: handleVerifierRestarted,
		WarningTime:    warningTime,
		ErrorTime:      errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subVerifyImageStatus = subVerifyImageStatus
	subVerifyImageStatus.Activate()

	// Look for ResolveStatus from downloader
	subResolveStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "downloader",
		MyAgentName:   agentName,
		TopicImpl:     types.ResolveStatus{},
		Activate:      false,
		Ctx:           &ctx,
		CreateHandler: handleResolveStatusModify,
		ModifyHandler: handleResolveStatusModify,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subResolveStatus = subResolveStatus
	subResolveStatus.Activate()

	subContentTreeConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler:  handleContentTreeCreateAppImg,
		ModifyHandler:  handleContentTreeModifyAppImg,
		DeleteHandler:  handleContentTreeDeleteAppImg,
		RestartHandler: handleContentTreeRestart,
		WarningTime:    warningTime,
		ErrorTime:      errorTime,
		AgentName:      "zedagent",
		MyAgentName:    agentName,
		TopicImpl:      types.ContentTreeConfig{},
		Ctx:            &ctx,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subContentTreeConfig = subContentTreeConfig
	subContentTreeConfig.Activate()

	subVolumeConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler: handleVolumeCreate,
		ModifyHandler: handleVolumeModify,
		DeleteHandler: handleVolumeDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		AgentName:     "zedagent",
		MyAgentName:   agentName,
		TopicImpl:     types.VolumeConfig{},
		Ctx:           &ctx,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subVolumeConfig = subVolumeConfig
	subVolumeConfig.Activate()

	subVolumeRefConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler: handleVolumeRefCreate,
		ModifyHandler: handleVolumeRefModify,
		DeleteHandler: handleVolumeRefDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		AgentName:     "zedmanager",
		AgentScope:    types.AppImgObj,
		MyAgentName:   agentName,
		TopicImpl:     types.VolumeRefConfig{},
		Ctx:           &ctx,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subVolumeRefConfig = subVolumeRefConfig
	subVolumeRefConfig.Activate()

	subBaseOsContentTreeConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler: handleContentTreeCreateBaseOs,
		ModifyHandler: handleContentTreeModifyBaseOs,
		DeleteHandler: handleContentTreeDeleteBaseOs,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		AgentName:     "baseosmgr",
		MyAgentName:   agentName,
		TopicImpl:     types.ContentTreeConfig{},
		Ctx:           &ctx,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subBaseOsContentTreeConfig = subBaseOsContentTreeConfig
	subBaseOsContentTreeConfig.Activate()

	subDatastoreConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler: handleDatastoreConfigModify,
		ModifyHandler: handleDatastoreConfigModify,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		AgentName:     "zedagent",
		TopicImpl:     types.DatastoreConfig{},
		Ctx:           &ctx,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.subDatastoreConfig = subDatastoreConfig
	subDatastoreConfig.Activate()

	// Pick up debug aka log level before we start real work
	for !ctx.GCInitialized {
		log.Infof("waiting for GCInitialized")
		select {
		case change := <-subGlobalConfig.MsgChan():
			subGlobalConfig.ProcessChange(change)
		case <-stillRunning.C:
		}
		ps.StillRunning(agentName, warningTime, errorTime)
	}
	log.Infof("processed GlobalConfig")

	if err := utils.WaitForVault(ps, log, agentName, warningTime, errorTime); err != nil {
		log.Fatal(err)
	}
	log.Infof("processed Vault Status")

	// create the directories
	initializeDirs()

	// Iterate over volume directory and prepares map of
	// volume's content format with the volume key
	populateExistingVolumesFormat(volumeEncryptedDirName)
	populateExistingVolumesFormat(volumeClearDirName)

	if ctx.casClient, err = cas.NewCAS(casClientType); err != nil {
		err = fmt.Errorf("Run: exception while initializing CAS client: %s", err.Error())
		log.Fatal(err)
	}

	//casClient which is commonly used across volumemgr will be closed when volumemgr exits.
	defer ctx.casClient.CloseClient()

	populateInitBlobStatus(&ctx)

	// First we process the verifierStatus to avoid triggering a download
	// of an image we already have in place.
	// Also we wait for zedagent to send all contentTreeConfig so that we can GC all blobs which
	// doesn't have ConfigTree ref
	for !(ctx.verifierRestarted && ctx.contentTreeRestarted) {
		log.Warnf("Subject to watchdog. Waiting for verifierRestarted")

		select {
		case change := <-subGlobalConfig.MsgChan():
			subGlobalConfig.ProcessChange(change)

		case change := <-subVerifyImageStatus.MsgChan():
			subVerifyImageStatus.ProcessChange(change)

		case change := <-ctx.subContentTreeConfig.MsgChan():
			ctx.subContentTreeConfig.ProcessChange(change)

		case res := <-ctx.worker.MsgChan():
			HandleWorkResult(&ctx, ctx.worker.Process(res))

		case <-stillRunning.C:
		}
		ps.StillRunning(agentName, warningTime, errorTime)
	}
	log.Infof("Handling all inputs. Updating .touch file")

	// We will cleanup zero RefCount volumes which were present at boot
	// after a while.
	// We run timer 10 times more often than the limit on LastUse
	// We start the timer once ZedAgentStatus tells us we are receiving
	// config (or using a saved config) to avoid removing volumes when
	// they might become used.
	// XXX should we instead do this immediately when ZedAgentStatus provides
	// the update?
	duration := time.Duration(ctx.vdiskGCTime / 10)
	ctx.gc = time.NewTicker(duration * time.Second)
	ctx.gc.Stop()

	// start the metrics reporting task
	diskMetricsTickerHandle := make(chan interface{})
	log.Infof("Creating %s at %s", "diskMetricsTimerTask", agentlog.GetMyStack())

	//Add .touch file to watchdog config
	base.TouchFile(log, diskMetricsWDTouchFile)

	go diskMetricsTimerTask(&ctx, diskMetricsTickerHandle)
	ctx.diskMetricsTickerHandle = <-diskMetricsTickerHandle

	for {
		select {
		case change := <-ctx.subGlobalConfig.MsgChan():
			ctx.subGlobalConfig.ProcessChange(change)

		case change := <-subZedAgentStatus.MsgChan():
			subZedAgentStatus.ProcessChange(change)

		case change := <-subDownloaderStatus.MsgChan():
			subDownloaderStatus.ProcessChange(change)

		case change := <-subVerifyImageStatus.MsgChan():
			subVerifyImageStatus.ProcessChange(change)

		case change := <-subResolveStatus.MsgChan():
			ctx.subResolveStatus.ProcessChange(change)

		case change := <-ctx.subContentTreeConfig.MsgChan():
			ctx.subContentTreeConfig.ProcessChange(change)

		case change := <-ctx.subVolumeConfig.MsgChan():
			ctx.subVolumeConfig.ProcessChange(change)

		case change := <-ctx.subVolumeRefConfig.MsgChan():
			ctx.subVolumeRefConfig.ProcessChange(change)

		case change := <-ctx.subBaseOsContentTreeConfig.MsgChan():
			ctx.subBaseOsContentTreeConfig.ProcessChange(change)

		case change := <-ctx.subDatastoreConfig.MsgChan():
			ctx.subDatastoreConfig.ProcessChange(change)

		case <-ctx.gc.C:
			start := time.Now()
			gcObjects(&ctx, volumeEncryptedDirName)
			gcObjects(&ctx, volumeClearDirName)
			if !ctx.initGced {
				gcUnusedInitObjects(&ctx)
				ctx.initGced = true
			}
			ps.CheckMaxTimeTopic(agentName, "gc", start,
				warningTime, errorTime)

		case res := <-ctx.worker.MsgChan():
			HandleWorkResult(&ctx, ctx.worker.Process(res))

		case <-stillRunning.C:
		}
		ps.StillRunning(agentName, warningTime, errorTime)
	}
}

//gcUnusedInitObjects this method will garbage collect all unused resource during init
func gcUnusedInitObjects(ctx *volumemgrContext) {
	log.Infof("gcUnusedInitObjects")
	gcBlobStatus(ctx)
	gcVerifyImageConfig(ctx)
	gcImagesFromCAS(ctx)
}

func handleVerifierRestarted(ctxArg interface{}, done bool) {
	ctx := ctxArg.(*volumemgrContext)

	log.Infof("handleVerifierRestarted(%v)", done)
	if done {
		ctx.verifierRestarted = true
	}
}

func handleGlobalConfigModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*volumemgrContext)
	if key != "global" {
		log.Infof("handleGlobalConfigModify: ignoring %s", key)
		return
	}
	log.Infof("handleGlobalConfigModify for %s", key)
	var gcp *types.ConfigItemValueMap
	debug, gcp = agentlog.HandleGlobalConfig(log, ctx.subGlobalConfig, agentName,
		debugOverride, logger)
	if gcp != nil {
		maybeUpdateConfigItems(ctx, gcp)
		ctx.globalConfig = gcp
		ctx.GCInitialized = true
	}
	log.Infof("handleGlobalConfigModify done for %s", key)
}

func handleGlobalConfigDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*volumemgrContext)
	if key != "global" {
		log.Infof("handleGlobalConfigDelete: ignoring %s", key)
		return
	}
	log.Infof("handleGlobalConfigDelete for %s", key)
	debug, _ = agentlog.HandleGlobalConfig(log, ctx.subGlobalConfig, agentName,
		debugOverride, logger)
	*ctx.globalConfig = *types.DefaultConfigItemValueMap()
	log.Infof("handleGlobalConfigDelete done for %s", key)
}

func handleZedAgentStatusModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*volumemgrContext)
	status := statusArg.(types.ZedAgentStatus)
	switch status.ConfigGetStatus {
	case types.ConfigGetSuccess, types.ConfigGetReadSaved:
		ctx.usingConfig = true
		duration := time.Duration(ctx.vdiskGCTime / 10)
		ctx.gc = time.NewTicker(duration * time.Second)
	}
}

func maybeUpdateConfigItems(ctx *volumemgrContext, newConfigItemValueMap *types.ConfigItemValueMap) {
	log.Infof("maybeUpdateConfigItems")
	oldConfigItemValueMap := ctx.globalConfig

	if newConfigItemValueMap.GlobalValueInt(types.VdiskGCTime) != 0 &&
		newConfigItemValueMap.GlobalValueInt(types.VdiskGCTime) !=
			oldConfigItemValueMap.GlobalValueInt(types.VdiskGCTime) {
		log.Infof("maybeUpdateConfigItems: Updating vdiskGCTime from %d to %d",
			oldConfigItemValueMap.GlobalValueInt(types.VdiskGCTime),
			newConfigItemValueMap.GlobalValueInt(types.VdiskGCTime))
		ctx.vdiskGCTime = newConfigItemValueMap.GlobalValueInt(types.VdiskGCTime)
	}

	if newConfigItemValueMap.GlobalValueInt(types.DiskScanMetricInterval) != 0 &&
		newConfigItemValueMap.GlobalValueInt(types.DiskScanMetricInterval) !=
			oldConfigItemValueMap.GlobalValueInt(types.DiskScanMetricInterval) {
		log.Infof("maybeUpdateConfigItems: Updating DiskScanMetricInterval from %d to %d",
			oldConfigItemValueMap.GlobalValueInt(types.DiskScanMetricInterval),
			newConfigItemValueMap.GlobalValueInt(types.DiskScanMetricInterval))
		if ctx.diskMetricsTickerHandle == nil {
			log.Warnf("maybeUpdateConfigItems: no diskMetricsTickerHandle yet")
		} else {
			diskMetricInterval := time.Duration(newConfigItemValueMap.
				GlobalValueInt(types.DiskScanMetricInterval)) * time.Second
			max := float64(diskMetricInterval)
			min := max * 0.3
			flextimer.UpdateRangeTicker(ctx.diskMetricsTickerHandle,
				time.Duration(min), time.Duration(max))
			// Force an immediate timout since timer could have decreased
			flextimer.TickNow(ctx.diskMetricsTickerHandle)
		}
	}
}
