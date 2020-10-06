// Copyright (c) 2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package zedagent

// all things related to running remote attestation with the Controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	eventlog "github.com/cshari-zededa/eve-tpm2-tools/eventlog"
	"github.com/golang/protobuf/proto"
	"github.com/lf-edge/eve/api/go/attest"
	"github.com/lf-edge/eve/pkg/pillar/agentlog"
	zattest "github.com/lf-edge/eve/pkg/pillar/attest"
	"github.com/lf-edge/eve/pkg/pillar/hardware"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
	"io/ioutil"
	"net/http"
	"reflect"
	"strings"
)

//TpmAgentImpl implements zattest.TpmAgent interface
type TpmAgentImpl struct{}

//VerifierImpl implements zattest.Verifier interface
type VerifierImpl struct{}

//WatchdogImpl implements zattest.Watchdog interface
type WatchdogImpl struct{}

// Attest Information Context
type attestContext struct {
	zedagentCtx                   *zedagentContext
	attestFsmCtx                  *zattest.Context
	pubAttestNonce                pubsub.Publication
	pubEncryptedKeyFromController pubsub.Publication
	//Nonce for the current attestation cycle
	Nonce []byte
	//Quote for the current attestation cycle
	InternalQuote *types.AttestQuote
	//Data to be escrowed with Controller
	EscrowData []byte
	//Iteration keeps track of retry count
	Iteration int
	//EventLogEntries are the TPM EventLog entries
	EventLogEntries []eventlog.Event
	//EventLogParseErr stores any error that happened during EventLog parsing
	EventLogParseErr error
}

const (
	watchdogInterval  = 15
	retryTimeInterval = 15
	//EventLogPath is the TPM measurement log aka TPM event log
	EventLogPath = "/sys/kernel/security/tpm0/binary_bios_measurements"
)

//One shot send, if fails, return an error to the state machine to retry later
func trySendToController(attestReq *attest.ZAttestReq, iteration int) (*http.Response, []byte, types.SenderResult, error) {
	data, err := proto.Marshal(attestReq)
	if err != nil {
		log.Fatal("SendInfoProtobufStr proto marshaling error: ", err)
	}

	buf := bytes.NewBuffer(data)
	size := int64(proto.Size(attestReq))
	attestURL := zedcloud.URLPathString(serverNameAndPort, zedcloudCtx.V2API,
		devUUID, "attest")
	return zedcloud.SendOnAllIntf(zedcloudCtx, attestURL,
		size, buf, iteration, true)
}

//SendNonceRequest implements SendNonceRequest method of zattest.Verifier
func (server *VerifierImpl) SendNonceRequest(ctx *zattest.Context) error {
	if ctx.OpaqueCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to OpaqueCtx")
	}
	attestCtx, ok := ctx.OpaqueCtx.(*attestContext)
	if !ok {
		log.Fatalf("[ATTEST] Unexpected type from opaque ctx: %T",
			ctx.OpaqueCtx)
	}
	if len(attestCtx.Nonce) > 0 {
		//Clear existing nonce before attempting another nonce request
		unpublishAttestNonce(attestCtx)
		attestCtx.Nonce = nil
	}
	var attestReq = &attest.ZAttestReq{}

	// bail if V2API is not supported
	if !zedcloud.UseV2API() {
		return zattest.ErrNoVerifier
	}

	attestReq.ReqType = attest.ZAttestReqType_ATTEST_REQ_NONCE

	//Increment Iteration for interface rotation
	attestCtx.Iteration++
	log.Debugf("Sending Nonce request %v", attestReq)

	_, contents, senderStatus, err := trySendToController(attestReq, attestCtx.Iteration)
	if err != nil || senderStatus != types.SenderStatusNone {
		log.Errorf("[ATTEST] Error %v, senderStatus %v",
			err, senderStatus)
		return zattest.ErrControllerReqFailed
	}

	attestResp := &attest.ZAttestResponse{}
	if err := proto.Unmarshal(contents, attestResp); err != nil {
		log.Errorf("[ATTEST] Error %v in Unmarshaling nonce response", err)
		return zattest.ErrControllerReqFailed
	}

	respType := attestResp.GetRespType()
	if respType != attest.ZAttestRespType_ATTEST_RESP_NONCE {
		log.Errorf("[ATTEST] Got %v, but want %v",
			respType, attest.ZAttestRespType_ATTEST_RESP_NONCE)
		return zattest.ErrControllerReqFailed
	}

	if nonceResp := attestResp.GetNonce(); nonceResp == nil {
		log.Errorf("[ATTEST] Got empty nonce response")
		return zattest.ErrControllerReqFailed
	} else {
		attestCtx.Nonce = nonceResp.GetNonce()
	}

	return nil
}

