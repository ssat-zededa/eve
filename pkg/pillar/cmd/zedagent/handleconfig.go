// Copyright (c) 2017-2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package zedagent

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang/protobuf/proto"
	zconfig "github.com/lf-edge/eve/api/go/config"
	"github.com/lf-edge/eve/pkg/pillar/agentlog"
	"github.com/lf-edge/eve/pkg/pillar/flextimer"
	"github.com/lf-edge/eve/pkg/pillar/hardware"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/utils"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
	"github.com/satori/go.uuid"
	log "github.com/sirupsen/logrus"
)

var configApi string = "api/v1/edgedevice/config"
var statusApi string = "api/v1/edgedevice/info"
var metricsApi string = "api/v1/edgedevice/metrics"
var flowlogAPI = "api/v1/edgedevice/flowlog"

// This is set once at init time and not changed
var serverName string
var serverNameAndPort string

type getconfigContext struct {
	zedagentCtx              *zedagentContext // Cross link
	ledManagerCount          int              // Current count
	configReceived           bool
	configGetStatus          types.ConfigGetStatus
	updateInprogress         bool
	readSavedConfig          bool
	configTickerHandle       interface{}
	metricsTickerHandle      interface{}
	pubDevicePortConfig      pubsub.Publication
	pubPhysicalIOAdapters    pubsub.Publication
	devicePortConfig         types.DevicePortConfig
	pubNetworkXObjectConfig  pubsub.Publication
	subAppInstanceStatus     pubsub.Subscription
	subDomainMetric          pubsub.Subscription
	subHostMemory            pubsub.Subscription
	subNodeAgentStatus       pubsub.Subscription
	pubZedAgentStatus        pubsub.Publication
	pubAppInstanceConfig     pubsub.Publication
	pubAppNetworkConfig      pubsub.Publication
	pubCertObjConfig         pubsub.Publication
	pubBaseOsConfig          pubsub.Publication
	pubDatastoreConfig       pubsub.Publication
	pubNetworkInstanceConfig pubsub.Publication
	pubCipherContextConfig   pubsub.Publication
	pubControllerCertConfig  pubsub.Publication
	rebootFlag               bool
}

// tlsConfig is initialized once i.e. effectively a constant
var zedcloudCtx zedcloud.ZedCloudContext

// devUUID is set in handleConfigInit and never changed
var devUUID uuid.UUID

// XXX need to support recreating devices. Remove when zedcloud preserves state
var zcdevUUID uuid.UUID

// Really a constant
var nilUUID uuid.UUID

func handleConfigInit(networkSendTimeout uint32) {

	// get the server name
	bytes, err := ioutil.ReadFile(types.ServerFileName)
	if err != nil {
		log.Fatal(err)
	}
	serverNameAndPort = strings.TrimSpace(string(bytes))
	serverName = strings.Split(serverNameAndPort, ":")[0]

	v2api := false // XXX set
	zedcloudCtx.DeviceNetworkStatus = deviceNetworkStatus
	zedcloudCtx.FailureFunc = zedcloud.ZedCloudFailure
	zedcloudCtx.SuccessFunc = zedcloud.ZedCloudSuccess
	zedcloudCtx.DevSerial = hardware.GetProductSerial()
	zedcloudCtx.DevSoftSerial = hardware.GetSoftSerial()
	zedcloudCtx.NetworkSendTimeout = networkSendTimeout
	zedcloudCtx.V2API = v2api
	log.Infof("Configure Get Device Serial %s, Soft Serial %s\n", zedcloudCtx.DevSerial,
		zedcloudCtx.DevSoftSerial)

	// XXX need to redo this since the root certificates can change
	err = zedcloud.UpdateTLSConfig(&zedcloudCtx, serverName, nil)
	if err != nil {
		log.Fatal(err)
	}

	b, err := ioutil.ReadFile(types.UUIDFileName)
	if err != nil {
		// XXX this can fail if agents have crashed
		log.Fatal("ReadFile", err, types.UUIDFileName)
	}
	uuidStr := strings.TrimSpace(string(b))
	devUUID, err = uuid.FromString(uuidStr)
	if err != nil {
		log.Fatal("uuid.FromString", err, string(b))
	}
	log.Infof("Read UUID %s\n", devUUID)
	zedcloudCtx.DevUUID = devUUID
	zcdevUUID = devUUID
}

