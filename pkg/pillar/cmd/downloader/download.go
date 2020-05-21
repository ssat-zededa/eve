package downloader

import (
	"errors"
	"fmt"
	"net"

	log "github.com/sirupsen/logrus"

	"github.com/lf-edge/eve/pkg/pillar/zedUpload"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
)

// perform the actual download
func download(ctx *downloaderContext, trType zedUpload.SyncTransportType,
	status Status, syncOp zedUpload.SyncOpType, downloadURL string,
	auth *zedUpload.AuthInput, dpath, region string, maxsize uint64, ifname string,
	ipSrc net.IP, filename, locFilename string) error {

	// create Endpoint
	var dEndPoint zedUpload.DronaEndPoint
	var err error
	switch trType {
	case zedUpload.SyncHttpTr, zedUpload.SyncSftpTr:
		dEndPoint, err = ctx.dCtx.NewSyncerDest(trType, downloadURL, dpath, auth)
	case zedUpload.SyncAzureTr:
		dEndPoint, err = ctx.dCtx.NewSyncerDest(trType, "", dpath, auth)
	case zedUpload.SyncAwsTr:
		dEndPoint, err = ctx.dCtx.NewSyncerDest(trType, region, dpath, auth)
	case zedUpload.SyncOCIRegistryTr:
		dEndPoint, err = ctx.dCtx.NewSyncerDest(trType, downloadURL, filename, auth)
	default:
		err = fmt.Errorf("unknown transfer type: %s", trType)
	}
	if err != nil {
		log.Errorf("NewSyncerDest failed: %s", err)
		return err
	}
	// check for proxies on the selected management port interface
	proxyLookupURL := zedcloud.IntfLookupProxyCfg(&ctx.deviceNetworkStatus, ifname, downloadURL)
	proxyURL, err := zedcloud.LookupProxy(&ctx.deviceNetworkStatus, ifname, proxyLookupURL)
	if err == nil && proxyURL != nil {
		log.Infof("%s: Using proxy %s", trType, proxyURL.String())
		dEndPoint.WithSrcIPAndProxySelection(ipSrc, proxyURL)
	} else {
		dEndPoint.WithSrcIPSelection(ipSrc)
	}

	var respChan = make(chan *zedUpload.DronaRequest)

	log.Infof("%s syncOp for dpath:<%s>, region: <%s>, filename: <%s>, "+
		"downloadURL: <%s>, maxsize: %d, ifname: %s, ipSrc: %+v, locFilename: %s",
		trType, dpath, region, filename, downloadURL, maxsize, ifname, ipSrc,
		locFilename)
	// create Request
	req := dEndPoint.NewRequest(syncOp, filename, locFilename,
		int64(maxsize), true, respChan)
	if req == nil {
		return errors.New("NewRequest failed")
	}

	req.Post()
	for resp := range respChan {
		if resp.IsDnUpdate() {
			asize, osize, progress := resp.Progress()
			log.Infof("Update progress for %v: %v/%v",
				resp.GetLocalName(), asize, osize)
			// sometime, the download goes to an infinite loop,
			// showing it has downloaded, more than it is supposed to
			// aborting download, marking it as an error
			if asize > osize {
				errStr := fmt.Sprintf("Size '%v' provided in image config of '%s' is incorrect.\nDownload status (%v / %v). Aborting the download",
					osize, resp.GetLocalName(), asize, osize)
				log.Errorln(errStr)
				return errors.New(errStr)
			}
			status.Progress(progress)
			continue
		}
		if syncOp == zedUpload.SyncOpDownload {
			err = resp.GetDnStatus()
		} else {
			_, err = resp.GetUpStatus()
		}
		if resp.IsError() {
			return err
		}
		log.Infof("Done for %v: size %v/%v",
			resp.GetLocalName(),
			resp.GetAsize(), resp.GetOsize())
		status.Progress(100)
		return nil
	}
	// if we got here, channel was closed
	// range ends on a closed channel, which is the equivalent of "!ok"
	errStr := fmt.Sprintf("respChan EOF for <%s>, <%s>, <%s>",
		dpath, region, filename)
	log.Errorln(errStr)
	return errors.New(errStr)
}

func objectMetadata(ctx *downloaderContext, trType zedUpload.SyncTransportType,
	syncOp zedUpload.SyncOpType, downloadURL string,
	auth *zedUpload.AuthInput, dpath, region string, ifname string,
	ipSrc net.IP, filename string) (string, error) {

	// create Endpoint
	var dEndPoint zedUpload.DronaEndPoint
	var err error
	var sha256 string
	switch trType {
	case zedUpload.SyncOCIRegistryTr:
		dEndPoint, err = ctx.dCtx.NewSyncerDest(trType, downloadURL, filename, auth)
	default:
		err = fmt.Errorf("Not supported transport type: %s", trType)
	}
	if err != nil {
		log.Errorf("NewSyncerDest failed: %s", err)
		return sha256, err
	}
	// check for proxies on the selected management port interface
	proxyLookupURL := zedcloud.IntfLookupProxyCfg(&ctx.deviceNetworkStatus, ifname, downloadURL)

	proxyURL, err := zedcloud.LookupProxy(&ctx.deviceNetworkStatus, ifname, proxyLookupURL)
	if err == nil && proxyURL != nil {
		log.Infof("%s: Using proxy %s", trType, proxyURL.String())
		dEndPoint.WithSrcIPAndProxySelection(ipSrc, proxyURL)
	} else {
		dEndPoint.WithSrcIPSelection(ipSrc)
	}

	var respChan = make(chan *zedUpload.DronaRequest)

	log.Infof("%s syncOp for dpath:<%s>, region: <%s>, filename: <%s>, "+
		"downloadURL: <%s>, ifname: %s, ipSrc: %+v",
		trType, dpath, region, filename, downloadURL, ifname, ipSrc)
	// create Request
	// Round up from bytes to Mbytes
	req := dEndPoint.NewRequest(syncOp, filename, "",
		0, true, respChan)
	if req == nil {
		return sha256, errors.New("NewRequest failed")
	}

	req.Post()
	for resp := range respChan {
		if resp.IsDnUpdate() {
			continue
		}
		if syncOp == zedUpload.SyncOpGetObjectMetaData {
			sha256 = resp.GetSha256()
			err = resp.GetDnStatus()
		} else {
			_, err = resp.GetUpStatus()
		}
		if resp.IsError() {
			return sha256, err
		}
		log.Infof("Resolve config Done for %v: sha %v",
			filename, resp.GetSha256())
		return sha256, nil
	}
	// if we got here, channel was closed
	// range ends on a closed channel, which is the equivalent of "!ok"
	errStr := fmt.Sprintf("respChan EOF for <%s>, <%s>, <%s>",
		dpath, region, filename)
	log.Errorln(errStr)
	return sha256, errors.New(errStr)
}
