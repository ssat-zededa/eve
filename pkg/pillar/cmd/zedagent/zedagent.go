// Copyright (c) 2017-2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

// zedAgent interfaces with zedcloud for
//   * config sync
//   * metric/info publish
// app instance config is published to zedmanager for orchestration
// baseos/certs config is published to baseosmgr for orchestration
// datastore config is published for downloader consideration
// event based baseos/app instance/device info published to ZedCloud
// periodic status/metric published to zedCloud

// zedagent handles the following configuration
//   * app instance config/status  <zedagent>   / <appimg> / <config | status>
//   * base os config/status       <zedagent>   / <baseos> / <config | status>
//   * certs config/status         <zedagent>   / certs>   / <config | status>
// <base os>
//   <zedagent>  <baseos> <config> --> <baseosmgr>  <baseos> <status>
// <certs>
//   <zedagent>  <certs> <config> --> <baseosmgr>   <certs> <status>
// <app image>
//   <zedagent>  <appimage> <config> --> <zedmanager> <appimage> <status>
// <datastore>
//   <zedagent>  <datastore> <config> --> <downloader>

package zedagent

import (
	"container/list"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/lf-edge/eve/pkg/pillar/agentlog"
	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/pidfile"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/utils"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"

	"github.com/sirupsen/logrus"
)

const (
	agentName          = "zedagent"
	restartCounterFile = types.IdentityDirname + "/restartcounter"
	// checkpointDirname - location of config checkpoint
	checkpointDirname = types.PersistDir + "/checkpoint"
	// Time limits for event loop handlers
	errorTime   = 3 * time.Minute
	warningTime = 40 * time.Second
)

// Set from Makefile
var Version = "No version specified"

// XXX move to a context? Which? Used in handleconfig and handlemetrics!
var deviceNetworkStatus = &types.DeviceNetworkStatus{}

// XXX globals filled in by subscription handlers and read by handlemetrics
// XXX could alternatively access sub object when adding them.
var clientMetrics types.MetricsMap
var logmanagerMetrics types.MetricsMap
var downloaderMetrics types.MetricsMap
var networkMetrics types.NetworkMetrics
var cipherMetricsDL types.CipherMetricsMap
var cipherMetricsDM types.CipherMetricsMap
var cipherMetricsNim types.CipherMetricsMap

// Context for handleDNSModify
type DNSContext struct {
	DNSinitialized         bool // Received DeviceNetworkStatus
	subDeviceNetworkStatus pubsub.Subscription
	triggerGetConfig       bool
	triggerDeviceInfo      bool
}

type zedagentContext struct {
	ps                        *pubsub.PubSub
	getconfigCtx              *getconfigContext // Cross link
	cipherCtx                 *cipherContext    // Cross link
	attestCtx                 *attestContext    // Cross link
	assignableAdapters        *types.AssignableAdapters
	subAssignableAdapters     pubsub.Subscription
	iteration                 int
	subNetworkInstanceStatus  pubsub.Subscription
	subCertObjConfig          pubsub.Subscription
	TriggerDeviceInfo         chan<- struct{}
	zbootRestarted            bool // published by baseosmgr
	subBaseOsStatus           pubsub.Subscription
	subNetworkInstanceMetrics pubsub.Subscription
	subAppFlowMonitor         pubsub.Subscription
	subAppVifIPTrig           pubsub.Subscription
	pubGlobalConfig           pubsub.Publication
	subGlobalConfig           pubsub.Subscription
	subEdgeNodeCert           pubsub.Subscription
	subVaultStatus            pubsub.Subscription
	subAttestQuote            pubsub.Subscription
	subEncryptedKeyFromDevice pubsub.Subscription
	subLogMetrics             pubsub.Subscription
	subBlobStatus             pubsub.Subscription
	GCInitialized             bool // Received initial GlobalConfig
	subZbootStatus            pubsub.Subscription
	subAppContainerMetrics    pubsub.Subscription
	subDiskMetric             pubsub.Subscription
	subAppDiskMetric          pubsub.Subscription
	rebootCmd                 bool
	rebootCmdDeferred         bool
	deviceReboot              bool
	currentRebootReason       string           // Set by zedagent
	currentBootReason         types.BootReason // Set by zedagent
	rebootReason              string           // Previous reboot from nodeagent
	bootReason                types.BootReason // Previous reboot from nodeagent
	rebootStack               string           // Previous reboot from nodeagent
	rebootTime                time.Time        // Previous reboot from nodeagent
	// restartCounter - counts number of reboots of the device by Eve
	restartCounter uint32
	// rebootConfigCounter - reboot counter sent by the cloud in its config.
	//  This is the value of counter that triggered reboot. This is sent in
	//  device info msg. Can be used to verify device is caught up on all
	// outstanding reboot commands from cloud.
	rebootConfigCounter     uint32
	subDevicePortConfigList pubsub.Subscription
	remainingTestTime       time.Duration
	physicalIoAdapterMap    map[string]types.PhysicalIOAdapter
	globalConfig            types.ConfigItemValueMap
	specMap                 types.ConfigItemSpecMap
	globalStatus            types.GlobalStatus
	appContainerStatsTime   time.Time // last time the App Container stats uploaded
}

var debug = false
var debugOverride bool // From command line arg
var flowQ *list.List
var logger *logrus.Logger
var log *base.LogObject
var zedcloudCtx *zedcloud.ZedCloudContext

