// Copyright (c) 2017 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package devicenetwork

import (
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/eriknordmark/ipinfo"
	"github.com/eriknordmark/netlink"
	"github.com/golang/protobuf/proto"
	zconfig "github.com/lf-edge/eve/api/go/config"
	"github.com/lf-edge/eve/pkg/pillar/cmd/tpmmgr"
	"github.com/lf-edge/eve/pkg/pillar/hardware"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"net"
	"os"
	"reflect"
	"strings"
	"time"
)

const (
	apDirname   = "/run/accesspoint"              // For wireless access-point identifiers
	wpaFilename = "/run/wlan/wpa_supplicant.conf" // wifi wpa_supplicant file, currently only support one
	runwlanDir  = "/run/wlan"
	wpaTempname = "wpa_supplicant.temp"
)

func LastResortDevicePortConfig(ctx *DeviceNetworkContext, ports []string) types.DevicePortConfig {

	config := makeDevicePortConfig(ctx, ports, ports)
	// Set to higher than all zero but lower than the hardware model derived one above
	config.TimePriority = time.Unix(0, 0)
	return config
}

func makeDevicePortConfig(ctx *DeviceNetworkContext, ports []string, free []string) types.DevicePortConfig {
	var config types.DevicePortConfig

	config.Version = types.DPCIsMgmt
	config.Ports = make([]types.NetworkPortConfig, len(ports))
	for ix, u := range ports {
		config.Ports[ix].IfName = u
		for _, f := range free {
			if f == u {
				config.Ports[ix].Free = true
				break
			}
		}
		config.Ports[ix].IsMgmt = true
		config.Ports[ix].Name = config.Ports[ix].IfName
		config.Ports[ix].Dhcp = types.DT_CLIENT
		port, err := ctx.DevicePortConfig.GetPortByIfName(u)
		if err == nil {
			config.Ports[ix].WirelessCfg = port.WirelessCfg
		}
	}
	return config
}

func IsProxyConfigEmpty(proxyConfig types.ProxyConfig) bool {
	if len(proxyConfig.Proxies) == 0 &&
		len(proxyConfig.ProxyCertPEM) == 0 &&
		proxyConfig.Exceptions == "" &&
		proxyConfig.Pacfile == "" &&
		proxyConfig.NetworkProxyEnable == false &&
		proxyConfig.NetworkProxyURL == "" {
		return true
	}
	return false
}

// Check if device can talk to outside world via atleast one of the free uplinks
func VerifyDeviceNetworkStatus(status types.DeviceNetworkStatus,
	retryCount int, timeout uint32) (bool, error) {

	log.Infof("VerifyDeviceNetworkStatus() %d\n", retryCount)
	// Check if it is 1970 in which case we declare success since
	// our certificates will not work until NTP has brought the time
	// forward.
	if time.Now().Year() == 1970 {
		log.Infof("VerifyDeviceNetworkStatus skip due to 1970")
		return false, nil
	}

	server, err := ioutil.ReadFile(types.ServerFileName)
	if err != nil {
		log.Fatal(err)
	}
	serverNameAndPort := strings.TrimSpace(string(server))
	serverName := strings.Split(serverNameAndPort, ":")[0]
	testUrl := serverNameAndPort + "/api/v1/edgedevice/ping"

	// XXX set by caller?
	v2api := false
	zedcloudCtx := zedcloud.ZedCloudContext{
		DeviceNetworkStatus: &status,
		NetworkSendTimeout:  timeout,
		V2API:               v2api,
	}

	// Get device serial number
	zedcloudCtx.DevSerial = hardware.GetProductSerial()
	zedcloudCtx.DevSoftSerial = hardware.GetSoftSerial()
	log.Infof("NIM Get Device Serial %s, Soft Serial %s\n", zedcloudCtx.DevSerial,
		zedcloudCtx.DevSoftSerial)

	tlsConfig, err := zedcloud.GetTlsConfig(zedcloudCtx.DeviceNetworkStatus, serverName,
		nil, &zedcloudCtx)
	if err != nil {
		log.Infof("VerifyDeviceNetworkStatus: " +
			"Device certificate not found, looking for Onboarding certificate")

		onboardingCert, err := tls.LoadX509KeyPair(types.OnboardCertName,
			types.OnboardKeyName)
		if err != nil {
			errStr := "Onboarding certificate cannot be found"
			log.Infof("VerifyDeviceNetworkStatus: %s\n", errStr)
			return false, errors.New(errStr)
		}
		clientCert := &onboardingCert
		tlsConfig, err = zedcloud.GetTlsConfig(zedcloudCtx.DeviceNetworkStatus,
			serverName, clientCert, &zedcloudCtx)
		if err != nil {
			errStr := "TLS configuration for talking to Zedcloud cannot be found"

			log.Infof("VerifyDeviceNetworkStatus: %s\n", errStr)
			return false, errors.New(errStr)
		}
	}
	zedcloudCtx.TlsConfig = tlsConfig
	for ix := range status.Ports {
		err = CheckAndGetNetworkProxy(&status, &status.Ports[ix])
		if err != nil {
			errStr := fmt.Sprintf("GetNetworkProxy failed %s", err)
			log.Errorf("VerifyDeviceNetworkStatus: %s\n", errStr)
			return false, errors.New(errStr)
		}
	}
	cloudReachable, rtf, err := zedcloud.VerifyAllIntf(zedcloudCtx, testUrl, retryCount, 1)
	if err != nil {
		if rtf {
			log.Errorf("VerifyDeviceNetworkStatus: VerifyAllIntf remoteTemporaryFailure %s",
				err)
		} else {
			log.Errorf("VerifyDeviceNetworkStatus: VerifyAllIntf failed %s",
				err)
		}
		return rtf, err
	}

	if cloudReachable {
		log.Infof("Uplink test SUCCESS to URL: %s", testUrl)
		return false, nil
	}
	errStr := fmt.Sprintf("Uplink test FAIL to URL: %s", testUrl)
	log.Errorf("VerifyDeviceNetworkStatus: %s\n", errStr)
	return rtf, errors.New(errStr)
}