func combineBiosFields(biosVendor, biosVersion, biosReleaseDate string) string {
	biosStr := ""
	if biosVendor != "" {
		biosStr = biosVendor
	}
	if biosVersion != "" {
		if biosStr != "" {
			biosStr = biosStr + "-" + biosVersion
		} else {
			biosStr = biosVersion
		}

	}
	if biosReleaseDate != "" {
		if biosStr != "" {
			biosStr = biosStr + "-" + biosReleaseDate
		} else {
			biosStr = biosReleaseDate
		}
	}
	return biosStr
}

//encodeVersions fetches EVE, UEFI versions
func encodeVersions(quoteMsg *attest.ZAttestQuote) error {
	quoteMsg.Versions = make([]*attest.AttestVersionInfo, 0)
	eveVersion := new(attest.AttestVersionInfo)
	eveVersion.VersionType = attest.AttestVersionType_ATTEST_VERSION_TYPE_EVE
	eveRelease, err := ioutil.ReadFile(types.EveVersionFile)
	if err != nil {
		return err
	}
	eveVersion.Version = strings.TrimSpace(string(eveRelease))
	quoteMsg.Versions = append(quoteMsg.Versions, eveVersion)

	//GetDeviceBios returns empty values on ARM64, check for them
	bVendor, bVersion, bReleaseDate := hardware.GetDeviceBios(log)
	biosVendor := strings.TrimSpace(bVendor)
	biosVersion := strings.TrimSpace(bVersion)
	biosReleaseDate := strings.TrimSpace(bReleaseDate)
	biosStr := combineBiosFields(biosVendor, biosVersion, biosReleaseDate)
	if biosStr != "" {
		uefiVersion := new(attest.AttestVersionInfo)
		uefiVersion.VersionType = attest.AttestVersionType_ATTEST_VERSION_TYPE_FIRMWARE
		uefiVersion.Version = biosStr
		quoteMsg.Versions = append(quoteMsg.Versions, uefiVersion)
		log.Infof("quoteMsg.Versions %s %s", eveVersion.Version, uefiVersion.Version)
	}
	return nil
}

//encodePCRValues encodes PCR values from types.AttestQuote into attest.ZAttestQuote
func encodePCRValues(internalQuote *types.AttestQuote, quoteMsg *attest.ZAttestQuote) error {
	quoteMsg.PcrValues = make([]*attest.TpmPCRValue, 0)
	for _, pcr := range internalQuote.PCRs {
		pcrValue := new(attest.TpmPCRValue)
		pcrValue.Index = uint32(pcr.Index)
		switch pcr.Algo {
		case types.PCRExtendHashAlgoSha1:
			pcrValue.HashAlgo = attest.TpmHashAlgo_TPM_HASH_ALGO_SHA1
		case types.PCRExtendHashAlgoSha256:
			pcrValue.HashAlgo = attest.TpmHashAlgo_TPM_HASH_ALGO_SHA256
		default:
			return fmt.Errorf("Unknown Hash Algo in PCR Digest %d",
				pcr.Index)
		}
		pcrValue.Value = pcr.Digest
		quoteMsg.PcrValues = append(quoteMsg.PcrValues, pcrValue)
	}
	//XXX Check for TPM platform, and if so, insist on non-empty quoteMsg.PCRValues
	return nil
}

