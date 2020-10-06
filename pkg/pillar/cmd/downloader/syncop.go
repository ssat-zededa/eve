// Copyright (c) 2019-2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package downloader

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"time"

	zconfig "github.com/lf-edge/eve/api/go/config"
	"github.com/lf-edge/eve/pkg/pillar/cipher"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/zedUpload"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
)

// Drona APIs for object Download
func handleSyncOp(ctx *downloaderContext, key string,
	config types.DownloaderConfig, status *types.DownloaderStatus,
	dst *types.DatastoreConfig) {
	var (
		err                                                    error
		errStr, locFilename, locDirname, remoteName, serverURL string
		syncOp                                                 zedUpload.SyncOpType = zedUpload.SyncOpDownload
		trType                                                 zedUpload.SyncTransportType
		auth                                                   *zedUpload.AuthInput
	)

	// the target filename, where to place the download, is provided in config.
	// downloader has two options:
	//  * download the file part by part, filling up the `Target` until it is complete, and then sending status
	//  * create a separate cache directory elsewhere under sole downloader control, download the file part by part there,
	//    and when complete, do a single atomic copy to `Target` and send status
	// `config.Target` is where it is expected to place the final downloaded file; how it gets there
	// is up to downloader.
	// As of this writing, the file is downloaded directly to `config.Target`
	locFilename = config.Target
	locDirname = path.Dir(locFilename)

	// construct the datastore context
	dsCtx, err := constructDatastoreContext(ctx, config.Name, config.NameIsURL, *dst)
	if err != nil {
		errStr := fmt.Sprintf("%s, Datastore construction failed, %s", config.Name, err)
		handleSyncOpResponse(ctx, config, status, locFilename, key, errStr)
		return
	}

	// by default the metricsURL _is_ the DownloadURL, but can override in switch
	metricsUrl := dsCtx.DownloadURL

	// update status to DOWNLOADING
	status.State = types.DOWNLOADING
	// save the name of the Target filename to our status. In theory, this can be
	// derived, but it is good for the status to say where it *is*, as opposed to
	// config, which says where it *should be*
	status.Target = locFilename
	publishDownloaderStatus(ctx, status)

	// make sure the directory exists - just a safety check
	if _, err := os.Stat(locDirname); err != nil {
		log.Debugf("Create %s", locDirname)
		if err = os.MkdirAll(locDirname, 0755); err != nil {
			log.Fatal(err)
		}
	}

	log.Infof("Downloading <%s> to <%s> using %v allow non-free port",
		config.Name, locFilename, config.AllowNonFreePort)

	var addrCount int
	if !config.AllowNonFreePort {
		addrCount = types.CountLocalAddrFreeNoLinkLocal(ctx.deviceNetworkStatus)
		log.Infof("Have %d free management port addresses", addrCount)
		err = errors.New("No free IP management port addresses for download")
	} else {
		addrCount = types.CountLocalAddrAnyNoLinkLocal(ctx.deviceNetworkStatus)
		log.Infof("Have %d any management port addresses", addrCount)
		err = errors.New("No IP management port addresses for download")
	}
	if addrCount == 0 {
		errStr = err.Error()
		log.Errorf(errStr)
		handleSyncOpResponse(ctx, config, status, locFilename,
			key, errStr)
		return
	}

	switch dsCtx.TransportMethod {
	case zconfig.DsType_DsContainerRegistry.String():
		auth = &zedUpload.AuthInput{
			AuthType: "apikey",
			Uname:    dsCtx.APIKey,
			Password: dsCtx.Password,
		}
		trType = zedUpload.SyncOCIRegistryTr
		// get the name of the repository and the URL for the registry
		serverURL, remoteName, err = ociRepositorySplit(dsCtx.DownloadURL)
		if err != nil {
			errStr = fmt.Sprintf("invalid OCI registry URL: %s", serverURL)
		}
	case zconfig.DsType_DsS3.String():
		auth = &zedUpload.AuthInput{
			AuthType: "s3",
			Uname:    dsCtx.APIKey,
			Password: dsCtx.Password,
		}
		trType = zedUpload.SyncAwsTr
		serverURL = dsCtx.DownloadURL
		// pass in the config.Name instead of 'filename' which
		// does not contain the prefix of the relative path with '/'s
		remoteName = config.Name
		metricsUrl = fmt.Sprintf("S3:%s/%s", dsCtx.Dpath, config.Name)

	case zconfig.DsType_DsAzureBlob.String():
		auth = &zedUpload.AuthInput{
			AuthType: "password",
			Uname:    dsCtx.APIKey,
			Password: dsCtx.Password,
		}
		trType = zedUpload.SyncAzureTr
		serverURL = dsCtx.DownloadURL
		// pass in the config.Name instead of 'filename' which
		// does not contain the prefix of the relative path with '/'s
		remoteName = config.Name

	case zconfig.DsType_DsSFTP.String():
		auth = &zedUpload.AuthInput{
			AuthType: "sftp",
			Uname:    dsCtx.APIKey,
			Password: dsCtx.Password,
		}
		trType = zedUpload.SyncSftpTr
		// pass in the config.Name instead of 'filename' which
		// does not contain the prefix of the relative path with '/'s
		remoteName = config.Name
		serverURL, err = getServerUrl(dsCtx)
		// failed to get server url
		if err != nil {
			errStr = errStr + "\n" + err.Error()
		}

	case zconfig.DsType_DsHttp.String(), zconfig.DsType_DsHttps.String(), "":
		auth = &zedUpload.AuthInput{
			AuthType: "http",
		}
		trType = zedUpload.SyncHttpTr
		// pass in the config.Name instead of 'filename' which
		// does not contain the prefix of the relative path with '/'s
		remoteName = config.Name
		serverURL, err = getServerUrl(dsCtx)
		// failed to get server url
		if err != nil {
			errStr = errStr + "\n" + err.Error()
		}

	default:
		errStr = "unsupported transport method " + dsCtx.TransportMethod

	}

	// if there were any errors, do not bother continuing
	// ideally in go we would have this as a check for error
	// and return, but we will get to it later
	if errStr != "" {
		log.Errorf("Error preparing to download. All errors:%s", errStr)
		handleSyncOpResponse(ctx, config, status, locFilename,
			key, errStr)
		return
	}

	// Loop through all interfaces until a success
	for addrIndex := 0; addrIndex < addrCount; addrIndex += 1 {
		var ipSrc net.IP
		if !config.AllowNonFreePort {
			ipSrc, err = types.GetLocalAddrFreeNoLinkLocal(ctx.deviceNetworkStatus,
				addrIndex, "")
		} else {
			// Note that GetLocalAddrAny has the free ones first
			ipSrc, err = types.GetLocalAddrAnyNoLinkLocal(ctx.deviceNetworkStatus,
				addrIndex, "")
		}
		if err != nil {
			log.Errorf("GetLocalAddr failed: %s", err)
			errStr = errStr + "\n" + err.Error()
			continue
		}
		ifname := types.GetMgmtPortFromAddr(ctx.deviceNetworkStatus, ipSrc)
		log.Infof("Using IP source %v if %s transport %v",
			ipSrc, ifname, dsCtx.TransportMethod)

		// do the download
		st := &PublishStatus{
			ctx:    ctx,
			status: status,
		}
		downloadStartTime := time.Now()
		contentType, err := download(ctx, trType, st, syncOp, serverURL, auth,
			dsCtx.Dpath, dsCtx.Region,
			config.Size, ifname, ipSrc, remoteName, locFilename)
		if err != nil {
			sourceFailureError(ipSrc.String(), ifname, metricsUrl, err)
			errStr = errStr + "\n" + err.Error()
			continue
		}
		// Record how much we downloaded
		size := int64(0)
		info, err := os.Stat(locFilename)
		if err != nil {
			log.Error(err)
		} else {
			size = info.Size()
		}
		downloadTime := int64(time.Since(downloadStartTime) / time.Millisecond)
		status.Size = uint64(size)
		status.ContentType = contentType
		zedcloud.ZedCloudSuccess(log, ifname,
			metricsUrl, 1024, size, downloadTime)
		handleSyncOpResponse(ctx, config, status,
			locFilename, key, "")
		return

	}
	log.Errorf("All source IP addresses failed. All errors:%s", errStr)
	handleSyncOpResponse(ctx, config, status, locFilename,
		key, errStr)
}

