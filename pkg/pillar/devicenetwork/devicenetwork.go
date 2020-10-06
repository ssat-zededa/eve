// Copyright (c) 2017 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package devicenetwork

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/eriknordmark/ipinfo"
	"github.com/eriknordmark/netlink"
	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/cipher"
	"github.com/lf-edge/eve/pkg/pillar/hardware"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
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
		config.Ports[ix].Phylabel = u
		config.Ports[ix].Logicallabel = u
		for _, f := range free {
			if f == u {
				config.Ports[ix].Free = true
				break
			}
		}
		config.Ports[ix].IsMgmt = true
		config.Ports[ix].Dhcp = types.DT_CLIENT
		portPtr := ctx.DevicePortConfig.GetPortByIfName(u)
		if portPtr != nil {
			config.Ports[ix].WirelessCfg = portPtr.WirelessCfg
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

// VerifyDeviceNetworkStatus
//  Check if device can talk to outside world via atleast 'successCount' of the
//  uplinks
// Return Values:
//  Success / Failure
//  error - Overall Error
//  PerInterfaceErrorMap - Key: ifname
//    Includes entries for all interfaces that were tested.
//    For each interface verified
//      set Error ( If success, set to "")
//      set ErrorTime to time of testing ( Even if verify Successful )
func VerifyDeviceNetworkStatus(log *base.LogObject, status types.DeviceNetworkStatus,
	successCount uint, iteration int, timeout uint32) (bool, types.IntfStatusMap, error) {

	log.Debugf("VerifyDeviceNetworkStatus() successCount %d, iteration %d",
		successCount, iteration)

	// Map of per-interface errors
	intfStatusMap := *types.NewIntfStatusMap()

	server, err := ioutil.ReadFile(types.ServerFileName)
	if err != nil {
		log.Fatal(err)
	}
	serverNameAndPort := strings.TrimSpace(string(server))
	serverName := strings.Split(serverNameAndPort, ":")[0]

	zedcloudCtx := zedcloud.NewContext(log, zedcloud.ContextOptions{
		DevNetworkStatus: &status,
		Timeout:          timeout,
		Serial:           hardware.GetProductSerial(log),
		SoftSerial:       hardware.GetSoftSerial(log),
		AgentName:        "devicenetwork",
	})
	log.Infof("VerifyDeviceNetworkStatus: Use V2 API %v\n", zedcloud.UseV2API())
	testURL := zedcloud.URLPathString(serverNameAndPort, zedcloudCtx.V2API, nilUUID, "ping")

	log.Debugf("NIM Get Device Serial %s, Soft Serial %s\n", zedcloudCtx.DevSerial,
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
			return false, intfStatusMap, errors.New(errStr)
		}
		clientCert := &onboardingCert
		tlsConfig, err = zedcloud.GetTlsConfig(zedcloudCtx.DeviceNetworkStatus,
			serverName, clientCert, &zedcloudCtx)
		if err != nil {
			errStr := fmt.Sprintf("TLS configuration for talking to Zedcloud cannot be found: %s", err)
			log.Infof("VerifyDeviceNetworkStatus: %s\n", errStr)
			return false, intfStatusMap, errors.New(errStr)
		}
	}
	zedcloudCtx.TlsConfig = tlsConfig
	for ix := range status.Ports {
		err = CheckAndGetNetworkProxy(log, &status, &status.Ports[ix])
		if err != nil {
			ifName := status.Ports[ix].IfName
			errStr := fmt.Sprintf("ifName: %s. Failed to get NetworkProxy. Err:%s",
				ifName, err)
			log.Errorf("VerifyDeviceNetworkStatus: %s", errStr)
			intfStatusMap.RecordFailure(ifName, errStr)
			return false, intfStatusMap, errors.New(errStr)
		}
	}
	cloudReachable, rtf, tempIntfStatusMap, err := zedcloud.VerifyAllIntf(
		&zedcloudCtx, testURL, successCount, iteration)
	intfStatusMap.SetOrUpdateFromMap(tempIntfStatusMap)
	log.Debugf("VerifyDeviceNetworkStatus: intfStatusMap - %+v", intfStatusMap)
	if err != nil {
		if rtf {
			log.Errorf("VerifyDeviceNetworkStatus: VerifyAllIntf remoteTemporaryFailure %s",
				err)
		} else {
			log.Errorf("VerifyDeviceNetworkStatus: VerifyAllIntf failed %s",
				err)
		}
		return rtf, intfStatusMap, err
	}

	if cloudReachable {
		log.Infof("Uplink test SUCCESS to URL: %s", testURL)
		return false, intfStatusMap, nil
	}
	errStr := fmt.Sprintf("Uplink test FAIL to URL: %s", testURL)
	log.Errorf("VerifyDeviceNetworkStatus: %s, intfStatusMap: %+v",
		errStr, intfStatusMap)
	return rtf, intfStatusMap, err
}