//SendAttestQuote implements SendAttestQuote method of zattest.Verifier
func (server *VerifierImpl) SendAttestQuote(ctx *zattest.Context) error {
	if ctx.OpaqueCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to OpaqueCtx")
	}
	attestCtx, ok := ctx.OpaqueCtx.(*attestContext)
	if !ok {
		log.Fatalf("[ATTEST] Unexpected type from opaque ctx: %T",
			ctx.OpaqueCtx)
	}
	var attestReq = &attest.ZAttestReq{}

	// bail if V2API is not supported
	if !zedcloud.UseV2API() {
		return zattest.ErrNoVerifier
	}

	attestReq.ReqType = attest.ZAttestReqType_ATTEST_REQ_QUOTE
	//XXX Fill GPS info, Version, Eventlog fields later
	quote := &attest.ZAttestQuote{
		AttestData: attestCtx.InternalQuote.Quote,
		Signature:  attestCtx.InternalQuote.Signature,
	}

	if err := encodePCRValues(attestCtx.InternalQuote, quote); err != nil {
		log.Errorf("[ATTEST] encodePCRValues failed with err %v", err)
		return zattest.ErrControllerReqFailed
	}

	if err := encodeVersions(quote); err != nil {
		log.Errorf("[ATTEST] encodeVersions failed with err %v", err)
		return zattest.ErrControllerReqFailed
	}

	if attestCtx.EventLogParseErr == nil {
		//On some platforms, either the kernel does not export TPM Eventlog
		//or the TPM does not have SHA256 bank enabled for PCRs. We populate
		//eventlog if we are able to parse eventlog successfully
		encodeEventLog(attestCtx, quote)
	}

	attestReq.Quote = quote

	//Increment Iteration for interface rotation
	attestCtx.Iteration++
	log.Debugf("Sending Quote request")

	_, contents, senderStatus, err := trySendToController(attestReq, attestCtx.Iteration)
	if err != nil || senderStatus != types.SenderStatusNone {
		log.Errorf("[ATTEST] Error %v, senderStatus %v",
			err, senderStatus)
		return zattest.ErrControllerReqFailed
	}

	attestResp := &attest.ZAttestResponse{}
	if err := proto.Unmarshal(contents, attestResp); err != nil {
		log.Errorf("[ATTEST] Error %v in Unmarshaling quote response", err)
		return zattest.ErrControllerReqFailed
	}

	respType := attestResp.GetRespType()
	if respType != attest.ZAttestRespType_ATTEST_RESP_QUOTE_RESP {
		log.Errorf("[ATTEST] Got %v, but want %v",
			respType, attest.ZAttestRespType_ATTEST_RESP_QUOTE_RESP)
		return zattest.ErrControllerReqFailed
	}

	var quoteResp *attest.ZAttestQuoteResp
	if quoteResp = attestResp.GetQuoteResp(); quoteResp == nil {
		log.Errorf("[ATTEST] Got empty quote response")
		return zattest.ErrControllerReqFailed
	}
	quoteRespCode := quoteResp.GetResponse()
	switch quoteRespCode {
	case attest.ZAttestResponseCode_Z_ATTEST_RESPONSE_CODE_INVALID:
		log.Errorf("[ATTEST] Invalid response code")
		return zattest.ErrControllerReqFailed
	case attest.ZAttestResponseCode_Z_ATTEST_RESPONSE_CODE_SUCCESS:
		//Retrieve integrity token
		storeIntegrityToken(quoteResp.GetIntegrityToken())
		log.Notice("[ATTEST] Attestation successful, processing keys given by Controller")
		if encryptedKeys := quoteResp.GetKeys(); encryptedKeys != nil {
			for _, sk := range encryptedKeys {
				encryptedKeyType := sk.GetKeyType()
				encryptedKey := sk.GetKey()
				if encryptedKeyType == attest.AttestVolumeKeyType_ATTEST_VOLUME_KEY_TYPE_VSK {
					publishEncryptedKeyFromController(attestCtx, encryptedKey)
					log.Infof("[ATTEST] published Controller-given encrypted key")
				}
			}
		}
		return nil
	case attest.ZAttestResponseCode_Z_ATTEST_RESPONSE_CODE_NONCE_MISMATCH:
		log.Errorf("[ATTEST] Nonce Mismatch")
		return zattest.ErrNonceMismatch
	case attest.ZAttestResponseCode_Z_ATTEST_RESPONSE_CODE_NO_CERT_FOUND:
		log.Errorf("[ATTEST] Controller yet to receive signing cert")
		return zattest.ErrNoCertYet
	case attest.ZAttestResponseCode_Z_ATTEST_RESPONSE_CODE_QUOTE_FAILED:
		log.Errorf("[ATTEST] Quote Mismatch")
		return zattest.ErrQuoteMismatch
	default:
		log.Errorf("[ATTEST] Unknown quoteRespCode %v", quoteRespCode)
		return zattest.ErrControllerReqFailed
	}
}

