// Copyright (c) 2017-2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/google/go-cmp/cmp"
	"github.com/lf-edge/eve/api/go/register"
	"github.com/lf-edge/eve/pkg/pillar/agentlog"
	"github.com/lf-edge/eve/pkg/pillar/flextimer"
	"github.com/lf-edge/eve/pkg/pillar/hardware"
	"github.com/lf-edge/eve/pkg/pillar/pidfile"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/utils"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
	"github.com/satori/go.uuid"
	log "github.com/sirupsen/logrus"
)

const (
	agentName   = "zedclient"
	maxDelay    = time.Second * 600 // 10 minutes
	uuidMaxWait = time.Second * 60  // 1 minute
	// Time limits for event loop handlers
	errorTime   = 3 * time.Minute
	warningTime = 40 * time.Second
	return400   = false
)

// Really a constant
var nilUUID uuid.UUID

// Set from Makefile
var Version = "No version specified"

// Assumes the config files are in IdentityDirname, which is /config
// by default. The files are
//  root-certificate.pem	Root CA cert(s) for object signing
//  server			Fixed? Written if redirected. factory-root-cert?
//  onboard.cert.pem, onboard.key.pem	Per device onboarding certificate/key
//  		   		for selfRegister operation
//  device.cert.pem,
//  device.key.pem		Device certificate/key created before this
//  		     		client is started.
//  uuid			Written by getUuid operation
//  hardwaremodel		Written by getUuid if server returns a hardwaremodel
//  enterprise			Written by getUuid if server returns an enterprise
//  name			Written by getUuid if server returns a name
//
//

type clientContext struct {
	subDeviceNetworkStatus pubsub.Subscription
	deviceNetworkStatus    *types.DeviceNetworkStatus
	usableAddressCount     int
	subGlobalConfig        pubsub.Subscription
	globalConfig           *types.GlobalConfig
	zedcloudCtx            *zedcloud.ZedCloudContext
}

var (
	debug             = false
	debugOverride     bool // From command line arg
	serverNameAndPort string
)