// DownloadURL format : http://<serverURL>/dpath/filename
func getServerUrl(dsCtx *types.DatastoreContext) (string, error) {
	u, err := url.Parse(dsCtx.DownloadURL)
	if err != nil {
		log.Errorf("URL Parsing failed: %s", err)
		return "", err
	}
	return u.Scheme + "://" + u.Host, nil
}

func handleSyncOpResponse(ctx *downloaderContext, config types.DownloaderConfig,
	status *types.DownloaderStatus, locFilename string,
	key string, errStr string) {

	// have finished the download operation
	// based on the result, perform some storage
	// management also

	if errStr != "" {
		// Delete file, and update the storage
		doDelete(ctx, key, locFilename, status)
		status.RetryCount++
		status.HandleDownloadFail(errStr)
		publishDownloaderStatus(ctx, status)
		log.Errorf("handleSyncOpResponse(%s): failed with %s",
			status.Name, errStr)
		return
	}

	// make sure the file exists
	_, err := os.Stat(locFilename)
	if err != nil {
		// error, so delete the file
		doDelete(ctx, key, locFilename, status)
		errStr := fmt.Sprintf("%v", err)
		status.RetryCount++
		status.HandleDownloadFail(errStr)
		publishDownloaderStatus(ctx, status)
		log.Errorf("handleSyncOpResponse(%s): failed with %s",
			status.Name, errStr)
		return
	}

	log.Infof("handleSyncOpResponse(%s): successful <%s>",
		config.Name, locFilename)
	// We do not clear any status.RetryCount, Error, etc. The caller
	// should look at State == DOWNLOADED to determine it is done.

	status.ModTime = time.Now()
	status.State = types.DOWNLOADED
	status.Progress = 100 // Just in case
	status.ClearPendingStatus()
	publishDownloaderStatus(ctx, status)
}