// Calculate local IP addresses to make a types.DeviceNetworkStatus
func MakeDeviceNetworkStatus(log *base.LogObject, globalConfig types.DevicePortConfig, oldStatus types.DeviceNetworkStatus) types.DeviceNetworkStatus {
	var globalStatus types.DeviceNetworkStatus

	log.Infof("MakeDeviceNetworkStatus()\n")
	globalStatus.Version = globalConfig.Version
	globalStatus.State = oldStatus.State
	globalStatus.Ports = make([]types.NetworkPortStatus,
		len(globalConfig.Ports))
	for ix, u := range globalConfig.Ports {
		globalStatus.Ports[ix].IfName = u.IfName
		globalStatus.Ports[ix].Phylabel = u.Phylabel
		globalStatus.Ports[ix].Logicallabel = u.Logicallabel
		globalStatus.Ports[ix].Alias = u.Alias
		globalStatus.Ports[ix].IsMgmt = u.IsMgmt
		globalStatus.Ports[ix].Free = u.Free
		globalStatus.Ports[ix].ProxyConfig = u.ProxyConfig
		// Set fields from the config...
		globalStatus.Ports[ix].Dhcp = u.Dhcp
		_, subnet, _ := net.ParseCIDR(u.AddrSubnet)
		if subnet != nil {
			globalStatus.Ports[ix].Subnet = *subnet
		}
		// Start with any statically assigned values; update below
		globalStatus.Ports[ix].DomainName = u.DomainName
		globalStatus.Ports[ix].DNSServers = u.DnsServers

		globalStatus.Ports[ix].NtpServer = u.NtpServer
		globalStatus.Ports[ix].TestResults = u.TestResults
		ifindex, err := IfnameToIndex(log, u.IfName)
		if err != nil {
			errStr := fmt.Sprintf("Port %s does not exist - ignored",
				u.IfName)
			log.Errorf("MakeDeviceNetworkStatus: %s\n", errStr)
			globalStatus.Ports[ix].RecordFailure(errStr)
			continue
		}
		addrs, up, macAddr, err := GetIPAddrs(log, ifindex)
		if err != nil {
			log.Warnf("MakeDeviceNetworkStatus addrs not found %s index %d: %s\n",
				u.IfName, ifindex, err)
			addrs = nil
		}
		globalStatus.Ports[ix].Up = up
		globalStatus.Ports[ix].MacAddr = macAddr.String()
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
		GetDhcpInfo(log, &globalStatus.Ports[ix])
		GetDNSInfo(log, &globalStatus.Ports[ix])

		// Get used default routers aka gateways from kernel
		globalStatus.Ports[ix].DefaultRouters = getDefaultRouters(log, ifindex)

		// Attempt to get a wpad.dat file if so configured
		// Result is updating the Pacfile
		// We always redo this since we don't know what has changed
		// from the previous DeviceNetworkStatus.
		err = CheckAndGetNetworkProxy(log, &globalStatus,
			&globalStatus.Ports[ix])
		if err != nil {
			errStr := fmt.Sprintf("GetNetworkProxy failed for %s: %s",
				u.IfName, err)
			// XXX where can we return this failure?
			// Already have TestResults set from above
			log.Error(errStr)
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
	UpdateResolvConf(log, globalStatus)
	UpdatePBR(log, globalStatus)
	// Immediate check
	UpdateDeviceNetworkGeo(log, time.Second, &globalStatus)
	log.Infof("MakeDeviceNetworkStatus() DONE\n")
	return globalStatus
}

// write the access-point name into /run/accesspoint directory
// the filenames are the physical ports with access-point address/name in content
func devPortInstallAPname(log *base.LogObject, ifname string, wconfig types.WirelessConfig) {
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

func devPortInstallWifiConfig(ctx *DeviceNetworkContext,
	ifname string, wconfig types.WirelessConfig) bool {

	log := ctx.Log
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
			decBlock, err := getWifiCredential(ctx, wifi)
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
				if len(decBlock.WifiPassword) > 0 {
					s = fmt.Sprintf("        psk=%s\n", decBlock.WifiPassword)
					tmpfile.WriteString(s)
				}
			case types.KeySchemeWpaEap: // EAP PEAP
				tmpfile.WriteString("        key_mgmt=WPA-EAP\n        eap=PEAP\n")
				if len(decBlock.WifiUserName) > 0 {
					s = fmt.Sprintf("        identity=\"%s\"\n", decBlock.WifiUserName)
					tmpfile.WriteString(s)
				}
				if len(decBlock.WifiPassword) > 0 {
					s = fmt.Sprintf("        password=hash:%s\n", decBlock.WifiPassword)
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

func getWifiCredential(ctx *DeviceNetworkContext,
	wifi types.WifiConfig) (types.EncryptionBlock, error) {

	log := ctx.Log
	if wifi.CipherBlockStatus.IsCipher {
		status, decBlock, err := cipher.GetCipherCredentials(&ctx.DecryptCipherContext,
			"devicenetwork", wifi.CipherBlockStatus)
		ctx.PubCipherBlockStatus.Publish(status.Key(), status)
		if err != nil {
			log.Errorf("%s, wifi config cipherblock decryption unsuccessful, falling back to cleartext: %v\n",
				wifi.SSID, err)
			decBlock.WifiUserName = wifi.Identity
			decBlock.WifiPassword = wifi.Password
			// We assume IsCipher is only set when there was some
			// data. Hence this is a fallback if there is
			// some cleartext.
			if decBlock.WifiUserName != "" || decBlock.WifiPassword != "" {
				cipher.RecordFailure(ctx.AgentName,
					types.CleartextFallback)
			} else {
				cipher.RecordFailure(ctx.AgentName,
					types.MissingFallback)
			}
			return decBlock, nil
		}
		log.Infof("%s, wifi config cipherblock decryption successful\n", wifi.SSID)
		return decBlock, nil
	}
	log.Infof("%s, wifi config cipherblock not present\n", wifi.SSID)
	decBlock := types.EncryptionBlock{}
	decBlock.WifiUserName = wifi.Identity
	decBlock.WifiPassword = wifi.Password
	if decBlock.WifiUserName != "" || decBlock.WifiPassword != "" {
		cipher.RecordFailure(ctx.AgentName, types.NoCipher)
	} else {
		cipher.RecordFailure(ctx.AgentName, types.NoData)
	}
	return decBlock, nil
}

// CheckDNSUpdate sees if we should update based on DNS
// XXX identical code to HandleAddressChange
func CheckDNSUpdate(ctx *DeviceNetworkContext) {

	log := ctx.Log
	// Check if we have more or less addresses
	var dnStatus types.DeviceNetworkStatus

	log.Infof("CheckDnsUpdate Pending.Inprogress %v",
		ctx.Pending.Inprogress)
	if !ctx.Pending.Inprogress {
		dnStatus = *ctx.DeviceNetworkStatus
		status := MakeDeviceNetworkStatus(log, *ctx.DevicePortConfig,
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
		dnStatus = MakeDeviceNetworkStatus(log, *ctx.DevicePortConfig,
			ctx.Pending.PendDNS)

		if !reflect.DeepEqual(ctx.Pending.PendDNS, dnStatus) {
			log.Infof("CheckDNSUpdate pending: change from %v to %v\n",
				ctx.Pending.PendDNS, dnStatus)
			pingTestDNS := checkIfMgmtPortsHaveIPandDNS(log, dnStatus)
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
// Also returns the up flag (based on admin status), and hardware address.
// Leaves mask uninitialized
// It replaces what is in the Ifindex cache since AddrChange callbacks
// are far from reliable.
// If AddrChange worked reliably this would just be:
// return IfindexToAddrs(ifindex)
func GetIPAddrs(log *base.LogObject, ifindex int) ([]net.IP, bool, net.HardwareAddr, error) {

	var addrs []net.IP
	var up bool
	var macAddr net.HardwareAddr

	link, err := netlink.LinkByIndex(ifindex)
	if err != nil {
		err = errors.New(fmt.Sprintf("Port in config/global does not exist: %d",
			ifindex))
		return addrs, up, macAddr, err
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
	attrs := link.Attrs()
	up = (attrs.Flags & net.FlagUp) != 0
	if attrs.HardwareAddr != nil {
		macAddr = attrs.HardwareAddr
	}

	log.Infof("GetIPAddrs(%d) found %v and %v", ifindex, addrs4, addrs6)
	IfindexToAddrsFlush(log, ifindex)
	for _, a := range addrs4 {
		if a.IP == nil {
			continue
		}
		addrs = append(addrs, a.IP)
		IfindexToAddrsAdd(log, ifindex, a.IP)
	}
	for _, a := range addrs6 {
		if a.IP == nil {
			continue
		}
		addrs = append(addrs, a.IP)
		IfindexToAddrsAdd(log, ifindex, a.IP)
	}
	return addrs, up, macAddr, nil

}

// getDefaultRouters retries the default routers from the kernel i.e.,
// the ones actually in use whether from DHCP or static
func getDefaultRouters(log *base.LogObject, ifindex int) []net.IP {
	var res []net.IP
	table := types.GetDefaultRouteTable()
	// Note that a default route is represented as nil Dst
	filter := netlink.Route{Table: table, LinkIndex: ifindex, Dst: nil}
	fflags := netlink.RT_FILTER_TABLE
	fflags |= netlink.RT_FILTER_OIF
	fflags |= netlink.RT_FILTER_DST
	routes, err := netlink.RouteListFiltered(syscall.AF_UNSPEC,
		&filter, fflags)
	if err != nil {
		log.Errorf("getDefaultRouters: for ifindex %d RouteList failed: %v",
			ifindex, err)
		return res
	}
	// log.Debugf("getDefaultRouters(%s) - got %d", ifname, len(routes))
	for _, rt := range routes {
		if rt.Table != table {
			continue
		}
		if ifindex != 0 && rt.LinkIndex != ifindex {
			continue
		}
		// log.Debugf("getDefaultRouters route dest %v", rt.Dst)
		res = append(res, rt.Gw)
	}
	return res
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
func UpdateDeviceNetworkGeo(log *base.LogObject, timelimit time.Duration, globalStatus *types.DeviceNetworkStatus) bool {
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