func Run(ps *pubsub.PubSub) { //nolint:gocyclo
	versionPtr := flag.Bool("v", false, "Version")
	debugPtr := flag.Bool("d", false, "Debug flag")
	curpartPtr := flag.String("c", "", "Current partition")
	stdoutPtr := flag.Bool("s", false, "Use stdout")
	noPidPtr := flag.Bool("p", false, "Do not check for running client")
	maxRetriesPtr := flag.Int("r", 0, "Max retries")
	flag.Parse()

	versionFlag := *versionPtr
	debug = *debugPtr
	debugOverride = debug
	if debugOverride {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}
	curpart := *curpartPtr
	useStdout := *stdoutPtr
	noPidFlag := *noPidPtr
	maxRetries := *maxRetriesPtr
	args := flag.Args()
	if versionFlag {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return
	}
	// Sending json log format to stdout
	logf, err := agentlog.Init("client", curpart)
	if err != nil {
		log.Fatal(err)
	}
	defer logf.Close()
	if useStdout {
		if logf == nil {
			log.SetOutput(os.Stdout)
		} else {
			multi := io.MultiWriter(logf, os.Stdout)
			log.SetOutput(multi)
		}
	}
	if !noPidFlag {
		if err := pidfile.CheckAndCreatePidfile(agentName); err != nil {
			log.Fatal(err)
		}
	}
	log.Infof("Starting %s\n", agentName)
	operations := map[string]bool{
		"selfRegister": false,
		"getUuid":      false,
	}
	for _, op := range args {
		if _, ok := operations[op]; ok {
			operations[op] = true
		} else {
			log.Errorf("Unknown arg %s\n", op)
			log.Fatal("Usage: " + os.Args[0] +
				"[-o] [<operations>...]")
		}
	}

	hardwaremodelFileName := types.IdentityDirname + "/hardwaremodel"
	enterpriseFileName := types.IdentityDirname + "/enterprise"
	nameFileName := types.IdentityDirname + "/name"

	cms := zedcloud.GetCloudMetrics() // Need type of data
	pub, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: cms,
	})
	if err != nil {
		log.Fatal(err)
	}

	var oldUUID uuid.UUID
	b, err := ioutil.ReadFile(types.UUIDFileName)
	if err == nil {
		uuidStr := strings.TrimSpace(string(b))
		oldUUID, err = uuid.FromString(uuidStr)
		if err != nil {
			log.Warningf("Malformed UUID file ignored: %s\n", err)
		}
	}
	// Check if we have a /config/hardwaremodel file
	oldHardwaremodel := hardware.GetHardwareModelOverride()

	clientCtx := clientContext{
		deviceNetworkStatus: &types.DeviceNetworkStatus{},
		globalConfig:        &types.GlobalConfigDefaults,
	}

	// Look for global config such as log levels
	subGlobalConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler: handleGlobalConfigModify,
		ModifyHandler: handleGlobalConfigModify,
		DeleteHandler: handleGlobalConfigDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		TopicImpl:     types.GlobalConfig{},
		Ctx:           &clientCtx,
	})

	if err != nil {
		log.Fatal(err)
	}
	clientCtx.subGlobalConfig = subGlobalConfig
	subGlobalConfig.Activate()

	subDeviceNetworkStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler: handleDNSModify,
		ModifyHandler: handleDNSModify,
		DeleteHandler: handleDNSDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		AgentName:     "nim",
		TopicImpl:     types.DeviceNetworkStatus{},
		Ctx:           &clientCtx,
	})
	if err != nil {
		log.Fatal(err)
	}
	clientCtx.subDeviceNetworkStatus = subDeviceNetworkStatus
	subDeviceNetworkStatus.Activate()

	// XXX set propertly
	v2api := false
	zedcloudCtx := zedcloud.ZedCloudContext{
		DeviceNetworkStatus: clientCtx.deviceNetworkStatus,
		FailureFunc:         zedcloud.ZedCloudFailure,
		SuccessFunc:         zedcloud.ZedCloudSuccess,
		NetworkSendTimeout:  clientCtx.globalConfig.NetworkSendTimeout,
		V2API:               v2api,
	}

	// Get device serial number
	zedcloudCtx.DevSerial = hardware.GetProductSerial()
	zedcloudCtx.DevSoftSerial = hardware.GetSoftSerial()
	clientCtx.zedcloudCtx = &zedcloudCtx
	log.Infof("Client Get Device Serial %s, Soft Serial %s\n", zedcloudCtx.DevSerial,
		zedcloudCtx.DevSoftSerial)

	// Run a periodic timer so we always update StillRunning
	stillRunning := time.NewTicker(25 * time.Second)
	agentlog.StillRunning(agentName, warningTime, errorTime)

	// Wait for a usable IP address.
	// After 5 seconds we check; if we already have a UUID we proceed.
	// That ensures that we will start zedagent and it will check
	// the cloudGoneTime if we are doing an imake update.
	t1 := time.NewTimer(5 * time.Second)

	ticker := flextimer.NewExpTicker(time.Second, maxDelay, 0.0)

	// XXX redo in ticker case to handle change to servername?
	server, err := ioutil.ReadFile(types.ServerFileName)
	if err != nil {
		log.Fatal(err)
	}
	serverNameAndPort = strings.TrimSpace(string(server))
	serverName := strings.Split(serverNameAndPort, ":")[0]

	var onboardCert tls.Certificate
	var deviceCertPem []byte
	var onboardTLSConfig *tls.Config

	if operations["selfRegister"] {
		var err error
		onboardCert, err = tls.LoadX509KeyPair(types.OnboardCertName,
			types.OnboardKeyName)
		if err != nil {
			log.Fatal(err)
		}
		onboardTLSConfig, err = zedcloud.GetTlsConfig(zedcloudCtx.DeviceNetworkStatus,
			serverName, &onboardCert, &zedcloudCtx)
		if err != nil {
			log.Fatal(err)
		}
		// Load device text cert for upload
		deviceCertPem, err = ioutil.ReadFile(types.DeviceCertName)
		if err != nil {
			log.Fatal(err)
		}
	}

	// Load device cert
	deviceCert, err := zedcloud.GetClientCert()
	if err != nil {
		log.Fatal(err)
	}
	tlsConfig, err := zedcloud.GetTlsConfig(zedcloudCtx.DeviceNetworkStatus,
		serverName, &deviceCert, &zedcloudCtx)
	if err != nil {
		log.Fatal(err)
	}

	done := false
	var devUUID uuid.UUID
	var hardwaremodel string
	var enterprise string
	var name string
	gotUUID := false
	retryCount := 0
	for !done {
		log.Infof("Waiting for usableAddressCount %d and done %v\n",
			clientCtx.usableAddressCount, done)
		select {
		case change := <-subGlobalConfig.MsgChan():
			subGlobalConfig.ProcessChange(change)

		case change := <-subDeviceNetworkStatus.MsgChan():
			subDeviceNetworkStatus.ProcessChange(change)

		case <-ticker.C:
			if clientCtx.usableAddressCount == 0 {
				log.Infof("ticker and no usableAddressCount")
				// XXX keep exponential unchanged?
				break
			}
			// XXX server/tls setup from above to capture change
			// to server file?
			if operations["selfRegister"] {
				done = selfRegister(zedcloudCtx, onboardTLSConfig, deviceCertPem, retryCount)
				if !done && operations["getUuid"] {
					// Check if getUUid succeeds
					done, devUUID, hardwaremodel, enterprise, name = doGetUUID(zedcloudCtx, tlsConfig, retryCount)
					if done {
						log.Infof("getUUID succeeded; selfRegister no longer needed")
						gotUUID = true
					}
				}
			}
			if !gotUUID && operations["getUuid"] {
				done, devUUID, hardwaremodel, enterprise, name = doGetUUID(zedcloudCtx, tlsConfig, retryCount)
				if done {
					log.Infof("getUUID succeeded; selfRegister no longer needed")
					gotUUID = true
				}
				if oldUUID != nilUUID && retryCount > 2 {
					log.Infof("Sticking with old UUID\n")
					devUUID = oldUUID
					done = true
					break
				}
			}
			retryCount++
			if maxRetries != 0 && retryCount > maxRetries {
				log.Errorf("Exceeded %d retries",
					maxRetries)
				os.Exit(1)
			}

		case <-t1.C:
			// If we already know a uuid we can skip
			// This might not set hardwaremodel when upgrading
			// an onboarded system without /config/hardwaremodel.
			// Unlikely to have a network outage during that
			// upgrade *and* require an override.
			if clientCtx.usableAddressCount == 0 &&
				operations["getUuid"] && oldUUID != nilUUID {

				log.Infof("Already have a UUID %s; declaring success\n",
					oldUUID.String())
				done = true
			}

		case <-stillRunning.C:
		}
		agentlog.StillRunning(agentName, warningTime, errorTime)
	}

	// Post loop code
	if devUUID != nilUUID {
		doWrite := true
		if oldUUID != nilUUID {
			if oldUUID != devUUID {
				log.Infof("Replacing existing UUID %s\n",
					oldUUID.String())
			} else {
				log.Infof("No change to UUID %s\n",
					devUUID)
				doWrite = false
			}
		} else {
			log.Infof("Got config with UUID %s\n", devUUID)
		}

		if doWrite {
			b := []byte(fmt.Sprintf("%s\n", devUUID))
			err = ioutil.WriteFile(types.UUIDFileName, b, 0644)
			if err != nil {
				log.Fatal("WriteFile", err, types.UUIDFileName)
			}
			log.Debugf("Wrote UUID %s\n", devUUID)
		}
		doWrite = true
		if hardwaremodel != "" {
			if oldHardwaremodel != hardwaremodel {
				log.Infof("Replacing existing hardwaremodel %s with %s\n",
					oldHardwaremodel, hardwaremodel)
			} else {
				log.Infof("No change to hardwaremodel %s\n",
					hardwaremodel)
				doWrite = false
			}
		} else {
			log.Infof("Got config with no hardwaremodel\n")
			doWrite = false
		}

		if doWrite {
			// Note that no CRLF
			b := []byte(hardwaremodel)
			err = ioutil.WriteFile(hardwaremodelFileName, b, 0644)
			if err != nil {
				log.Fatal("WriteFile", err,
					hardwaremodelFileName)
			}
			log.Debugf("Wrote hardwaremodel %s\n", hardwaremodel)
		}
		// We write the strings even if empty to make sure we have the most
		// recents. Since this is for debug use we are less careful
		// than for the hardwaremodel.
		b = []byte(enterprise) // Note that no CRLF
		err = ioutil.WriteFile(enterpriseFileName, b, 0644)
		if err != nil {
			log.Fatal("WriteFile", err, enterpriseFileName)
		}
		log.Debugf("Wrote enterprise %s\n", enterprise)
		b = []byte(name) // Note that no CRLF
		err = ioutil.WriteFile(nameFileName, b, 0644)
		if err != nil {
			log.Fatal("WriteFile", err, nameFileName)
		}
		log.Debugf("Wrote name %s\n", name)
	}

	err = pub.Publish("global", zedcloud.GetCloudMetrics())
	if err != nil {
		log.Errorln(err)
	}
}