//SendAttestEscrow implements SendAttestEscrow method of zattest.Verifier
func (server *VerifierImpl) SendAttestEscrow(ctx *zattest.Context) error {
	if ctx.OpaqueCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to OpaqueCtx")
	}
	attestCtx, ok := ctx.OpaqueCtx.(*attestContext)
	if !ok {
		log.Fatalf("[ATTEST] Unexpected type from opaque ctx: %T",
			ctx.OpaqueCtx)
	}
	// bail if V2API is not supported
	if !zedcloud.UseV2API() {
		return zattest.ErrNoVerifier
	}
	if attestCtx.EscrowData == nil {
		return zattest.ErrNoEscrowData
	}

	escrowMsg := &attest.AttestStorageKeys{}
	escrowMsg.Keys = make([]*attest.AttestVolumeKey, 0)
	key := new(attest.AttestVolumeKey)
	key.KeyType = attest.AttestVolumeKeyType_ATTEST_VOLUME_KEY_TYPE_VSK
	key.Key = attestCtx.EscrowData
	escrowMsg.Keys = append(escrowMsg.Keys, key)
	if b, err := readIntegrityToken(); err == nil {
		escrowMsg.IntegrityToken = b
	}
	var attestReq = &attest.ZAttestReq{}
	attestReq.ReqType = attest.ZAttestReqType_Z_ATTEST_REQ_TYPE_STORE_KEYS
	attestReq.StorageKeys = escrowMsg

	//Increment Iteration for interface rotation
	attestCtx.Iteration++
	log.Debugf("Sending Escrow data")

	_, contents, senderStatus, err := trySendToController(attestReq, attestCtx.Iteration)
	if err != nil || senderStatus != types.SenderStatusNone {
		log.Errorf("[ATTEST] Error %v, senderStatus %v",
			err, senderStatus)
		return zattest.ErrControllerReqFailed
	}
	attestResp := &attest.ZAttestResponse{}
	if err := proto.Unmarshal(contents, attestResp); err != nil {
		log.Errorf("[ATTEST] Error %v in Unmarshaling storage keys response", err)
		return zattest.ErrControllerReqFailed
	}

	respType := attestResp.GetRespType()
	if respType != attest.ZAttestRespType_Z_ATTEST_RESP_TYPE_STORE_KEYS {
		log.Errorf("[ATTEST] Got %v, but want %v",
			respType, attest.ZAttestRespType_Z_ATTEST_RESP_TYPE_STORE_KEYS)
		return zattest.ErrControllerReqFailed
	}

	var escrowResp *attest.AttestStorageKeysResp
	if escrowResp = attestResp.GetStorageKeysResp(); escrowResp == nil {
		log.Errorf("[ATTEST] Got empty storage keys response")
		return zattest.ErrControllerReqFailed
	}
	escrowRespCode := escrowResp.GetResponse()
	switch escrowRespCode {
	case attest.AttestStorageKeysResponseCode_ATTEST_STORAGE_KEYS_RESPONSE_CODE_INVALID:
		log.Errorf("[ATTEST] Invalid response code")
		return zattest.ErrControllerReqFailed
	case attest.AttestStorageKeysResponseCode_ATTEST_STORAGE_KEYS_RESPONSE_CODE_ITOKEN_MISMATCH:
		log.Errorf("[ATTEST] Integrity Token Mismatch")
		return zattest.ErrITokenMismatch
	case attest.AttestStorageKeysResponseCode_ATTEST_STORAGE_KEYS_RESPONSE_CODE_SUCCESS:
		log.Notice("[ATTEST] Escrow successful")
		return nil
	default:
		log.Errorf("[ATTEST] Unknown escrowRespCode %v", escrowRespCode)
		return zattest.ErrControllerReqFailed
	}
}