func Run(ps *pubsub.PubSub, loggerArg *logrus.Logger, logArg *base.LogObject) int {
	logger = loggerArg
	log = logArg
	var err error
	versionPtr := flag.Bool("v", false, "Version")
	debugPtr := flag.Bool("d", false, "Debug flag")
	parsePtr := flag.String("p", "", "parse checkpoint file")
	validatePtr := flag.Bool("V", false, "validate UTF-8 in checkpoint")
	fatalPtr := flag.Bool("F", false, "Cause log.Fatal fault injection")
	hangPtr := flag.Bool("H", false, "Cause watchdog .touch fault injection")
	flag.Parse()
	debug = *debugPtr
	debugOverride = debug
	fatalFlag := *fatalPtr
	hangFlag := *hangPtr
	if debugOverride {
		logger.SetLevel(logrus.TraceLevel)
	} else {
		logger.SetLevel(logrus.InfoLevel)
	}
	parse := *parsePtr
	validate := *validatePtr
	if *versionPtr {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return 0
	}
	if validate && parse == "" {
		fmt.Printf("Setting -V requires -p\n")
		return 1
	}
	if parse != "" {
		res, config := readValidateConfig(
			types.DefaultConfigItemValueMap().GlobalValueInt(types.StaleConfigTime), parse)
		if !res {
			fmt.Printf("Failed to parse %s\n", parse)
			return 1
		}
		fmt.Printf("parsed proto <%v>\n", config)
		if validate {
			valid := validateConfigUTF8(config)
			if !valid {
				fmt.Printf("Found some invalid UTF-8\n")
				return 1
			}
		}
		return 0
	}
	if err := pidfile.CheckAndCreatePidfile(log, agentName); err != nil {
		log.Fatal(err)
	}

	log.Functionf("Starting %s", agentName)

	triggerDeviceInfo := make(chan struct{}, 1)
	zedagentCtx := zedagentContext{
		ps:                ps,
		TriggerDeviceInfo: triggerDeviceInfo,
	}
	zedagentCtx.specMap = types.NewConfigItemSpecMap()
	zedagentCtx.globalConfig = *types.DefaultConfigItemValueMap()
	zedagentCtx.globalStatus.ConfigItems = make(
		map[string]types.ConfigItemStatus)
	zedagentCtx.globalStatus.UpdateItemValuesFromGlobalConfig(
		zedagentCtx.globalConfig)
	zedagentCtx.globalStatus.UnknownConfigItems = make(
		map[string]types.ConfigItemStatus)

	rebootConfig := readRebootConfig()
	if rebootConfig != nil {
		zedagentCtx.rebootConfigCounter = rebootConfig.Counter
		log.Functionf("Zedagent Run - rebootConfigCounter at init is %d",
			zedagentCtx.rebootConfigCounter)
	}

	zedagentCtx.physicalIoAdapterMap = make(map[string]types.PhysicalIOAdapter)

	zedagentCtx.pubGlobalConfig, err = ps.NewPublication(pubsub.PublicationOptions{
		AgentName:  agentName,
		TopicType:  types.ConfigItemValueMap{},
		Persistent: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	// upgradeconverter ensures we have a ConfigItemValueMap so we
	// read it to get the initial values
	item, err := zedagentCtx.pubGlobalConfig.Get("global")
	if err != nil {
		log.Fatalf("ConfigItemValueMap missing: %s", err)
	}
	zedagentCtx.globalConfig = item.(types.ConfigItemValueMap)
	log.Functionf("initialized GlobalConfig")

	// Run a periodic timer so we always update StillRunning
	stillRunning := time.NewTicker(25 * time.Second)
	ps.StillRunning(agentName, warningTime, errorTime)

	initializeDirs()

	// Context to pass around
	getconfigCtx := getconfigContext{}
	cipherCtx := cipherContext{}
	attestCtx := attestContext{}

	// Pick up (mostly static) AssignableAdapters before we report
	// any device info
	aa := types.AssignableAdapters{}
	zedagentCtx.assignableAdapters = &aa

	// Cross link
	getconfigCtx.zedagentCtx = &zedagentCtx
	zedagentCtx.getconfigCtx = &getconfigCtx

	cipherCtx.zedagentCtx = &zedagentCtx
	zedagentCtx.cipherCtx = &cipherCtx

	attestCtx.zedagentCtx = &zedagentCtx
	zedagentCtx.attestCtx = &attestCtx

	// Wait until we have been onboarded aka know our own UUID
	if err := utils.WaitForOnboarded(ps, log, agentName, warningTime, errorTime); err != nil {
		log.Fatal(err)
	}
	log.Functionf("processed onboarded")

	// We know our own UUID; prepare for communication with controller
	zedcloudCtx = handleConfigInit(zedagentCtx.globalConfig.GlobalValueInt(types.NetworkSendTimeout))

	// Timer for deferred sends of info messages
	deferredChan := zedcloud.GetDeferredChan(zedcloudCtx)

	subAssignableAdapters, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "domainmgr",
		MyAgentName:   agentName,
		TopicImpl:     types.AssignableAdapters{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleAACreate,
		ModifyHandler: handleAAModify,
		DeleteHandler: handleAADelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subAssignableAdapters = subAssignableAdapters
	subAssignableAdapters.Activate()

	pubPhysicalIOAdapters, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.PhysicalIOAdapterList{},
	})
	if err != nil {
		log.Fatal(err)
	}
	pubPhysicalIOAdapters.ClearRestarted()
	getconfigCtx.pubPhysicalIOAdapters = pubPhysicalIOAdapters

	pubDevicePortConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.DevicePortConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.pubDevicePortConfig = pubDevicePortConfig

	// Publish NetworkXObjectConfig and for outselves. XXX remove
	pubNetworkXObjectConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.NetworkXObjectConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.pubNetworkXObjectConfig = pubNetworkXObjectConfig

	pubNetworkInstanceConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.NetworkInstanceConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.pubNetworkInstanceConfig = pubNetworkInstanceConfig

	pubAppInstanceConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.AppInstanceConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.pubAppInstanceConfig = pubAppInstanceConfig
	pubAppInstanceConfig.ClearRestarted()

	pubAppNetworkConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.AppNetworkConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	pubAppNetworkConfig.ClearRestarted()
	getconfigCtx.pubAppNetworkConfig = pubAppNetworkConfig

	// XXX defer this until we have some config from cloud or saved copy
	pubAppInstanceConfig.SignalRestarted()

	pubBaseOsConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.BaseOsConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	pubBaseOsConfig.ClearRestarted()
	getconfigCtx.pubBaseOsConfig = pubBaseOsConfig

	pubZedAgentStatus, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.ZedAgentStatus{},
	})
	if err != nil {
		log.Fatal(err)
	}
	pubZedAgentStatus.ClearRestarted()
	getconfigCtx.pubZedAgentStatus = pubZedAgentStatus
	pubDatastoreConfig, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.DatastoreConfig{},
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.pubDatastoreConfig = pubDatastoreConfig
	pubDatastoreConfig.ClearRestarted()

	pubControllerCert, err := ps.NewPublication(
		pubsub.PublicationOptions{
			AgentName:  agentName,
			Persistent: true,
			TopicType:  types.ControllerCert{},
		})
	if err != nil {
		log.Fatal(err)
	}
	pubControllerCert.ClearRestarted()
	getconfigCtx.pubControllerCert = pubControllerCert

	// for CipherContextStatus Publisher
	pubCipherContext, err := ps.NewPublication(
		pubsub.PublicationOptions{
			AgentName:  agentName,
			Persistent: true,
			TopicType:  types.CipherContext{},
		})
	if err != nil {
		log.Fatal(err)
	}
	pubCipherContext.ClearRestarted()
	getconfigCtx.pubCipherContext = pubCipherContext

	// for ContentTree config Publisher
	pubContentTreeConfig, err := ps.NewPublication(
		pubsub.PublicationOptions{
			AgentName: agentName,
			TopicType: types.ContentTreeConfig{},
		})
	if err != nil {
		log.Fatal(err)
	}
	pubContentTreeConfig.ClearRestarted()
	getconfigCtx.pubContentTreeConfig = pubContentTreeConfig

	// for volume config Publisher
	pubVolumeConfig, err := ps.NewPublication(
		pubsub.PublicationOptions{
			AgentName: agentName,
			TopicType: types.VolumeConfig{},
		})
	if err != nil {
		log.Fatal(err)
	}
	pubVolumeConfig.ClearRestarted()
	getconfigCtx.pubVolumeConfig = pubVolumeConfig

	// Look for global config such as log levels
	subGlobalConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     agentName,
		MyAgentName:   agentName,
		TopicImpl:     types.ConfigItemValueMap{},
		Persistent:    true,
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleGlobalConfigCreate,
		ModifyHandler: handleGlobalConfigModify,
		DeleteHandler: handleGlobalConfigDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subGlobalConfig = subGlobalConfig
	subGlobalConfig.Activate()

	subNetworkInstanceStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "zedrouter",
		MyAgentName:   agentName,
		TopicImpl:     types.NetworkInstanceStatus{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleNetworkInstanceCreate,
		ModifyHandler: handleNetworkInstanceModify,
		DeleteHandler: handleNetworkInstanceDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subNetworkInstanceStatus = subNetworkInstanceStatus
	subNetworkInstanceStatus.Activate()

	subNetworkInstanceMetrics, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "zedrouter",
		MyAgentName: agentName,
		TopicImpl:   types.NetworkInstanceMetrics{},
		Activate:    false,
		Ctx:         &zedagentCtx,
		WarningTime: warningTime,
		ErrorTime:   errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subNetworkInstanceMetrics = subNetworkInstanceMetrics
	subNetworkInstanceMetrics.Activate()

	subAppFlowMonitor, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "zedrouter",
		MyAgentName:   agentName,
		TopicImpl:     types.IPFlow{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleAppFlowMonitorCreate,
		ModifyHandler: handleAppFlowMonitorModify,
		DeleteHandler: handleAppFlowMonitorDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	subAppFlowMonitor.Activate()
	flowQ = list.New()
	log.Functionf("FlowStats: create subFlowStatus")

	subAppVifIPTrig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "zedrouter",
		MyAgentName:   agentName,
		TopicImpl:     types.VifIPTrig{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleAppVifIPTrigCreate,
		ModifyHandler: handleAppVifIPTrigModify,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	subAppVifIPTrig.Activate()

	// Look for AppInstanceStatus from zedmanager
	subAppInstanceStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "zedmanager",
		MyAgentName:   agentName,
		TopicImpl:     types.AppInstanceStatus{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleAppInstanceStatusCreate,
		ModifyHandler: handleAppInstanceStatusModify,
		DeleteHandler: handleAppInstanceStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.subAppInstanceStatus = subAppInstanceStatus
	subAppInstanceStatus.Activate()

	// Look for ContentTreeStatus from volumemgr
	subContentTreeStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "volumemgr",
		MyAgentName:   agentName,
		AgentScope:    types.AppImgObj,
		TopicImpl:     types.ContentTreeStatus{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleContentTreeStatusCreate,
		ModifyHandler: handleContentTreeStatusModify,
		DeleteHandler: handleContentTreeStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.subContentTreeStatus = subContentTreeStatus
	subContentTreeStatus.Activate()

	// Look for VolumeStatus from volumemgr
	subVolumeStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "volumemgr",
		MyAgentName:   agentName,
		AgentScope:    types.AppImgObj,
		TopicImpl:     types.VolumeStatus{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleVolumeStatusCreate,
		ModifyHandler: handleVolumeStatusModify,
		DeleteHandler: handleVolumeStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.subVolumeStatus = subVolumeStatus
	subVolumeStatus.Activate()

	// Look for DomainMetric from domainmgr
	subDomainMetric, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "domainmgr",
		MyAgentName: agentName,
		TopicImpl:   types.DomainMetric{},
		Activate:    true,
		Ctx:         &zedagentCtx,
		WarningTime: warningTime,
		ErrorTime:   errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.subDomainMetric = subDomainMetric

	// Look for ProcessMetric from domainmgr
	subProcessMetric, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "domainmgr",
		MyAgentName: agentName,
		TopicImpl:   types.ProcessMetric{},
		Activate:    true,
		Ctx:         &zedagentCtx,
		WarningTime: warningTime,
		ErrorTime:   errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.subProcessMetric = subProcessMetric

	subHostMemory, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "domainmgr",
		MyAgentName: agentName,
		TopicImpl:   types.HostMemory{},
		Activate:    true,
		Ctx:         &zedagentCtx,
		WarningTime: warningTime,
		ErrorTime:   errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.subHostMemory = subHostMemory

	// Look for zboot status
	subZbootStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:      "baseosmgr",
		MyAgentName:    agentName,
		TopicImpl:      types.ZbootStatus{},
		Activate:       false,
		Ctx:            &zedagentCtx,
		CreateHandler:  handleZbootStatusCreate,
		ModifyHandler:  handleZbootStatusModify,
		DeleteHandler:  handleZbootStatusDelete,
		RestartHandler: handleZbootRestarted,
		WarningTime:    warningTime,
		ErrorTime:      errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subZbootStatus = subZbootStatus
	subZbootStatus.Activate()

	// sub AppContainerMetrics from zedrouter
	subAppContainerMetrics, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "zedrouter",
		MyAgentName:   agentName,
		TopicImpl:     types.AppContainerMetrics{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleAppContainerMetricsCreate,
		ModifyHandler: handleAppContainerMetricsModify,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subAppContainerMetrics = subAppContainerMetrics
	subAppContainerMetrics.Activate()

	subBaseOsStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "baseosmgr",
		MyAgentName:   agentName,
		TopicImpl:     types.BaseOsStatus{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleBaseOsStatusCreate,
		ModifyHandler: handleBaseOsStatusModify,
		DeleteHandler: handleBaseOsStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subBaseOsStatus = subBaseOsStatus
	subBaseOsStatus.Activate()

	subEdgeNodeCert, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "tpmmgr",
		MyAgentName:   agentName,
		TopicImpl:     types.EdgeNodeCert{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleEdgeNodeCertCreate,
		ModifyHandler: handleEdgeNodeCertModify,
		DeleteHandler: handleEdgeNodeCertDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subEdgeNodeCert = subEdgeNodeCert
	subEdgeNodeCert.Activate()

	subVaultStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "vaultmgr",
		MyAgentName:   agentName,
		TopicImpl:     types.VaultStatus{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleVaultStatusCreate,
		ModifyHandler: handleVaultStatusModify,
		DeleteHandler: handleVaultStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subVaultStatus = subVaultStatus
	subVaultStatus.Activate()

	subAttestQuote, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "tpmmgr",
		MyAgentName:   agentName,
		TopicImpl:     types.AttestQuote{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleAttestQuoteCreate,
		ModifyHandler: handleAttestQuoteModify,
		DeleteHandler: handleAttestQuoteDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subAttestQuote = subAttestQuote
	subAttestQuote.Activate()

	subEncryptedKeyFromDevice, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "vaultmgr",
		MyAgentName:   agentName,
		TopicImpl:     types.EncryptedVaultKeyFromDevice{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleEncryptedKeyFromDeviceCreate,
		ModifyHandler: handleEncryptedKeyFromDeviceModify,
		DeleteHandler: handleEncryptedKeyFromDeviceDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subEncryptedKeyFromDevice = subEncryptedKeyFromDevice
	subEncryptedKeyFromDevice.Activate()

	// Look for nodeagent status
	subNodeAgentStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "nodeagent",
		MyAgentName:   agentName,
		TopicImpl:     types.NodeAgentStatus{},
		Activate:      false,
		Ctx:           &getconfigCtx,
		CreateHandler: handleNodeAgentStatusCreate,
		ModifyHandler: handleNodeAgentStatusModify,
		DeleteHandler: handleNodeAgentStatusDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	getconfigCtx.subNodeAgentStatus = subNodeAgentStatus
	subNodeAgentStatus.Activate()

	DNSctx := DNSContext{}
	subDeviceNetworkStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "nim",
		MyAgentName:   agentName,
		TopicImpl:     types.DeviceNetworkStatus{},
		Activate:      false,
		Ctx:           &DNSctx,
		CreateHandler: handleDNSCreate,
		ModifyHandler: handleDNSModify,
		DeleteHandler: handleDNSDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	DNSctx.subDeviceNetworkStatus = subDeviceNetworkStatus
	subDeviceNetworkStatus.Activate()

	subDevicePortConfigList, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "nim",
		MyAgentName:   agentName,
		TopicImpl:     types.DevicePortConfigList{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleDPCLCreate,
		ModifyHandler: handleDPCLModify,
		DeleteHandler: handleDPCLDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subDevicePortConfigList = subDevicePortConfigList
	subDevicePortConfigList.Activate()

	subBlobStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "volumemgr",
		MyAgentName:   agentName,
		TopicImpl:     types.BlobStatus{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleBlobStatusCreate,
		ModifyHandler: handleBlobStatusModify,
		DeleteHandler: handleBlobDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subBlobStatus = subBlobStatus
	subBlobStatus.Activate()

	// Subscribe to Log metrics from logmanager
	subLogMetrics, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "logmanager",
		MyAgentName: agentName,
		TopicImpl:   types.LogMetrics{},
		Activate:    false,
		Ctx:         &zedagentCtx,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subLogMetrics = subLogMetrics
	subLogMetrics.Activate()

	subDiskMetric, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "volumemgr",
		MyAgentName:   agentName,
		TopicImpl:     types.DiskMetric{},
		Activate:      false,
		Ctx:           &zedagentCtx,
		CreateHandler: handleDiskMetricCreate,
		ModifyHandler: handleDiskMetricModify,
		DeleteHandler: handleDiskMetricDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subDiskMetric = subDiskMetric
	subDiskMetric.Activate()

	subAppDiskMetric, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "volumemgr",
		MyAgentName: agentName,
		TopicImpl:   types.AppDiskMetric{},
		Activate:    false,
		Ctx:         &zedagentCtx,
		WarningTime: warningTime,
		ErrorTime:   errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	zedagentCtx.subAppDiskMetric = subAppDiskMetric
	subAppDiskMetric.Activate()

	//initialize cipher processing block
	cipherModuleInitialize(&zedagentCtx, ps)

	//initialize remote attestation context
	attestModuleInitialize(&zedagentCtx, ps)

	// Pick up debug aka log level before we start real work
	// Note that we use these handlers to process updates from
	// the controller since the parser (in zedagent aka ourselves)
	// merely publishes the GlobalConfig
	for !zedagentCtx.GCInitialized {
		log.Functionf("Waiting for GCInitialized")
		select {
		case change := <-subGlobalConfig.MsgChan():
			subGlobalConfig.ProcessChange(change)

		case change := <-getconfigCtx.subNodeAgentStatus.MsgChan():
			getconfigCtx.subNodeAgentStatus.ProcessChange(change)
		}
	}
	log.Functionf("processed GlobalConfig")

	// wait till, zboot status is ready
	for !zedagentCtx.zbootRestarted {
		select {
		case change := <-subZbootStatus.MsgChan():
			subZbootStatus.ProcessChange(change)
			if zedagentCtx.zbootRestarted {
				log.Functionf("Zboot reported restarted")
			}

		case change := <-getconfigCtx.subNodeAgentStatus.MsgChan():
			getconfigCtx.subNodeAgentStatus.ProcessChange(change)

		case <-stillRunning.C:
			// Fault injection
			if fatalFlag {
				log.Fatal("Requested fault injection to cause watchdog")
			}
		}
		if hangFlag {
			log.Functionf("Requested to not touch to cause watchdog")
		} else {
			ps.StillRunning(agentName, warningTime, errorTime)
		}
	}

	log.Functionf("Waiting until we have some uplinks with usable addresses")
	for !DNSctx.DNSinitialized {
		log.Functionf("Waiting for DeviceNetworkStatus %v",
			DNSctx.DNSinitialized)

		select {
		case change := <-subGlobalConfig.MsgChan():
			subGlobalConfig.ProcessChange(change)

		case change := <-subDeviceNetworkStatus.MsgChan():
			subDeviceNetworkStatus.ProcessChange(change)

		case change := <-subAssignableAdapters.MsgChan():
			subAssignableAdapters.ProcessChange(change)

		case change := <-subDevicePortConfigList.MsgChan():
			subDevicePortConfigList.ProcessChange(change)

		case change := <-getconfigCtx.subNodeAgentStatus.MsgChan():
			subNodeAgentStatus.ProcessChange(change)

		case change := <-subVaultStatus.MsgChan():
			subVaultStatus.ProcessChange(change)

		case change := <-subAttestQuote.MsgChan():
			subAttestQuote.ProcessChange(change)

		case change := <-subEncryptedKeyFromDevice.MsgChan():
			subEncryptedKeyFromDevice.ProcessChange(change)

		case change := <-deferredChan:
			start := time.Now()
			zedcloud.HandleDeferred(zedcloudCtx, change, 100*time.Millisecond)
			ps.CheckMaxTimeTopic(agentName, "deferredChan", start,
				warningTime, errorTime)

		case <-stillRunning.C:
			// Fault injection
			if fatalFlag {
				log.Fatal("Requested fault injection to cause watchdog")
			}
		}
		if hangFlag {
			log.Functionf("Requested to not touch to cause watchdog")
		} else {
			ps.StillRunning(agentName, warningTime, errorTime)
		}
	}

	// Subscribe to network metrics from zedrouter
	subNetworkMetrics, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "zedrouter",
		MyAgentName: agentName,
		TopicImpl:   types.NetworkMetrics{},
		Activate:    true,
		Ctx:         &zedagentCtx,
	})
	if err != nil {
		log.Fatal(err)
	}
	// Subscribe to cloud metrics from different agents
	cms := zedcloud.GetCloudMetrics(log)
	subClientMetrics, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "zedclient",
		MyAgentName: agentName,
		TopicImpl:   cms,
		Activate:    true,
		Ctx:         &zedagentCtx,
	})
	if err != nil {
		log.Fatal(err)
	}
	subLogmanagerMetrics, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "logmanager",
		MyAgentName: agentName,
		TopicImpl:   cms,
		Activate:    true,
		Ctx:         &zedagentCtx,
	})
	if err != nil {
		log.Fatal(err)
	}
	subDownloaderMetrics, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "downloader",
		MyAgentName: agentName,
		TopicImpl:   cms,
		Activate:    true,
		Ctx:         &zedagentCtx,
	})
	if err != nil {
		log.Fatal(err)
	}

	subCipherMetricsDL, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "downloader",
		MyAgentName: agentName,
		TopicImpl:   types.CipherMetricsMap{},
		Activate:    true,
		Ctx:         &zedagentCtx,
	})
	if err != nil {
		log.Fatal(err)
	}
	subCipherMetricsDM, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "domainmgr",
		MyAgentName: agentName,
		TopicImpl:   types.CipherMetricsMap{},
		Activate:    true,
		Ctx:         &zedagentCtx,
	})
	if err != nil {
		log.Fatal(err)
	}
	subCipherMetricsNim, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "nim",
		MyAgentName: agentName,
		TopicImpl:   types.CipherMetricsMap{},
		Activate:    true,
		Ctx:         &zedagentCtx,
	})
	if err != nil {
		log.Fatal(err)
	}

	// Use a go routine to make sure we have wait/timeout without
	// blocking the main select loop
	log.Functionf("Creating %s at %s", "deviceInfoTask", agentlog.GetMyStack())
	go deviceInfoTask(&zedagentCtx, triggerDeviceInfo)

	// Publish initial device info.
	triggerPublishDevInfo(&zedagentCtx)

	// start the metrics reporting task
	handleChannel := make(chan interface{})
	log.Functionf("Creating %s at %s", "metricsTimerTask", agentlog.GetMyStack())
	go metricsTimerTask(&zedagentCtx, handleChannel)
	metricsTickerHandle := <-handleChannel
	getconfigCtx.metricsTickerHandle = metricsTickerHandle

	// start the config fetch tasks, when zboot status is ready
	log.Functionf("Creating %s at %s", "configTimerTask", agentlog.GetMyStack())
	go configTimerTask(handleChannel, &getconfigCtx)
	configTickerHandle := <-handleChannel
	// XXX close handleChannels?
	getconfigCtx.configTickerHandle = configTickerHandle

	// start cipher module tasks
	cipherModuleStart(&zedagentCtx)

	// start remote attestation task
	attestModuleStart(&zedagentCtx)

	for {
		select {
		case change := <-subZbootStatus.MsgChan():
			subZbootStatus.ProcessChange(change)

		case change := <-subGlobalConfig.MsgChan():
			subGlobalConfig.ProcessChange(change)

		case change := <-subAppInstanceStatus.MsgChan():
			subAppInstanceStatus.ProcessChange(change)

		case change := <-subContentTreeStatus.MsgChan():
			subContentTreeStatus.ProcessChange(change)

		case change := <-subVolumeStatus.MsgChan():
			subVolumeStatus.ProcessChange(change)

		case change := <-subDomainMetric.MsgChan():
			subDomainMetric.ProcessChange(change)

		case change := <-subProcessMetric.MsgChan():
			subProcessMetric.ProcessChange(change)

		case change := <-subHostMemory.MsgChan():
			subHostMemory.ProcessChange(change)

		case change := <-subBaseOsStatus.MsgChan():
			subBaseOsStatus.ProcessChange(change)

		case change := <-subLogMetrics.MsgChan():
			subLogMetrics.ProcessChange(change)

		case change := <-subBlobStatus.MsgChan():
			subBlobStatus.ProcessChange(change)

		case change := <-getconfigCtx.subNodeAgentStatus.MsgChan():
			subNodeAgentStatus.ProcessChange(change)

		case change := <-subDeviceNetworkStatus.MsgChan():
			subDeviceNetworkStatus.ProcessChange(change)
			if DNSctx.triggerGetConfig {
				triggerGetConfig(configTickerHandle)
				DNSctx.triggerGetConfig = false
			}
			if DNSctx.triggerDeviceInfo {
				// IP/DNS in device info could have changed
				log.Functionf("NetworkStatus triggered PublishDeviceInfo")
				triggerPublishDevInfo(&zedagentCtx)
				DNSctx.triggerDeviceInfo = false
			}

		case change := <-subAssignableAdapters.MsgChan():
			subAssignableAdapters.ProcessChange(change)

		case change := <-subNetworkMetrics.MsgChan():
			subNetworkMetrics.ProcessChange(change)
			m, err := subNetworkMetrics.Get("global")
			if err != nil {
				log.Errorf("subNetworkMetrics.Get failed: %s",
					err)
			} else {
				networkMetrics = m.(types.NetworkMetrics)
			}

		case change := <-subClientMetrics.MsgChan():
			subClientMetrics.ProcessChange(change)
			m, err := subClientMetrics.Get("global")
			if err != nil {
				log.Errorf("subClientMetrics.Get failed: %s",
					err)
			} else {
				clientMetrics = m.(types.MetricsMap)
			}

		case change := <-subLogmanagerMetrics.MsgChan():
			subLogmanagerMetrics.ProcessChange(change)
			m, err := subLogmanagerMetrics.Get("global")
			if err != nil {
				log.Errorf("subLogmanagerMetrics.Get failed: %s",
					err)
			} else {
				logmanagerMetrics = m.(types.MetricsMap)
			}

		case change := <-subDownloaderMetrics.MsgChan():
			subDownloaderMetrics.ProcessChange(change)
			m, err := subDownloaderMetrics.Get("global")
			if err != nil {
				log.Errorf("subDownloaderMetrics.Get failed: %s",
					err)
			} else {
				downloaderMetrics = m.(types.MetricsMap)
			}

		case change := <-deferredChan:
			start := time.Now()
			zedcloud.HandleDeferred(zedcloudCtx, change, 100*time.Millisecond)
			ps.CheckMaxTimeTopic(agentName, "deferredChan", start,
				warningTime, errorTime)

		case change := <-subCipherMetricsDL.MsgChan():
			subCipherMetricsDL.ProcessChange(change)
			m, err := subCipherMetricsDL.Get("global")
			if err != nil {
				log.Errorf("subCipherMetricsDL.Get failed: %s",
					err)
			} else {
				cipherMetricsDL = m.(types.CipherMetricsMap)
			}

		case change := <-subCipherMetricsDM.MsgChan():
			subCipherMetricsDM.ProcessChange(change)
			m, err := subCipherMetricsDM.Get("global")
			if err != nil {
				log.Errorf("subCipherMetricsDM.Get failed: %s",
					err)
			} else {
				cipherMetricsDM = m.(types.CipherMetricsMap)
			}

		case change := <-subCipherMetricsNim.MsgChan():
			subCipherMetricsNim.ProcessChange(change)
			m, err := subCipherMetricsNim.Get("global")
			if err != nil {
				log.Errorf("subCipherMetricsNim.Get failed: %s",
					err)
			} else {
				cipherMetricsNim = m.(types.CipherMetricsMap)
			}

		case change := <-subNetworkInstanceStatus.MsgChan():
			subNetworkInstanceStatus.ProcessChange(change)

		case change := <-subNetworkInstanceMetrics.MsgChan():
			subNetworkInstanceMetrics.ProcessChange(change)

		case change := <-subDevicePortConfigList.MsgChan():
			subDevicePortConfigList.ProcessChange(change)

		case change := <-subAppFlowMonitor.MsgChan():
			log.Tracef("FlowStats: change called")
			subAppFlowMonitor.ProcessChange(change)

		case change := <-subAppVifIPTrig.MsgChan():
			subAppVifIPTrig.ProcessChange(change)

		case change := <-subEdgeNodeCert.MsgChan():
			subEdgeNodeCert.ProcessChange(change)

		case change := <-subVaultStatus.MsgChan():
			subVaultStatus.ProcessChange(change)

		case change := <-subAttestQuote.MsgChan():
			subAttestQuote.ProcessChange(change)

		case change := <-subEncryptedKeyFromDevice.MsgChan():
			subEncryptedKeyFromDevice.ProcessChange(change)

		case change := <-subAppContainerMetrics.MsgChan():
			subAppContainerMetrics.ProcessChange(change)

		case change := <-subDiskMetric.MsgChan():
			subDiskMetric.ProcessChange(change)

		case change := <-subAppDiskMetric.MsgChan():
			subAppDiskMetric.ProcessChange(change)

		case <-stillRunning.C:
			// Fault injection
			if fatalFlag {
				log.Fatal("Requested fault injection to cause watchdog")
			}
		}
		if hangFlag {
			log.Functionf("Requested to not touch to cause watchdog")
		} else {
			ps.StillRunning(agentName, warningTime, errorTime)
		}
	}
}