// Run a periodic fetch of the config
func configTimerTask(handleChannel chan interface{},
	getconfigCtx *getconfigContext) {

	configUrl := serverNameAndPort + "/" + configApi
	iteration := 0
	getconfigCtx.rebootFlag = getLatestConfig(configUrl, iteration,
		getconfigCtx)
	publishZedAgentStatus(getconfigCtx)

	configInterval := getconfigCtx.zedagentCtx.globalConfig.ConfigInterval
	interval := time.Duration(configInterval) * time.Second
	max := float64(interval)
	min := max * 0.3
	ticker := flextimer.NewRangeTicker(time.Duration(min),
		time.Duration(max))
	// Return handle to caller
	handleChannel <- ticker

	// Run a periodic timer so we always update StillRunning
	stillRunning := time.NewTicker(25 * time.Second)
	agentlog.StillRunning(agentName+"config", warningTime, errorTime)

	for {
		select {
		case <-ticker.C:
			start := time.Now()
			iteration += 1
			rebootFlag := getLatestConfig(configUrl, iteration, getconfigCtx)
			getconfigCtx.rebootFlag = getconfigCtx.rebootFlag || rebootFlag
			pubsub.CheckMaxTimeTopic(agentName+"config", "getLastestConfig", start,
				warningTime, errorTime)
			publishZedAgentStatus(getconfigCtx)

		case <-stillRunning.C:
			if getconfigCtx.rebootFlag {
				log.Infof("reboot flag set")
			}
		}
		agentlog.StillRunning(agentName+"config", warningTime, errorTime)
	}
}

func triggerGetConfig(tickerHandle interface{}) {
	log.Infof("triggerGetConfig()\n")
	flextimer.TickNow(tickerHandle)
}

// Called when globalConfig changes
// Assumes the caller has verifier that the interval has changed
func updateConfigTimer(configInterval uint32, tickerHandle interface{}) {

	if tickerHandle == nil {
		// Happens if we have a GlobalConfig setting in /persist/
		log.Warnf("updateConfigTimer: no configTickerHandle yet")
		return
	}
	interval := time.Duration(configInterval) * time.Second
	log.Infof("updateConfigTimer() change to %v\n", interval)
	max := float64(interval)
	min := max * 0.3
	flextimer.UpdateRangeTicker(tickerHandle,
		time.Duration(min), time.Duration(max))
	// Force an immediate timout since timer could have decreased
	flextimer.TickNow(tickerHandle)
}