//SendInternalQuoteRequest implements SendInternalQuoteRequest method of zattest.TpmAgent
func (agent *TpmAgentImpl) SendInternalQuoteRequest(ctx *zattest.Context) error {
	if ctx.OpaqueCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to OpaqueCtx")
	}
	attestCtx, ok := ctx.OpaqueCtx.(*attestContext)
	if !ok {
		log.Fatalf("[ATTEST] Unexpected type from opaque ctx: %T",
			ctx.OpaqueCtx)
	}

	//Clear existing quote before requesting a new one
	if attestCtx.InternalQuote != nil {
		log.Infof("[ATTEST] Clearing current quote, before requesting a new one")
		attestCtx.InternalQuote = nil
	}
	publishAttestNonce(attestCtx)
	return nil
}

//PunchWatchdog implements PunchWatchdog method of zattest.Watchdog
func (wd *WatchdogImpl) PunchWatchdog(ctx *zattest.Context) error {
	log.Debug("[ATTEST] Punching watchdog")
	ctx.PubSub.StillRunning(agentName+"attest", warningTime, errorTime)
	return nil
}

//parseTpmEventLog parses TPM Event Log and stores it given attestContext
//any error during parsing is stored in EventLogParseErr
func parseTpmEventLog(attestCtx *attestContext) {
	events, err := eventlog.ParseEvents(EventLogPath)
	attestCtx.EventLogEntries = events
	attestCtx.EventLogParseErr = err
	if err != nil {
		log.Errorf("[ATTEST] Eventlog parsing error %v", err)
	}
}

func encodeEventLog(attestCtx *attestContext, quoteMsg *attest.ZAttestQuote) error {
	quoteMsg.EventLog = make([]*attest.TpmEventLogEntry, 0)
	for _, event := range attestCtx.EventLogEntries {
		tpmEventLog := new(attest.TpmEventLogEntry)
		tpmEventLog.Index = uint32(event.Sequence)
		tpmEventLog.PcrIndex = uint32(event.Index)
		tpmEventLog.Digest = new(attest.TpmEventDigest)
		tpmEventLog.Digest.HashAlgo = attest.TpmHashAlgo_TPM_HASH_ALGO_SHA256
		tpmEventLog.Digest.Digest = event.Sha256Digest()
		tpmEventLog.EventDataBinary = event.Data

		//Populate EventDataString for PCRs 8 and 9
		//they are human readable
		if event.Index == 8 || event.Index == 9 {
			tpmEventLog.EventDataString = string(event.Data)
		} else {
			tpmEventLog.EventDataString = "Not Applicable"
		}
		quoteMsg.EventLog = append(quoteMsg.EventLog, tpmEventLog)
	}
	return nil
}