// Calculate local IP addresses to make a types.DeviceNetworkStatus
func MakeDeviceNetworkStatus(globalConfig types.DevicePortConfig, oldStatus types.DeviceNetworkStatus) types.DeviceNetworkStatus {
	var globalStatus types.DeviceNetworkStatus

	log.Infof("MakeDeviceNetworkStatus()\n")
	globalStatus.Version = globalConfig.Version
	globalStatus.Ports = make([]types.NetworkPortStatus,
		len(globalConfig.Ports))
	for ix, u := range globalConfig.Ports {
		globalStatus.Ports[ix].IfName = u.IfName
		globalStatus.Ports[ix].Name = u.Name
		globalStatus.Ports[ix].IsMgmt = u.IsMgmt
		globalStatus.Ports[ix].Free = u.Free
		globalStatus.Ports[ix].ProxyConfig = u.ProxyConfig
		// Set fields from the config...
		globalStatus.Ports[ix].Dhcp = u.Dhcp
		_, subnet, _ := net.ParseCIDR(u.AddrSubnet)
		if subnet != nil {
			globalStatus.Ports[ix].Subnet = *subnet
		}
		globalStatus.Ports[ix].Gateway = u.Gateway
		globalStatus.Ports[ix].DomainName = u.DomainName
		globalStatus.Ports[ix].NtpServer = u.NtpServer
		globalStatus.Ports[ix].DnsServers = u.DnsServers
		if u.ParseError != "" {
			globalStatus.Ports[ix].Error = u.ParseError
			globalStatus.Ports[ix].ErrorTime = u.ParseErrorTime
		}
		ifindex, err := IfnameToIndex(u.IfName)
		if err != nil {
			errStr := fmt.Sprintf("Port %s does not exist - ignored",
				u.IfName)
			log.Errorf("MakeDeviceNetworkStatus: %s\n", errStr)
			globalStatus.Ports[ix].Error = errStr
			globalStatus.Ports[ix].ErrorTime = time.Now()
			continue
		}
		addrs, err := GetIPAddrs(ifindex)
		if err != nil {
			log.Warnf("MakeDeviceNetworkStatus addrs not found %s index %d: %s\n",
				u.IfName, ifindex, err)
			addrs = nil
		}
		globalStatus.Ports[ix].AddrInfoList = make([]types.AddrInfo,
			len(addrs))
		if len(addrs) == 0 {
			log.Infof("PortAddrs(%s) found NO addresses",
				u.IfName)
		}
		for i, addr := range addrs {
			v := "IPv4"
			if addr.To4() == nil {
				v = "IPv6"
			}
			log.Infof("PortAddrs(%s) found %s %v\n",
				u.IfName, v, addr)
			globalStatus.Ports[ix].AddrInfoList[i].Addr = addr
		}
		// Get DNS etc info from dhcpcd. Updates DomainName and DnsServers
		err = GetDhcpInfo(&globalStatus.Ports[ix])
		if err != nil {
			errStr := fmt.Sprintf("GetDhcpInfo failed %s", err)
			globalStatus.Ports[ix].Error = errStr
			globalStatus.Ports[ix].ErrorTime = time.Now()
		}
		GetDNSInfo(&globalStatus.Ports[ix])

		// Attempt to get a wpad.dat file if so configured
		// Result is updating the Pacfile
		// We always redo this since we don't know what has changed
		// from the previous DeviceNetworkStatus.
		err = CheckAndGetNetworkProxy(&globalStatus,
			&globalStatus.Ports[ix])
		if err != nil {
			errStr := fmt.Sprintf("GetNetworkProxy failed %s", err)
			globalStatus.Ports[ix].Error = errStr
			globalStatus.Ports[ix].ErrorTime = time.Now()
		}
	}
	// Preserve geo info for existing interface and IP address
	for ui := range globalStatus.Ports {
		u := &globalStatus.Ports[ui]
		for i := range u.AddrInfoList {
			// Need pointer since we are going to modify
			ai := &u.AddrInfoList[i]
			oai := lookupPortStatusAddr(oldStatus,
				u.IfName, ai.Addr)
			if oai == nil {
				continue
			}
			ai.Geo = oai.Geo
			ai.LastGeoTimestamp = oai.LastGeoTimestamp
		}
	}
	// Need to write resolv.conf for Geo
	UpdateResolvConf(globalStatus)
	UpdatePBR(globalStatus)
	// Immediate check
	UpdateDeviceNetworkGeo(time.Second, &globalStatus)
	log.Infof("MakeDeviceNetworkStatus() DONE\n")
	return globalStatus
}

