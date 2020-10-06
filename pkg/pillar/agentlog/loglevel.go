// Copyright (c) 2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

// Handle logLevel and remoteLogLevel for agents.

package agentlog

import (
	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/sirupsen/logrus"
)

func GetGlobalConfig(log *base.LogObject, sub pubsub.Subscription) *types.ConfigItemValueMap {
	m, err := sub.Get("global")
	if err != nil {
		log.Errorf("GlobalConfig - Failed to get key global. err: %s", err)
		return nil
	}
	gc := m.(types.ConfigItemValueMap)
	return &gc
}

// Returns (value, ok)
func GetLogLevel(log *base.LogObject, sub pubsub.Subscription, agentName string) (string, bool) {
	val, ok, _ := getLogLevelImpl(log, sub, agentName, true)
	return val, ok
}

func GetLogLevelNoDefault(log *base.LogObject, sub pubsub.Subscription, agentName string) (string, bool) {
	val, ok, _ := getLogLevelImpl(log, sub, agentName, false)
	return val, ok
}

// Returns a level string, true if found, and true if it was from the default
func getLogLevelImpl(log *base.LogObject, sub pubsub.Subscription, agentName string,
	allowDefault bool) (string, bool, bool) {

	m, err := sub.Get("global")
	if err != nil {
		log.Errorf("GetLogLevel- failed to get global. Err: %s", err)
		return "", false, false
	}
	gc := m.(types.ConfigItemValueMap)
	// Do we have an entry for this agent?
	loglevel := gc.AgentSettingStringValue(agentName, types.LogLevel)
	if loglevel != "" {
		log.Debugf("getLogLevelImpl: loglevel=%s", loglevel)
		return loglevel, true, false
	}
	if !allowDefault {
		log.Debugf("getLogLevelImpl: loglevel not found. allowDefault False")
		return "", false, false
	}
	// Agent specific setting  not available. Get it from Global Setting
	loglevel = gc.GlobalValueString(types.DefaultLogLevel)
	if loglevel != "" {
		log.Debugf("getLogLevelImpl: returning DefaultLogLevel (%s)", loglevel)
		return loglevel, true, true
	}
	log.Errorf("***getLogLevelImpl: DefaultLogLevel not found. returning info")
	return "info", false, false
}

// Returns (value, ok)
func GetRemoteLogLevel(log *base.LogObject, sub pubsub.Subscription, agentName string) (string, bool) {
	return getRemoteLogLevelImpl(log, sub, agentName, true)
}

func GetRemoteLogLevelNoDefault(log *base.LogObject, sub pubsub.Subscription, agentName string) (string, bool) {
	return getRemoteLogLevelImpl(log, sub, agentName, false)
}

func getRemoteLogLevelImpl(log *base.LogObject, sub pubsub.Subscription, agentName string,
	allowDefault bool) (string, bool) {

	m, err := sub.Get("global")
	if err != nil {
		log.Errorf("GetRemoteLogLevel failed %s\n", err)
		return "", false
	}
	gc := m.(types.ConfigItemValueMap)
	// Do we have an entry for this agent?
	loglevel := gc.AgentSettingStringValue(agentName, types.RemoteLogLevel)
	if loglevel != "" {
		log.Debugf("getRemoteLogLevelImpl: loglevel=%s", loglevel)
		return loglevel, true
	}
	if !allowDefault {
		log.Debugf("getRemoteLogLevelImpl: loglevel not found. allowDefault False")
		return "", false
	}
	// Agent specific setting  not available. Get it from Global Setting
	loglevel = gc.GlobalValueString(types.DefaultRemoteLogLevel)
	if loglevel != "" {
		log.Debugf("getRemoteLogLevelImpl: returning DefaultRemoteLogLevel (%s)",
			loglevel)
		return loglevel, true
	}
	log.Errorf("***getRemoteLogLevelImpl: DefaultRemoteLogLevel not found. " +
		"returning info")
	return "info", false
}

func LogLevel(gc *types.ConfigItemValueMap, agentName string) string {

	loglevel := gc.AgentSettingStringValue(agentName, types.LogLevel)
	if loglevel != "" {
		return loglevel
	}
	return ""
}

// HandleGlobalConfig updates the LogLevel setting in the passed in logger
// based on GlobalConfig and debugOverride
// Returns the debug bool
func HandleGlobalConfig(log *base.LogObject, sub pubsub.Subscription, agentName string,
	debugOverride bool, logger *logrus.Logger) (bool, *types.ConfigItemValueMap) {

	log.Infof("HandleGlobalConfig(%s, %v)\n", agentName, debugOverride)
	return handleGlobalConfigImpl(log, sub, agentName, debugOverride, true,
		logger)
}

// HandleGlobalConfigNoDefault updates the LogLevel setting in the passed in logger
// based on GlobalConfig and debugOverride
// Returns the debug bool
// Unlike HandleGlobalConfig it does not look at debug.default.loglevel
// XXX Only used by logmanager since enable debug for it slows it down too much
func HandleGlobalConfigNoDefault(log *base.LogObject, sub pubsub.Subscription, agentName string,
	debugOverride bool, logger *logrus.Logger) (bool, *types.ConfigItemValueMap) {

	log.Infof("HandleGlobalConfig(%s, %v)\n", agentName, debugOverride)
	return handleGlobalConfigImpl(log, sub, agentName, debugOverride, false,
		logger)
}

func handleGlobalConfigImpl(log *base.LogObject, sub pubsub.Subscription, agentName string,
	debugOverride bool, allowDefault bool, logger *logrus.Logger) (bool, *types.ConfigItemValueMap) {
	level := logrus.InfoLevel
	debug := false
	gcp := GetGlobalConfig(log, sub)
	log.Infof("handleGlobalConfigImpl: gcp %+v\n", gcp)
	if debugOverride {
		debug = true
		level = logrus.TraceLevel
		log.Infof("handleGlobalConfigImpl: debugOverride set. set loglevel to debug")
	} else if loglevel, ok, def := getLogLevelImpl(log, sub, agentName, allowDefault); ok {
		l, err := logrus.ParseLevel(loglevel)
		if err != nil {
			log.Errorf("***ParseLevel %s failed: %s\n", loglevel, err)
		} else {
			level = l
			log.Infof("handleGlobalConfigModify: level %v\n",
				level)
		}
		if level == logrus.TraceLevel {
			debug = true
		}
		if def {
			// XXX hack to set default logger
			logrus.SetLevel(level)
		}
	} else {
		log.Errorf("***handleGlobalConfigImpl: Failed to get loglevel")
	}
	log.Infof("handleGlobalConfigImpl: Setting loglevel to %s", level)
	logger.SetLevel(level)
	return debug, gcp
}