func triggerPublishDevInfo(ctxPtr *zedagentContext) {

	log.Function("Triggered PublishDeviceInfo")
	select {
	case ctxPtr.TriggerDeviceInfo <- struct{}{}:
		// Do nothing more
	default:
		// This occurs if we are already trying to send a device info
		// and we get a second and third trigger before that is complete.
		log.Warnf("Failed to send on PublishDeviceInfo")
	}
}

func handleZbootRestarted(ctxArg interface{}, done bool) {
	ctx := ctxArg.(*zedagentContext)
	log.Functionf("handleZbootRestarted(%v)", done)
	if done {
		ctx.zbootRestarted = true
	}
}

func initializeDirs() {

	// create persistent holder directory
	if _, err := os.Stat(types.PersistDir); err != nil {
		log.Tracef("Create %s", types.PersistDir)
		if err := os.MkdirAll(types.PersistDir, 0700); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := os.Stat(types.CertificateDirname); err != nil {
		log.Tracef("Create %s", types.CertificateDirname)
		if err := os.MkdirAll(types.CertificateDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}
	if _, err := os.Stat(checkpointDirname); err != nil {
		log.Tracef("Create %s", checkpointDirname)
		if err := os.MkdirAll(checkpointDirname, 0700); err != nil {
			log.Fatal(err)
		}
	}
}

// handleAppInstanceStatusCreate - Handle AIS create. Publish ZInfoApp
//  and ZInfoDevice to the cloud.
func handleAppInstanceStatusCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	status := statusArg.(types.AppInstanceStatus)
	log.Functionf("handleAppInstanceStatusCreate(%s)", key)
	ctx := ctxArg.(*zedagentContext)
	uuidStr := status.Key()
	PublishAppInfoToZedCloud(ctx, uuidStr, &status, ctx.assignableAdapters,
		ctx.iteration)
	triggerPublishDevInfo(ctx)
	ctx.iteration++
	log.Functionf("handleAppInstanceStatusCreate(%s) DONE", key)
}

// app instance event watch to capture transitions
// and publish to zedCloud
// Handles both create and modify events
func handleAppInstanceStatusModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {

	status := statusArg.(types.AppInstanceStatus)
	log.Functionf("handleAppInstanceStatusModify(%s)", key)
	ctx := ctxArg.(*zedagentContext)
	uuidStr := status.Key()
	PublishAppInfoToZedCloud(ctx, uuidStr, &status, ctx.assignableAdapters,
		ctx.iteration)
	ctx.iteration++
	log.Functionf("handleAppInstanceStatusModify(%s) DONE", key)
}

func handleAppInstanceStatusDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*zedagentContext)
	uuidStr := key
	log.Functionf("handleAppInstanceStatusDelete(%s)", key)
	PublishAppInfoToZedCloud(ctx, uuidStr, nil, ctx.assignableAdapters,
		ctx.iteration)
	triggerPublishDevInfo(ctx)
	ctx.iteration++
	log.Functionf("handleAppInstanceStatusDelete(%s) DONE", key)
}