// Start by trying the all the free management ports and then all the non-free
// until one succeeds in communicating with the cloud.
// We use the iteration argument to start at a different point each time.
// Returns a rebootFlag
func getLatestConfig(url string, iteration int,
	getconfigCtx *getconfigContext) bool {

	log.Debugf("getLatestConfig(%s, %d)\n", url, iteration)

	const return400 = false
	getconfigCtx.configGetStatus = types.ConfigGetFail
	b, cr, err := generateConfigRequest()
	if err != nil {
		// XXX	fatal?
		return false
	}
	buf := bytes.NewBuffer(b)
	size := int64(proto.Size(cr))
	resp, contents, rtf, err := zedcloud.SendOnAllIntf(zedcloudCtx, url, size, buf, iteration, return400)
	if err != nil {
		newCount := 2
		if rtf {
			log.Errorf("getLatestConfig remoteTemporaryFailure: %s", err)
			newCount = 3 // Almost connected to controller!
			// Don't treat as upgrade failure
			if getconfigCtx.updateInprogress {
				log.Warnf("remoteTemporaryFailure don't fail update")
				getconfigCtx.configGetStatus = types.ConfigGetTemporaryFail
			}
		} else {
			log.Errorf("getLatestConfig failed: %s", err)
		}
		if getconfigCtx.ledManagerCount == 4 {
			// Inform ledmanager about loss of config from cloud
			utils.UpdateLedManagerConfig(newCount)
			getconfigCtx.ledManagerCount = newCount
		}
		// If we didn't yet get a config, then look for a file
		// XXX should we try a few times?
		// XXX different policy if updateInProgress? No fallback for now
		if !getconfigCtx.updateInprogress &&
			!getconfigCtx.readSavedConfig && !getconfigCtx.configReceived {

			config, err := readSavedProtoMessage(
				getconfigCtx.zedagentCtx.globalConfig.StaleConfigTime,
				checkpointDirname+"/lastconfig", false)
			if err != nil {
				log.Errorf("getconfig: %v\n", err)
				return false
			}
			if config != nil {
				log.Info("Using saved config")
				getconfigCtx.readSavedConfig = true
				getconfigCtx.configGetStatus = types.ConfigGetReadSaved
				return inhaleDeviceConfig(config, getconfigCtx,
					true)
			}
		}
		return false
	}

	if err := validateConfigMessage(url, resp); err != nil {
		log.Errorln("validateConfigMessage: ", err)
		// Inform ledmanager about cloud connectivity
		utils.UpdateLedManagerConfig(3)
		getconfigCtx.ledManagerCount = 3
		return false
	}

	changed, config, err := readConfigResponseProtoMessage(resp, contents)
	if err != nil {
		log.Errorln("readConfigResponseProtoMessage: ", err)
		// Inform ledmanager about cloud connectivity
		utils.UpdateLedManagerConfig(3)
		getconfigCtx.ledManagerCount = 3
		return false
	}

	// Inform ledmanager about config received from cloud
	utils.UpdateLedManagerConfig(4)
	getconfigCtx.ledManagerCount = 4

	if !getconfigCtx.configReceived {
		getconfigCtx.configReceived = true
	}
	getconfigCtx.configGetStatus = types.ConfigGetSuccess

	if !changed {
		log.Debugf("Configuration from zedcloud is unchanged\n")
		return false
	}
	writeReceivedProtoMessage(contents)

	return inhaleDeviceConfig(config, getconfigCtx, false)
}

func validateConfigMessage(url string, r *http.Response) error {

	var ctTypeStr = "Content-Type"
	var ctTypeProtoStr = "application/x-proto-binary"

	ct := r.Header.Get(ctTypeStr)
	if ct == "" {
		return fmt.Errorf("No content-type")
	}
	mimeType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return fmt.Errorf("Get Content-type error")
	}
	switch mimeType {
	case ctTypeProtoStr:
		return nil
	default:
		return fmt.Errorf("Content-type %s not supported",
			mimeType)
	}
}

func writeReceivedProtoMessage(contents []byte) {
	writeProtoMessage("lastconfig", contents)
}

// XXX for debug we track these
func writeSentMetricsProtoMessage(contents []byte) {
	writeProtoMessage("lastmetrics", contents)
}

// XXX for debug we track these
func writeSentDeviceInfoProtoMessage(contents []byte) {
	writeProtoMessage("lastdeviceinfo", contents)
}

// XXX for debug we track these
func writeSentAppInfoProtoMessage(contents []byte) {
	writeProtoMessage("lastappinfo", contents)
}

func writeProtoMessage(filename string, contents []byte) {
	filename = checkpointDirname + "/" + filename
	err := ioutil.WriteFile(filename, contents, 0744)
	if err != nil {
		log.Fatal("writeReceiveProtoMessage", err)
		return
	}
}