// Post something without a return type.
// Returns true when done; false when retry
func myPost(zedcloudCtx zedcloud.ZedCloudContext, tlsConfig *tls.Config,
	requrl string, retryCount int, reqlen int64, b *bytes.Buffer) (bool, *http.Response, []byte) {

	zedcloudCtx.TlsConfig = tlsConfig
	resp, contents, rtf, err := zedcloud.SendOnAllIntf(zedcloudCtx,
		requrl, reqlen, b, retryCount, return400)
	if err != nil {
		if rtf {
			log.Errorf("remoteTemporaryFailure %s", err)
		} else {
			log.Errorln(err)
		}
		return false, resp, contents
	}

	if !zedcloudCtx.NoLedManager {
		// Inform ledmanager about cloud connectivity
		utils.UpdateLedManagerConfig(3)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if !zedcloudCtx.NoLedManager {
			// Inform ledmanager about existence in cloud
			utils.UpdateLedManagerConfig(4)
		}
		log.Infof("%s StatusOK\n", requrl)
	case http.StatusCreated:
		if !zedcloudCtx.NoLedManager {
			// Inform ledmanager about existence in cloud
			utils.UpdateLedManagerConfig(4)
		}
		log.Infof("%s StatusCreated\n", requrl)
	case http.StatusConflict:
		if !zedcloudCtx.NoLedManager {
			// Inform ledmanager about brokenness
			utils.UpdateLedManagerConfig(10)
		}
		log.Errorf("%s StatusConflict\n", requrl)
		// Retry until fixed
		log.Errorf("%s\n", string(contents))
		return false, resp, contents
	case http.StatusNotModified:
		// Caller needs to handle
		return false, resp, contents
	default:
		log.Errorf("%s statuscode %d %s\n",
			requrl, resp.StatusCode,
			http.StatusText(resp.StatusCode))
		log.Errorf("%s\n", string(contents))
		return false, resp, contents
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		log.Errorf("%s no content-type\n", requrl)
		return false, resp, contents
	}
	mimeType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		log.Errorf("%s ParseMediaType failed %v\n", requrl, err)
		return false, resp, contents
	}
	switch mimeType {
	case "application/x-proto-binary", "application/json", "text/plain":
		log.Debugf("Received reply %s\n", string(contents))
	default:
		log.Errorln("Incorrect Content-Type " + mimeType)
		return false, resp, contents
	}
	return true, resp, contents
}