// write the access-point name into /run/cccesspoint directory
// the filenames are the physical ports with access-point address/name in content
func devPortInstallAPname(ifname string, wconfig types.WirelessConfig) {
	if _, err := os.Stat(apDirname); err != nil {
		if err := os.MkdirAll(apDirname, 0700); err != nil {
			log.Errorln(err)
			return
		}
	}

	filepath := apDirname + "/" + ifname
	if _, err := os.Stat(filepath); err == nil {
		if err := os.Remove(filepath); err != nil {
			log.Errorln(err)
			return
		}
	}

	if len(wconfig.Cellular) == 0 {
		return
	}

	file, err := os.Create(filepath)
	if err != nil {
		log.Errorln(err)
		return
	}

	for _, cell := range wconfig.Cellular {
		s := fmt.Sprintf("%s\n", cell.APN)
		file.WriteString(s)
		break // only handle the first APN for now utill we know how to handle multiple of APNs
	}
	file.Close()
	log.Infof("devPortInstallAPname: write file %s for name %v", filepath, wconfig.Cellular)
}

func devPortInstallWifiConfig(ifname string, wconfig types.WirelessConfig) bool {
	if _, err := os.Stat(runwlanDir); os.IsNotExist(err) {
		err = os.Mkdir(runwlanDir, 600)
		if err != nil {
			log.Errorln("/run/wlan ", err)
			return false
		}
	}

	tmpfile, err := ioutil.TempFile(runwlanDir, wpaTempname)
	if err != nil {
		log.Errorln("TempFile ", err)
		return false
	}
	defer tmpfile.Close()
	defer os.Remove(tmpfile.Name())
	tmpfile.Chmod(0600)

	log.Infof("devPortInstallWifiConfig: write file %s for wifi params %v, size %d", wpaFilename, wconfig.Wifi, len(wconfig.Wifi))
	if len(wconfig.Wifi) == 0 {
		// generate dummy wpa_supplicant.conf
		tmpfile.WriteString("# Fill in the networks and their passwords\nnetwork={\n")
		tmpfile.WriteString("       ssid=\"XXX\"\n")
		tmpfile.WriteString("       scan_ssid=1\n")
		tmpfile.WriteString("       key_mgmt=WPA-PSK\n")
		tmpfile.WriteString("       psk=\"YYYYYYYY\"\n")
		tmpfile.WriteString("}\n")
	} else {
		tmpfile.WriteString("# Automatically generated\n")
		for _, wifi := range wconfig.Wifi {
			cred, err := getWifiCredential(wifi)
			if err != nil {
				continue
			}
			tmpfile.WriteString("network={\n")
			s := fmt.Sprintf("        ssid=\"%s\"\n", wifi.SSID)
			tmpfile.WriteString(s)
			tmpfile.WriteString("        scan_ssid=1\n")
			switch wifi.KeyScheme {
			case types.KeySchemeWpaPsk: // WPA-PSK
				tmpfile.WriteString("        key_mgmt=WPA-PSK\n")
				// this assumes a hashed passphrase, otherwise need "" around string
				if len(cred.Password) > 0 {
					s = fmt.Sprintf("        psk=%s\n", cred.Password)
					tmpfile.WriteString(s)
				}
			case types.KeySchemeWpaEap: // EAP PEAP
				tmpfile.WriteString("        key_mgmt=WPA-EAP\n        eap=PEAP\n")
				if len(cred.Identity) > 0 {
					s = fmt.Sprintf("        identity=\"%s\"\n", cred.Identity)
					tmpfile.WriteString(s)
				}
				if len(cred.Password) > 0 {
					s = fmt.Sprintf("        password=hash:%s\n", cred.Password)
					tmpfile.WriteString(s)
				}
				// comment out the certifacation verify. file.WriteString("        ca_cert=\"/config/ca.pem\"\n")
				tmpfile.WriteString("        phase1=\"peaplabel=1\"\n")
				tmpfile.WriteString("        phase2=\"auth=MSCHAPV2\"\n")
			}
			if wifi.Priority != 0 {
				s = fmt.Sprintf("        priority=%d\n", wifi.Priority)
				tmpfile.WriteString(s)
			}
			tmpfile.WriteString("}\n")
		}
	}
	tmpfile.Sync()
	if err := tmpfile.Close(); err != nil {
		log.Errorln("Close ", tmpfile.Name(), err)
		return false
	}

	if err := os.Rename(tmpfile.Name(), wpaFilename); err != nil {
		log.Errorln(err)
		return false
	}

	return true
}