// cloud storage interface functions/APIs
func constructDatastoreContext(ctx *downloaderContext, configName string, NameIsURL bool, dst types.DatastoreConfig) (*types.DatastoreContext, error) {
	dpath := dst.Dpath
	downloadURL := configName
	if !NameIsURL {
		downloadURL = dst.Fqdn
		if len(dpath) > 0 {
			downloadURL = downloadURL + "/" + dpath
		}
		if len(configName) > 0 {
			downloadURL = downloadURL + "/" + configName
		}
	}

	// get the decrypted encryption block
	decBlock, err := getDatastoreCredential(ctx, dst)
	if err != nil {
		return nil, err
	}

	dsCtx := types.DatastoreContext{
		DownloadURL:     downloadURL,
		TransportMethod: dst.DsType,
		Dpath:           dpath,
		APIKey:          decBlock.DsAPIKey,
		Password:        decBlock.DsPassword,
		Region:          dst.Region,
	}
	return &dsCtx, nil
}

func sourceFailureError(ip, ifname, url string, err error) {
	log.Errorf("Source IP %s failed: %s", ip, err)
	zedcloud.ZedCloudFailure(log, ifname, url, 1024, 0, false)
}

func getDatastoreCredential(ctx *downloaderContext,
	dst types.DatastoreConfig) (types.EncryptionBlock, error) {
	if dst.CipherBlockStatus.IsCipher {
		status, decBlock, err := cipher.GetCipherCredentials(&ctx.decryptCipherContext,
			agentName, dst.CipherBlockStatus)
		ctx.pubCipherBlockStatus.Publish(status.Key(), status)
		if err != nil {
			log.Errorf("%s, datastore config cipherblock decryption unsuccessful, falling back to cleartext: %v",
				dst.Key(), err)
			decBlock.DsAPIKey = dst.ApiKey
			decBlock.DsPassword = dst.Password
			// We assume IsCipher is only set when there was some
			// data. Hence this is a fallback if there is
			// some cleartext.
			if decBlock.DsAPIKey != "" || decBlock.DsPassword != "" {
				cipher.RecordFailure(agentName,
					types.CleartextFallback)
			} else {
				cipher.RecordFailure(agentName,
					types.MissingFallback)
			}
			return decBlock, nil
		}
		log.Infof("%s, datastore config cipherblock decryption successful", dst.Key())
		return decBlock, nil
	}
	log.Infof("%s, datastore config cipherblock not present", dst.Key())
	decBlock := types.EncryptionBlock{}
	decBlock.DsAPIKey = dst.ApiKey
	decBlock.DsPassword = dst.Password
	if decBlock.DsAPIKey != "" || decBlock.DsPassword != "" {
		cipher.RecordFailure(agentName, types.NoCipher)
	} else {
		cipher.RecordFailure(agentName, types.NoData)
	}
	return decBlock, nil
}