// Returns true when done; false when retry
func selfRegister(zedcloudCtx zedcloud.ZedCloudContext, tlsConfig *tls.Config, deviceCertPem []byte, retryCount int) bool {
	// XXX add option to get this from a file in /config + override
	// logic
	productSerial := hardware.GetProductSerial()
	productSerial = strings.TrimSpace(productSerial)
	softSerial := hardware.GetSoftSerial()
	softSerial = strings.TrimSpace(softSerial)
	log.Infof("ProductSerial %s, SoftwareSerial %s\n", productSerial, softSerial)

	registerCreate := &register.ZRegisterMsg{
		PemCert:    []byte(base64.StdEncoding.EncodeToString(deviceCertPem)),
		Serial:     productSerial,
		SoftSerial: softSerial,
	}
	b, err := proto.Marshal(registerCreate)
	if err != nil {
		log.Errorln(err)
		return false
	}
	requrl := serverNameAndPort + "/api/v1/edgedevice/register"
	done, resp, contents := myPost(zedcloudCtx, tlsConfig,
		requrl, retryCount,
		int64(len(b)), bytes.NewBuffer(b))
	if resp != nil && resp.StatusCode == http.StatusNotModified {
		if !zedcloudCtx.NoLedManager {
			// Inform ledmanager about brokenness
			utils.UpdateLedManagerConfig(10)
		}
		log.Errorf("%s StatusNotModified\n", requrl)
		// Retry until fixed
		log.Errorf("%s\n", string(contents))
		done = false
	}
	return done
}