// If the file exists then read the config
// Ignore if if older than StaleConfigTime seconds
func readSavedProtoMessage(staleConfigTime uint32,
	filename string, force bool) (*zconfig.EdgeDevConfig, error) {
	info, err := os.Stat(filename)
	if err != nil {
		if os.IsNotExist(err) && !force {
			return nil, nil
		} else {
			return nil, err
		}
	}
	age := time.Since(info.ModTime())
	staleLimit := time.Second * time.Duration(staleConfigTime)
	if !force && age > staleLimit {
		errStr := fmt.Sprintf("savedProto too old: age %v limit %d\n",
			age, staleLimit)
		log.Errorln(errStr)
		return nil, nil
	}
	contents, err := ioutil.ReadFile(filename)
	if err != nil {
		log.Errorln("readSavedProtoMessage", err)
		return nil, err
	}
	var config = &zconfig.EdgeDevConfig{}

	err = proto.Unmarshal(contents, config)
	if err != nil {
		log.Errorf("readSavedProtoMessage Unmarshalling failed: %v",
			err)
		return nil, err
	}
	return config, nil
}

// The most recent config hash we received. Starts empty
var prevConfigHash string

func generateConfigRequest() ([]byte, *zconfig.ConfigRequest, error) {
	log.Debugf("generateConfigRequest() sending hash %s", prevConfigHash)
	configRequest := &zconfig.ConfigRequest{
		ConfigHash: prevConfigHash,
	}
	b, err := proto.Marshal(configRequest)
	if err != nil {
		log.Errorln(err)
		return nil, nil, err
	}
	return b, configRequest, nil
}

// Returns changed, config, error. The changed is based the ConfigRequest vs
// the ConfigResponse hash
func readConfigResponseProtoMessage(resp *http.Response, contents []byte) (bool, *zconfig.EdgeDevConfig, error) {

	if resp.StatusCode == http.StatusNotModified {
		log.Debugf("StatusNotModified")
		if len(contents) > 0 {
			// XXX controller should omit full content
			log.Infof("XXX StatusNotModified with len %d",
				len(contents))
		}
		return false, nil, nil
	}

	var configResponse = &zconfig.ConfigResponse{}
	err := proto.Unmarshal(contents, configResponse)
	if err != nil {
		log.Errorf("Unmarshalling failed: %v", err)
		return false, nil, err
	}
	hash := configResponse.GetConfigHash()
	if hash == prevConfigHash {
		log.Debugf("Same ConfigHash %s", hash)
		if len(contents) > 0 {
			// XXX controller should omit full content
			log.Infof("XXX same hash %s with len %d",
				hash, len(contents))
		}
		return false, nil, nil
	}
	log.Debugf("Change in ConfigHash from %s to %s", prevConfigHash, hash)
	prevConfigHash = hash
	config := configResponse.GetConfig()
	return true, config, nil
}

// Returns a rebootFlag
func inhaleDeviceConfig(config *zconfig.EdgeDevConfig, getconfigCtx *getconfigContext, usingSaved bool) bool {
	log.Debugf("Inhaling config")

	// if they match return
	var devId = &zconfig.UUIDandVersion{}

	devId = config.GetId()
	if devId != nil {
		id, err := uuid.FromString(devId.Uuid)
		if err != nil {
			log.Errorf("Invalid UUID %s from cloud: %s\n",
				devId.Uuid, err)
			return false
		}
		if id != devUUID {
			// XXX logic to handle re-registering a device private
			// key with zedcloud. We accept a new UUID from the
			// cloud and use that in our reports, but we do
			// not update the hostname nor LISP.
			// XXX remove once zedcloud preserves state.
			if id != zcdevUUID {
				log.Infof("XXX Device UUID changed from %s to %s\n",
					zcdevUUID.String(), id.String())
				zcdevUUID = id
				ctx := getconfigCtx.zedagentCtx
				triggerPublishDevInfo(ctx)
			}

		}
	}
	handleLookupParam(getconfigCtx, config)

	// add new BaseOS/App instances; returns rebootFlag
	return parseConfig(config, getconfigCtx, usingSaved)
}

func publishZedAgentStatus(getconfigCtx *getconfigContext) {
	ctx := getconfigCtx.zedagentCtx
	status := types.ZedAgentStatus{
		Name:            agentName,
		ConfigGetStatus: getconfigCtx.configGetStatus,
		RebootCmd:       ctx.rebootCmd,
		RebootReason:    ctx.currentRebootReason,
	}
	pub := getconfigCtx.pubZedAgentStatus
	pub.Publish(agentName, status)
}
