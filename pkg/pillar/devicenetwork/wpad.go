// Copyright (c) 2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package devicenetwork

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
	"mime"
	"strings"
)

// Download a wpad file if so configured
func CheckAndGetNetworkProxy(log *base.LogObject, deviceNetworkStatus *types.DeviceNetworkStatus,
	status *types.NetworkPortStatus) error {

	ifname := status.IfName
	proxyConfig := &status.ProxyConfig

	log.Debugf("CheckAndGetNetworkProxy(%s): enable %v, url %s\n",
		ifname, proxyConfig.NetworkProxyEnable,
		proxyConfig.NetworkProxyURL)

	if proxyConfig.Pacfile != "" {
		log.Debugf("CheckAndGetNetworkProxy(%s): already have Pacfile\n",
			ifname)
		return nil
	}
	if !proxyConfig.NetworkProxyEnable {
		log.Debugf("CheckAndGetNetworkProxy(%s): not enabled\n",
			ifname)
		return nil
	}
	if proxyConfig.NetworkProxyURL != "" {
		pac, err := getPacFile(log, deviceNetworkStatus,
			proxyConfig.NetworkProxyURL, ifname)
		if err != nil {
			errStr := fmt.Sprintf("Failed to fetch %s for %s: %s",
				proxyConfig.NetworkProxyURL, ifname, err)
			log.Errorln(errStr)
			return errors.New(errStr)
		}
		proxyConfig.Pacfile = pac
		return nil
	}
	dn := status.DomainName
	if dn == "" {
		errStr := fmt.Sprintf("NetworkProxyEnable for %s but neither a NetworkProxyURL nor a DomainName",
			ifname)
		log.Errorln(errStr)
		return errors.New(errStr)
	}
	log.Infof("CheckAndGetNetworkProxy(%s): DomainName %s\n",
		ifname, dn)
	// Try http://wpad.%s/wpad.dat", dn where we the leading labels
	// in DomainName until we succeed
	for {
		url := fmt.Sprintf("http://wpad.%s/wpad.dat", dn)
		pac, err := getPacFile(log, deviceNetworkStatus, url, ifname)
		if err == nil {
			proxyConfig.Pacfile = pac
			proxyConfig.WpadURL = url
			return nil
		}
		errStr := fmt.Sprintf("Failed to fetch %s for %s: %s",
			url, ifname, err)
		log.Warnln(errStr)
		i := strings.Index(dn, ".")
		if i == -1 {
			log.Infof("CheckAndGetNetworkProxy(%s): no dots in DomainName %s\n",
				ifname, dn)
			log.Errorln(errStr)
			return errors.New(errStr)
		}
		b := []byte(dn)
		dn = string(b[i+1:])
		// How many dots left? End when we have a TLD i.e., no dots
		// since wpad.com isn't a useful place to look
		count := strings.Count(dn, ".")
		if count == 0 {
			log.Infof("CheckAndGetNetworkProxy(%s): reached TLD in DomainName %s\n",
				ifname, dn)
			log.Errorln(errStr)
			return errors.New(errStr)
		}
	}
}

func getPacFile(log *base.LogObject, status *types.DeviceNetworkStatus, url string,
	ifname string) (string, error) {

	ctx := zedcloud.NewContext(log, zedcloud.ContextOptions{
		Timeout:       15,
		NeedStatsFunc: true,
		AgentName:     "wpad",
	})
	ctx.DeviceNetworkStatus = status
	// Avoid using a proxy to fetch the wpad.dat; 15 second timeout
	const allowProxy = false
	resp, contents, _, err := zedcloud.SendOnIntf(&ctx, url, ifname, 0, nil,
		allowProxy)
	if err != nil {
		return "", err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		errStr := fmt.Sprintf("%s no content-type\n", url)
		return "", errors.New(errStr)
	}
	mimeType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		errStr := fmt.Sprintf("%s ParseMediaType failed %v\n", url, err)
		return "", errors.New(errStr)
	}
	switch mimeType {
	case "application/x-ns-proxy-autoconfig":
		log.Infof("getPacFile(%s): fetched from URL %s: %s\n",
			ifname, url, string(contents))
		encoded := base64.StdEncoding.EncodeToString(contents)
		return encoded, nil
	default:
		errStr := fmt.Sprintf("Incorrect mime-type %s from %s",
			mimeType, url)
		return "", errors.New(errStr)
	}
}