func lookupAppInstanceStatus(ctx *zedagentContext, key string) *types.AppInstanceStatus {

	sub := ctx.getconfigCtx.subAppInstanceStatus
	st, _ := sub.Get(key)
	if st == nil {
		log.Functionf("lookupAppInstanceStatus(%s) not found", key)
		return nil
	}
	status := st.(types.AppInstanceStatus)
	return &status
}

func handleDNSCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleDNSImpl(ctxArg, key, statusArg)
}

func handleDNSModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleDNSImpl(ctxArg, key, statusArg)
}

func handleDNSImpl(ctxArg interface{}, key string,
	statusArg interface{}) {

	status := statusArg.(types.DeviceNetworkStatus)
	ctx := ctxArg.(*DNSContext)
	if key != "global" {
		log.Functionf("handleDNSImpl: ignoring %s", key)
		return
	}
	log.Functionf("handleDNSImpl for %s", key)
	// Since we report the TestResults we compare the whole struct
	if cmp.Equal(*deviceNetworkStatus, status) {
		log.Functionf("handleDNSImpl no change")
		ctx.DNSinitialized = true
		return
	}
	log.Functionf("handleDNSImpl: changed %v",
		cmp.Diff(*deviceNetworkStatus, status))
	*deviceNetworkStatus = status
	ctx.DNSinitialized = true
	ctx.triggerDeviceInfo = true

	if zedcloudCtx.V2API {
		zedcloud.UpdateTLSProxyCerts(zedcloudCtx)
	}
	log.Functionf("handleDNSImpl done for %s", key)
}

func handleDNSDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	log.Functionf("handleDNSDelete for %s", key)
	ctx := ctxArg.(*DNSContext)

	if key != "global" {
		log.Functionf("handleDNSDelete: ignoring %s", key)
		return
	}
	*deviceNetworkStatus = types.DeviceNetworkStatus{}
	ctx.DNSinitialized = false
	log.Functionf("handleDNSDelete done for %s", key)
}

func handleDPCLCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleDPCLImpl(ctxArg, key, statusArg)
}

func handleDPCLModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleDPCLImpl(ctxArg, key, statusArg)
}

func handleDPCLImpl(ctxArg interface{}, key string,
	statusArg interface{}) {
	ctx := ctxArg.(*zedagentContext)
	if key != "global" {
		log.Functionf("handleDPCLImpl: ignoring %s", key)
		return
	}
	triggerPublishDevInfo(ctx)
}

func handleDPCLDelete(ctxArg interface{}, key string, statusArg interface{}) {

	ctx := ctxArg.(*zedagentContext)
	if key != "global" {
		log.Functionf("handleDPCLDelete: ignoring %s", key)
		return
	}
	log.Functionf("handleDPCLDelete for %s", key)
	triggerPublishDevInfo(ctx)
}

// base os status event handlers
// Report BaseOsStatus to zedcloud
func handleBaseOsStatusCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleBaseOsStatusImpl(ctxArg, key, statusArg)
}

func handleBaseOsStatusModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleBaseOsStatusImpl(ctxArg, key, statusArg)
}

func handleBaseOsStatusImpl(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*zedagentContext)
	triggerPublishDevInfo(ctx)
	log.Functionf("handleBaseOsStatusImpl(%s) done", key)
}

func handleBaseOsStatusDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	log.Functionf("handleBaseOsStatusDelete(%s)", key)
	ctx := ctxArg.(*zedagentContext)
	triggerPublishDevInfo(ctx)
	log.Functionf("handleBaseOsStatusDelete(%s) done", key)
}

// vault status event handlers
// Report VaultStatus to zedcloud
func handleVaultStatusCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleVaultStatusImpl(ctxArg, key, statusArg)
}

func handleVaultStatusModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleVaultStatusImpl(ctxArg, key, statusArg)
}

func handleVaultStatusImpl(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*zedagentContext)
	triggerPublishDevInfo(ctx)
	log.Functionf("handleVaultStatusImpl(%s) done", key)
}

func handleVaultStatusDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	log.Functionf("handleVaultStatusDelete(%s)", key)
	ctx := ctxArg.(*zedagentContext)
	triggerPublishDevInfo(ctx)
	log.Functionf("handleVaultStatusDelete(%s) done", key)
}

func appendError(allErrors string, prefix string, lasterr string) string {
	return fmt.Sprintf("%s%s: %s\n\n", allErrors, prefix, lasterr)
}

func handleGlobalConfigCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleGlobalConfigImpl(ctxArg, key, statusArg)
}

func handleGlobalConfigModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleGlobalConfigImpl(ctxArg, key, statusArg)
}

func handleGlobalConfigImpl(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*zedagentContext)
	if key != "global" {
		log.Functionf("handleGlobalConfigImpl: ignoring %s", key)
		return
	}
	log.Functionf("handleGlobalConfigImpl for %s", key)
	var gcp *types.ConfigItemValueMap
	debug, gcp = agentlog.HandleGlobalConfig(log, ctx.subGlobalConfig, agentName,
		debugOverride, logger)
	if gcp != nil && !ctx.GCInitialized {
		ctx.globalConfig = *gcp
		ctx.GCInitialized = true
	}
	log.Functionf("handleGlobalConfigImpl done for %s", key)
}

func handleGlobalConfigDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*zedagentContext)
	if key != "global" {
		log.Functionf("handleGlobalConfigDelete: ignoring %s", key)
		return
	}
	log.Functionf("handleGlobalConfigDelete for %s", key)
	debug, _ = agentlog.HandleGlobalConfig(log, ctx.subGlobalConfig, agentName,
		debugOverride, logger)
	ctx.globalConfig = *types.DefaultConfigItemValueMap()
	log.Functionf("handleGlobalConfigDelete done for %s", key)
}

func handleAACreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleAAImpl(ctxArg, key, statusArg)
}

func handleAAModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleAAImpl(ctxArg, key, statusArg)
}

func handleAAImpl(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*zedagentContext)
	status := statusArg.(types.AssignableAdapters)
	if key != "global" {
		log.Functionf("handleAAImpl: ignoring %s", key)
		return
	}
	log.Functionf("handleAAImpl() %+v", status)
	*ctx.assignableAdapters = status
	triggerPublishDevInfo(ctx)
	log.Functionf("handleAAImpl() done")
}

func handleAADelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*zedagentContext)
	if key != "global" {
		log.Functionf("handleAADelete: ignoring %s", key)
		return
	}
	log.Functionf("handleAADelete()")
	ctx.assignableAdapters.Initialized = false
	triggerPublishDevInfo(ctx)
	log.Functionf("handleAADelete() done")
}

func handleZbootStatusCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleZbootStatusImpl(ctxArg, key, statusArg)
}

func handleZbootStatusModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleZbootStatusImpl(ctxArg, key, statusArg)
}

func handleZbootStatusImpl(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*zedagentContext)
	if !isZbootValidPartitionLabel(key) {
		log.Errorf("handleZbootStatusImpl: invalid key %s", key)
		return
	}
	log.Functionf("handleZbootStatusImpl: for %s", key)
	// nothing to do
	triggerPublishDevInfo(ctx)
}

func handleZbootStatusDelete(ctxArg interface{}, key string,
	statusArg interface{}) {
	if !isZbootValidPartitionLabel(key) {
		log.Errorf("handleZbootStatusDelete: invalid key %s", key)
		return
	}
	log.Functionf("handleZbootStatusDelete: for %s", key)
	// Nothing to do
}

func handleNodeAgentStatusCreate(ctxArg interface{}, key string,
	statusArg interface{}) {
	handleNodeAgentStatusImpl(ctxArg, key, statusArg)
}

func handleNodeAgentStatusModify(ctxArg interface{}, key string,
	statusArg interface{}, oldStatusArg interface{}) {
	handleNodeAgentStatusImpl(ctxArg, key, statusArg)
}

func handleNodeAgentStatusImpl(ctxArg interface{}, key string,
	statusArg interface{}) {

	getconfigCtx := ctxArg.(*getconfigContext)
	status := statusArg.(types.NodeAgentStatus)
	log.Functionf("handleNodeAgentStatusImpl: updateInProgress %t rebootReason %s bootReason %s",
		status.UpdateInprogress, status.RebootReason,
		status.BootReason.String())
	updateInprogress := getconfigCtx.updateInprogress
	ctx := getconfigCtx.zedagentCtx
	ctx.remainingTestTime = status.RemainingTestTime
	getconfigCtx.updateInprogress = status.UpdateInprogress
	ctx.rebootTime = status.RebootTime
	ctx.rebootStack = status.RebootStack
	ctx.rebootReason = status.RebootReason
	ctx.bootReason = status.BootReason
	ctx.restartCounter = status.RestartCounter
	// if config reboot command was initiated and
	// was deferred, and the device is not in inprogress
	// state, initiate the reboot process
	if ctx.rebootCmdDeferred &&
		updateInprogress && !status.UpdateInprogress {
		log.Functionf("TestComplete and deferred reboot")
		ctx.rebootCmdDeferred = false
		infoStr := fmt.Sprintf("TestComplete and deferred Reboot Cmd")
		handleRebootCmd(ctx, infoStr)
	}
	if status.DeviceReboot {
		handleDeviceReboot(ctx)
	}
	triggerPublishDevInfo(ctx)
	log.Functionf("handleNodeAgentStatusImpl: done.")
}

func handleNodeAgentStatusDelete(ctxArg interface{}, key string,
	statusArg interface{}) {
	ctx := ctxArg.(*zedagentContext)
	log.Functionf("handleNodeAgentStatusDelete: for %s", key)
	// Nothing to do
	triggerPublishDevInfo(ctx)
}