func getWifiCredential(wifi types.WifiConfig) (zconfig.CredentialBlock, error) {
	cred := zconfig.CredentialBlock{}
	if !wifi.IsCipher {
		cred.Identity = wifi.Identity
		cred.Password = wifi.Password
		return cred, nil
	}
	if !wifi.IsValidCipher {
		errStr := fmt.Sprintf("%s, Cipher Block is not ready", wifi.SSID)
		log.Errorln(errStr)
		return cred, errors.New(errStr)
	}
	clearData, err := tpmmgr.DecryptCipherBlock(wifi.CipherBlock)
	if err != nil {
		log.Errorf("%s, wifi CipherData decryption failed, %v\n",
			wifi.SSID, err)
		return cred, err
	}
	if err := proto.Unmarshal(clearData, &cred); err != nil {
		log.Errorf("%s, wifi credential unmarshall failed, %v\n",
			wifi.SSID, err)
		return cred, err
	}
	return cred, nil
}

// CheckDNSUpdate sees if we should update based on DNS
// XXX identical code to HandleAddressChange
func CheckDNSUpdate(ctx *DeviceNetworkContext) {

	// Check if we have more or less addresses
	var dnStatus types.DeviceNetworkStatus

	log.Infof("CheckDnsUpdate Pending.Inprogress %v",
		ctx.Pending.Inprogress)
	if !ctx.Pending.Inprogress {
		dnStatus = *ctx.DeviceNetworkStatus
		status := MakeDeviceNetworkStatus(*ctx.DevicePortConfig,
			dnStatus)

		if !reflect.DeepEqual(*ctx.DeviceNetworkStatus, status) {
			log.Infof("CheckDNSUpdate: change from %v to %v\n",
				*ctx.DeviceNetworkStatus, status)
			*ctx.DeviceNetworkStatus = status
			DoDNSUpdate(ctx)
		} else {
			log.Infof("CheckDNSUpdate: No change\n")
		}
	} else {
		dnStatus = MakeDeviceNetworkStatus(*ctx.DevicePortConfig,
			ctx.Pending.PendDNS)

		if !reflect.DeepEqual(ctx.Pending.PendDNS, dnStatus) {
			log.Infof("CheckDNSUpdate pending: change from %v to %v\n",
				ctx.Pending.PendDNS, dnStatus)
			pingTestDNS := checkIfMgmtPortsHaveIPandDNS(dnStatus)
			if pingTestDNS {
				// We have a suitable candiate for running our cloud ping test.
				log.Infof("CheckDNSUpdate: Running cloud ping test now, " +
					"Since we have suitable addresses already.")
				VerifyDevicePortConfig(ctx)
			}
		} else {
			log.Infof("CheckDNSUpdate pending: No change\n")
		}
	}
}

