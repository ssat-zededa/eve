// Copyright (c) 2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"time"

	info "github.com/lf-edge/eve/api/go/info"
	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	uuid "github.com/satori/go.uuid"
)

//Context is a helper struct used to pass around in pubsub handlers
type Context struct {
	Initialized bool
}

// WaitForVault waits until it receives a types.VaultStatus msg, for types.DefaultVaultName
// and the status does not indicate any error
func WaitForVault(ps *pubsub.PubSub, log *base.LogObject, agentName string, warningTime, errorTime time.Duration) error {
	// Look for vault status
	Ctx := &Context{}
	subVaultStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "vaultmgr",
		MyAgentName:   agentName,
		TopicImpl:     types.VaultStatus{},
		Activate:      false,
		Ctx:           Ctx,
		CreateHandler: handleVaultStatusModify,
		ModifyHandler: handleVaultStatusModify,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		return err
	}

	subVaultStatus.Activate()

	// Run a periodic timer so we always update StillRunning
	stillRunning := time.NewTicker(25 * time.Second)
	ps.StillRunning(agentName, warningTime, errorTime)

	// Wait for vault to be ready, which might be delayed due to attestation
	for !Ctx.Initialized {
		log.Infof("Waiting for VaultStatus initialized")
		select {
		case change := <-subVaultStatus.MsgChan():
			subVaultStatus.ProcessChange(change)
		case <-stillRunning.C:
		}
		ps.StillRunning(agentName, warningTime, errorTime)
	}
	subVaultStatus.Close()
	stillRunning.Stop()
	return nil
}

func handleVaultStatusModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	ctx := ctxArg.(*Context)
	vault := statusArg.(types.VaultStatus)
	if vault.Name == types.DefaultVaultName && vault.ConversionComplete &&
		vault.Status != info.DataSecAtRestStatus_DATASEC_AT_REST_ERROR {
		ctx.Initialized = true
	}
}

// WaitForOnboarded waits until it receives a types.OnboardStatus msg with
// a non-zero UUID
func WaitForOnboarded(ps *pubsub.PubSub, log *base.LogObject, agentName string, warningTime, errorTime time.Duration) error {
	// Look for vault status
	Ctx := &Context{}
	subOnboardStatus, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:     "zedclient",
		MyAgentName:   agentName,
		TopicImpl:     types.OnboardingStatus{},
		Activate:      true,
		Persistent:    true,
		Ctx:           Ctx,
		CreateHandler: handleOnboardStatusModify,
		ModifyHandler: handleOnboardStatusModify,
		WarningTime:   warningTime,
		ErrorTime:     errorTime,
	})
	if err != nil {
		return err
	}

	// Run a periodic timer so we always update StillRunning
	stillRunning := time.NewTicker(25 * time.Second)
	ps.StillRunning(agentName, warningTime, errorTime)

	// Wait for Onboarding to be done by client
	for !Ctx.Initialized {
		log.Infof("Waiting for OnboardStatus initialized")
		select {
		case change := <-subOnboardStatus.MsgChan():
			subOnboardStatus.ProcessChange(change)
		case <-stillRunning.C:
		}
		ps.StillRunning(agentName, warningTime, errorTime)
	}
	stillRunning.Stop()
	subOnboardStatus.Close()
	return nil
}

// Really a constant
var nilUUID = uuid.UUID{}

// Set Initialized if the UUID is not nil
func handleOnboardStatusModify(ctxArg interface{}, key string, statusArg interface{}) {
	status := statusArg.(types.OnboardingStatus)
	ctx := ctxArg.(*Context)

	if status.DeviceUUID == nilUUID {
		return
	}
	ctx.Initialized = true
}
