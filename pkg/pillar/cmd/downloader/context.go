// Copyright (c) 2019-2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package downloader

import (
	"sync"

	"github.com/lf-edge/eve/pkg/pillar/cipher"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/zedUpload"
)

type downloaderContext struct {
	decryptCipherContext   cipher.DecryptCipherContext
	dCtx                   *zedUpload.DronaCtx
	subDeviceNetworkStatus pubsub.Subscription
	subDownloaderConfig    pubsub.Subscription
	pubDownloaderStatus    pubsub.Publication
	subResolveConfig       pubsub.Subscription
	pubResolveStatus       pubsub.Publication
	pubCipherBlockStatus   pubsub.Publication
	subDatastoreConfig     pubsub.Subscription
	deviceNetworkStatus    types.DeviceNetworkStatus
	globalStatusLock       sync.Mutex
	subGlobalConfig        pubsub.Subscription
	GCInitialized          bool
}

func (ctx *downloaderContext) registerHandlers(ps *pubsub.PubSub) error {
	// Look for controller certs which will be used for decryption
	subControllerCert, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "zedagent",
		MyAgentName: agentName,
		TopicImpl:   types.ControllerCert{},
		Activate:    false,
		Ctx:         ctx,
		WarningTime: warningTime,
		ErrorTime:   errorTime,
		Persistent:  true,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.decryptCipherContext.Log = log
	ctx.decryptCipherContext.SubControllerCert = subControllerCert
	subControllerCert.Activate()

	// Look for edge node certs which will be used for decryption
	subEdgeNodeCert, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "tpmmgr",
		MyAgentName: agentName,
		TopicImpl:   types.EdgeNodeCert{},
		Activate:    false,
		Ctx:         ctx,
		WarningTime: warningTime,
		ErrorTime:   errorTime,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.decryptCipherContext.SubEdgeNodeCert = subEdgeNodeCert
	subEdgeNodeCert.Activate()

	// Look for cipher context which will be used for decryption
	subCipherContext, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   "zedagent",
		MyAgentName: agentName,
		TopicImpl:   types.CipherContext{},
		Activate:    false,
		Ctx:         ctx,
		WarningTime: warningTime,
		ErrorTime:   errorTime,
		Persistent:  true,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx.decryptCipherContext.SubCipherContext = subCipherContext
	subCipherContext.Activate()

	// Look for global config such as log levels
	subGlobalConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "",
		MyAgentName:   agentName,
		CreateHandler: handleGlobalConfigModify,
		ModifyHandler: handleGlobalConfigModify,
		DeleteHandler: handleGlobalConfigDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		TopicImpl:     types.ConfigItemValueMap{},
		Ctx:           ctx,
	})
	if err != nil {
		return err
	}
	ctx.subGlobalConfig = subGlobalConfig
	subGlobalConfig.Activate()

	subDeviceNetworkStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler: handleDNSModify,
		ModifyHandler: handleDNSModify,
		DeleteHandler: handleDNSDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		TopicImpl:     types.DeviceNetworkStatus{},
		Ctx:           ctx,
		AgentName:     "nim",
		MyAgentName:   agentName,
	})
	if err != nil {
		return err
	}
	ctx.subDeviceNetworkStatus = subDeviceNetworkStatus
	subDeviceNetworkStatus.Activate()

	// Look for DatastoreConfig. We should process this
	// before any download config. Without DataStore Config,
	// Image Downloads will run into errors, which requires retries
	subDatastoreConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler: handleDatastoreConfigModify,
		ModifyHandler: handleDatastoreConfigModify,
		DeleteHandler: handleDatastoreConfigDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		AgentName:     "zedagent",
		MyAgentName:   agentName,
		TopicImpl:     types.DatastoreConfig{},
		Ctx:           ctx,
	})
	if err != nil {
		return err
	}
	ctx.subDatastoreConfig = subDatastoreConfig
	subDatastoreConfig.Activate()

	pubCipherBlockStatus, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.CipherBlockStatus{},
	})
	if err != nil {
		return err
	}
	ctx.pubCipherBlockStatus = pubCipherBlockStatus

	// Set up our publications before the subscriptions so ctx is set
	pubDownloaderStatus, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.DownloaderStatus{},
	})
	if err != nil {
		return err
	}
	ctx.pubDownloaderStatus = pubDownloaderStatus

	pubResolveStatus, err := ps.NewPublication(pubsub.PublicationOptions{
		AgentName: agentName,
		TopicType: types.ResolveStatus{},
	})
	if err != nil {
		return err
	}
	ctx.pubResolveStatus = pubResolveStatus

	subDownloaderConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler: handleDownloaderConfigCreate,
		ModifyHandler: handleDownloaderConfigModify,
		DeleteHandler: handleDownloaderConfigDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		AgentName:     "volumemgr",
		MyAgentName:   agentName,
		TopicImpl:     types.DownloaderConfig{},
		Ctx:           ctx,
	})
	if err != nil {
		return err
	}
	ctx.subDownloaderConfig = subDownloaderConfig
	subDownloaderConfig.Activate()

	subResolveConfig, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		CreateHandler: handleResolveModify,
		ModifyHandler: handleResolveModify,
		DeleteHandler: handleResolveDelete,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
		AgentName:     "volumemgr",
		MyAgentName:   agentName,
		TopicImpl:     types.ResolveConfig{},
		Ctx:           ctx,
	})
	if err != nil {
		return err
	}
	ctx.subResolveConfig = subResolveConfig
	subResolveConfig.Activate()

	pubDownloaderStatus.SignalRestarted()
	pubResolveStatus.SignalRestarted()

	return nil
}