// GetIPAddrs return all IP addresses for an ifindex, and updates the cached info.
// Leaves mask uninitialized
// It replaces what is in the Ifindex cache since AddrChange callbacks
// are far from reliable.
// If AddrChange worked reliably this would just be:
// return IfindexToAddrs(ifindex)
func GetIPAddrs(ifindex int) ([]net.IP, error) {

	var addrs []net.IP

	link, err := netlink.LinkByIndex(ifindex)
	if err != nil {
		err = errors.New(fmt.Sprintf("Port in config/global does not exist: %d",
			ifindex))
		return addrs, err
	}
	addrs4, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		log.Warnf("netlink.AddrList %d V4 failed: %s", ifindex, err)
		addrs4 = nil
	}
	addrs6, err := netlink.AddrList(link, netlink.FAMILY_V6)
	if err != nil {
		log.Warnf("netlink.AddrList %d V4 failed: %s", ifindex, err)
		addrs6 = nil
	}
	log.Infof("GetIPAddrs(%d) found %v and %v", ifindex, addrs4, addrs6)
	IfindexToAddrsFlush(ifindex)
	for _, a := range addrs4 {
		if a.IP == nil {
			continue
		}
		addrs = append(addrs, a.IP)
		IfindexToAddrsAdd(ifindex, a.IP)
	}
	for _, a := range addrs6 {
		if a.IP == nil {
			continue
		}
		addrs = append(addrs, a.IP)
		IfindexToAddrsAdd(ifindex, a.IP)
	}
	return addrs, nil

}

func lookupPortStatusAddr(status types.DeviceNetworkStatus,
	ifname string, addr net.IP) *types.AddrInfo {

	for _, u := range status.Ports {
		if u.IfName != ifname {
			continue
		}
		for _, ai := range u.AddrInfoList {
			if ai.Addr.Equal(addr) {
				return &ai
			}
		}
	}
	return nil
}

// Returns true if anything might have changed
func UpdateDeviceNetworkGeo(timelimit time.Duration, globalStatus *types.DeviceNetworkStatus) bool {
	change := false
	for ui := range globalStatus.Ports {
		u := &globalStatus.Ports[ui]
		if globalStatus.Version >= types.DPCIsMgmt &&
			!u.IsMgmt {
			continue
		}
		for i := range u.AddrInfoList {
			// Need pointer since we are going to modify
			ai := &u.AddrInfoList[i]
			if ai.Addr.IsLinkLocalUnicast() {
				continue
			}

			numDNSServers := types.CountDNSServers(*globalStatus, u.IfName)
			if numDNSServers == 0 {
				continue
			}
			timePassed := time.Since(ai.LastGeoTimestamp)
			if timePassed < timelimit {
				continue
			}
			// geoloc with short timeout
			opt := ipinfo.Options{
				Timeout:  5 * time.Second,
				SourceIp: ai.Addr,
			}
			info, err := ipinfo.MyIPWithOptions(opt)
			if err != nil {
				// Ignore error
				log.Infof("UpdateDeviceNetworkGeo MyIPInfo failed %s\n", err)
				continue
			}
			// Note that if the global IP is unchanged we don't
			// update anything.
			if info.IP == ai.Geo.IP {
				continue
			}
			log.Infof("UpdateDeviceNetworkGeo MyIPInfo changed from %v to %v\n",
				ai.Geo, *info)
			ai.Geo = *info
			ai.LastGeoTimestamp = time.Now()
			change = true
		}
	}
	return change
}

func lookupOnIfname(config types.DevicePortConfig, ifname string) *types.NetworkPortConfig {
	for _, c := range config.Ports {
		if c.IfName == ifname {
			return &c
		}
	}
	return nil
}