func doGetUUID(zedcloudCtx zedcloud.ZedCloudContext, tlsConfig *tls.Config,
	retryCount int) (bool, uuid.UUID, string, string, string) {

	var resp *http.Response
	var contents []byte

	requrl := serverNameAndPort + "/api/v1/edgedevice/config"
	b, err := generateConfigRequest()
	if err != nil {
		log.Errorln(err)
		return false, nilUUID, "", "", ""
	}
	var done bool
	done, resp, contents = myPost(zedcloudCtx, tlsConfig, requrl, retryCount,
		int64(len(b)), bytes.NewBuffer(b))
	if resp != nil && resp.StatusCode == http.StatusNotModified {
		// Acceptable response for a ConfigRequest POST
		done = true
	}
	if !done {
		return false, nilUUID, "", "", ""
	}
	devUUID, hardwaremodel, enterprise, name, err := parseConfig(requrl, resp, contents)
	if err == nil {
		// Inform ledmanager about config received from cloud
		if !zedcloudCtx.NoLedManager {
			utils.UpdateLedManagerConfig(4)
		}
		return true, devUUID, hardwaremodel, enterprise, name
	}
	// Keep on trying until it parses
	log.Errorf("Failed parsing uuid: %s\n", err)
	return false, nilUUID, "", "", ""
}

// Handles both create and modify events
func handleGlobalConfigModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*clientContext)
	if key != "global" {
		log.Debugf("handleGlobalConfigModify: ignoring %s\n", key)
		return
	}
	log.Infof("handleGlobalConfigModify for %s\n", key)
	var gcp *types.GlobalConfig
	debug, gcp = agentlog.HandleGlobalConfig(ctx.subGlobalConfig, agentName,
		debugOverride)
	if gcp != nil {
		ctx.globalConfig = gcp
	}
	log.Infof("handleGlobalConfigModify done for %s\n", key)
}

func handleGlobalConfigDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*clientContext)
	if key != "global" {
		log.Debugf("handleGlobalConfigDelete: ignoring %s\n", key)
		return
	}
	log.Infof("handleGlobalConfigDelete for %s\n", key)
	debug, _ = agentlog.HandleGlobalConfig(ctx.subGlobalConfig, agentName,
		debugOverride)
	*ctx.globalConfig = types.GlobalConfigDefaults
	log.Infof("handleGlobalConfigDelete done for %s\n", key)
}

// Handles both create and modify events
func handleDNSModify(ctxArg interface{}, key string, statusArg interface{}) {

	status := statusArg.(types.DeviceNetworkStatus)
	ctx := ctxArg.(*clientContext)
	if key != "global" {
		log.Infof("handleDNSModify: ignoring %s\n", key)
		return
	}
	log.Infof("handleDNSModify for %s\n", key)
	if cmp.Equal(ctx.deviceNetworkStatus, status) {
		log.Infof("handleDNSModify no change\n")
		return
	}

	log.Infof("handleDNSModify: changed %v",
		cmp.Diff(ctx.deviceNetworkStatus, status))
	*ctx.deviceNetworkStatus = status
	newAddrCount := types.CountLocalAddrAnyNoLinkLocal(*ctx.deviceNetworkStatus)
	if newAddrCount != ctx.usableAddressCount {
		log.Infof("DeviceNetworkStatus from %d to %d addresses\n",
			ctx.usableAddressCount, newAddrCount)
		// ledmanager subscribes to DeviceNetworkStatus to see changes
		ctx.usableAddressCount = newAddrCount
	}

	// update proxy certs if configured
	if ctx.zedcloudCtx != nil && ctx.zedcloudCtx.V2API {
		zedcloud.UpdateTLSProxyCerts(ctx.zedcloudCtx)
	}
	log.Infof("handleDNSModify done for %s\n", key)
}

func handleDNSDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	log.Infof("handleDNSDelete for %s\n", key)
	ctx := ctxArg.(*clientContext)

	if key != "global" {
		log.Infof("handleDNSDelete: ignoring %s\n", key)
		return
	}
	*ctx.deviceNetworkStatus = types.DeviceNetworkStatus{}
	newAddrCount := types.CountLocalAddrAnyNoLinkLocal(*ctx.deviceNetworkStatus)
	ctx.usableAddressCount = newAddrCount
	log.Infof("handleDNSDelete done for %s\n", key)
}