// initialize attest pubsub trigger handlers and channels
func attestModuleInitialize(ctx *zedagentContext, ps *pubsub.PubSub) error {
	zattest.RegisterExternalIntf(&TpmAgentImpl{}, &VerifierImpl{}, &WatchdogImpl{})

	if ctx.attestCtx == nil {
		ctx.attestCtx = &attestContext{}
	}

	c, err := zattest.New(ctx.ps, log, retryTimeInterval, watchdogInterval, ctx.attestCtx)
	if err != nil {
		log.Errorf("[ATTEST] Error %v while initializing attestation FSM", err)
		return err
	}
	ctx.attestCtx.attestFsmCtx = c
	pubAttestNonce, err := ps.NewPublication(
		pubsub.PublicationOptions{
			AgentName: agentName,
			TopicType: types.AttestNonce{},
		})
	if err != nil {
		log.Fatal(err)
	}
	ctx.attestCtx.pubAttestNonce = pubAttestNonce
	pubEncryptedKeyFromController, err := ps.NewPublication(
		pubsub.PublicationOptions{
			AgentName: agentName,
			TopicType: types.EncryptedVaultKeyFromController{},
		})
	if err != nil {
		log.Fatal(err)
	}
	ctx.attestCtx.pubEncryptedKeyFromController = pubEncryptedKeyFromController
	parseTpmEventLog(ctx.attestCtx)
	return nil
}

// start the task threads
func attestModuleStart(ctx *zedagentContext) error {
	log.Info("[ATTEST] Starting attestation task")
	if ctx.attestCtx == nil {
		return fmt.Errorf("No attest module context")
	}
	if ctx.attestCtx.attestFsmCtx == nil {
		return fmt.Errorf("No state machine context found")
	}
	log.Infof("Creating %s at %s", "attestFsmCtx.EnterEventLoop",
		agentlog.GetMyStack())
	go ctx.attestCtx.attestFsmCtx.EnterEventLoop()
	zattest.Kickstart(ctx.attestCtx.attestFsmCtx)
	return nil
}

// pubsub functions
func handleAttestQuoteModify(ctxArg interface{}, key string, quoteArg interface{}) {

	//Store quote received in state machine
	ctx, ok := ctxArg.(*zedagentContext)
	if !ok {
		log.Fatalf("[ATTEST] Unexpected ctx type %T", ctxArg)
	}

	quote, ok := quoteArg.(types.AttestQuote)
	if !ok {
		log.Fatalf("[ATTEST] Unexpected pub type %T", quoteArg)
	}

	if ctx.attestCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to attestCtx")
	}

	//Deepcopy quote into InternalQuote
	attestCtx := ctx.attestCtx
	attestCtx.InternalQuote = &types.AttestQuote{}
	buf, _ := json.Marshal(&quote)
	json.Unmarshal(buf, attestCtx.InternalQuote)

	if attestCtx.attestFsmCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to attestFsmCtx")
	}
	//Trigger event on the state machine
	zattest.InternalQuoteRecvd(attestCtx.attestFsmCtx)

	log.Infof("handleAttestQuoteModify done for %s", quote.Key())
	return
}

func handleAttestQuoteDelete(ctxArg interface{}, key string, quoteArg interface{}) {
	//Delete quote received in state machine

	ctx, ok := ctxArg.(*zedagentContext)
	if !ok {
		log.Fatalf("[ATTEST] Unexpected ctx type %T", ctxArg)
	}

	quote, ok := quoteArg.(types.AttestQuote)
	if !ok {
		log.Fatalf("[ATTEST] Unexpected pub type %T", quoteArg)
	}

	if ctx.attestCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to attestCtx")
	}

	attestCtx := ctx.attestCtx
	if attestCtx.InternalQuote == nil {
		log.Warnf("[ATTEST] Delete received while InternalQuote is unpopulated, ignoring")
		return
	}

	if attestCtx.attestFsmCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to attestFsmCtx")
	}

	if reflect.DeepEqual(quote.Nonce, attestCtx.InternalQuote.Nonce) {
		attestCtx.InternalQuote = nil
	} else {
		log.Warnf("[ATTEST] Nonce didn't match, ignoring incoming delete")
	}
	log.Infof("handleAttestQuoteDelete done for %s", quote.Key())
	return
}

func handleEncryptedKeyFromDeviceModify(ctxArg interface{}, key string, vaultKeyArg interface{}) {

	//Store quote received in state machine
	ctx, ok := ctxArg.(*zedagentContext)
	if !ok {
		log.Fatalf("[ATTEST] Unexpected ctx type %T", ctxArg)
	}

	vaultKey, ok := vaultKeyArg.(types.EncryptedVaultKeyFromDevice)
	if !ok {
		log.Fatalf("[ATTEST] Unexpected pub type %T", vaultKeyArg)
	}

	if ctx.attestCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to attestCtx")
	}

	if vaultKey.Name != types.DefaultVaultName {
		log.Warnf("Ignoring unknown vault %s", vaultKey.Name)
		return
	}
	attestCtx := ctx.attestCtx
	attestCtx.EscrowData = vaultKey.EncryptedVaultKey

	if attestCtx.attestFsmCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to attestFsmCtx")
	}
	//Trigger event on the state machine
	zattest.InternalEscrowDataRecvd(attestCtx.attestFsmCtx)
}

func publishAttestNonce(ctx *attestContext) {
	nonce := types.AttestNonce{
		Nonce:     ctx.Nonce,
		Requester: agentName,
	}
	key := nonce.Key()
	log.Debugf("[ATTEST] publishAttestNonce %s", key)
	pub := ctx.pubAttestNonce
	pub.Publish(key, nonce)
	log.Debugf("[ATTEST] publishAttestNonce done for %s", key)
}

func publishEncryptedKeyFromController(ctx *attestContext, encryptedVaultKey []byte) {
	sK := types.EncryptedVaultKeyFromController{
		Name:              types.DefaultVaultName,
		EncryptedVaultKey: encryptedVaultKey,
	}
	key := sK.Key()
	log.Debugf("[ATTEST] publishEncryptedKeyFromController %s", key)
	pub := ctx.pubEncryptedKeyFromController
	pub.Publish(key, sK)
	log.Debugf("[ATTEST] publishEncryptedKeyFromController done for %s", key)
}

func unpublishAttestNonce(ctx *attestContext) {
	nonce := types.AttestNonce{
		Nonce:     ctx.Nonce,
		Requester: agentName,
	}
	pub := ctx.pubAttestNonce
	key := nonce.Key()
	c, _ := pub.Get(key)
	if c == nil {
		log.Errorf("[ATTEST] unpublishAttestNonce(%s) not found", key)
		return
	}
	pub.Unpublish(key)
	items := pub.GetAll()
	if len(items) > 0 {
		for _, item := range items {
			nonce := item.(types.AttestNonce)
			log.Errorf("[ATTEST] Stale nonce item found, %s", nonce.Key())
		}
		log.Fatal("[ATTEST] Stale nonce items found after unpublishing")
	}
	log.Debugf("[ATTEST] unpublishAttestNonce done for %s", key)
}

//helper to set IntegrityToken
func storeIntegrityToken(token []byte) {
	if len(token) == 0 {
		log.Warnf("[ATTEST] Received empty integrity token")
	}
	err := ioutil.WriteFile(types.ITokenFile, token, 644)
	if err != nil {
		log.Fatalf("Failed to store integrity token, err: %v", err)
	}
}

//helper to get IntegrityToken
func readIntegrityToken() ([]byte, error) {
	return ioutil.ReadFile(types.ITokenFile)
}

//trigger restart event in attesation FSM
func restartAttestation(zedagentCtx *zedagentContext) error {
	if zedagentCtx.attestCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to attestCtx")
	}
	attestCtx := zedagentCtx.attestCtx
	if attestCtx.attestFsmCtx == nil {
		log.Fatalf("[ATTEST] Uninitialized access to attestFsmCtx")
	}
	//Trigger event on the state machine
	zattest.RestartAttestation(attestCtx.attestFsmCtx)
	return nil
}
