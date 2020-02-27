package downloader

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"time"

	"github.com/golang/protobuf/proto"
	zconfig "github.com/lf-edge/eve/api/go/config"
	"github.com/lf-edge/eve/pkg/pillar/cmd/tpmmgr"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/lf-edge/eve/pkg/pillar/zedUpload"
	"github.com/lf-edge/eve/pkg/pillar/zedcloud"
	log "github.com/sirupsen/logrus"
)

// Drona APIs for object Download
func handleSyncOp(ctx *downloaderContext, key string,
	config types.DownloaderConfig, status *types.DownloaderStatus,
	dst *types.DatastoreConfig) {
	var (
		err                                        error
		errStr, locFilename, remoteName, serverURL string
		syncOp                                     zedUpload.SyncOpType = zedUpload.SyncOpDownload
		trType                                     zedUpload.SyncTransportType
		auth                                       *zedUpload.AuthInput
	)

	if status.ObjType == "" {
		log.Fatalf("handleSyncOp: No ObjType for %s\n",
			status.ImageID)
	}

	// construct the datastore context
	dsCtx, err := constructDatastoreContext(config, status, *dst)
	if err != nil {
		errStr := fmt.Sprintf("%s, Datastore construction failed, %s", config.Name, err)
		handleSyncOpResponse(ctx, config, status, locFilename, key, errStr)
		return
	}

	// by default the metricsURL _is_ the DownloadURL, but can override in switch
	metricsUrl := dsCtx.DownloadURL

	locDirname := types.DownloadDirname + "/" + status.ObjType
	locFilename = locDirname + "/pending"
	// XXX common routines to determine pathnames?
	locFilename = locFilename + "/" + config.ImageID.String()

	// update status to DOWNLOAD STARTED
	status.FileLocation = locFilename
	status.State = types.DOWNLOAD_STARTED
	publishDownloaderStatus(ctx, status)

	if _, err := os.Stat(locFilename); err != nil {
		log.Debugf("Create %s\n", locFilename)
		if err = os.MkdirAll(locFilename, 0755); err != nil {
			log.Fatal(err)
		}
	}

	// Handle names which are paths
	filename := path.Base(config.Name)
	locFilename = locFilename + "/" + filename

	log.Infof("Downloading <%s> to <%s> using %v allow non-free port\n",
		config.Name, locFilename, config.AllowNonFreePort)

	var addrCount int
	if !config.AllowNonFreePort {
		addrCount = types.CountLocalAddrFreeNoLinkLocal(ctx.deviceNetworkStatus)
		log.Infof("Have %d free management port addresses\n", addrCount)
		err = errors.New("No free IP management port addresses for download")
	} else {
		addrCount = types.CountLocalAddrAnyNoLinkLocal(ctx.deviceNetworkStatus)
		log.Infof("Have %d any management port addresses\n", addrCount)
		err = errors.New("No IP management port addresses for download")
	}
	if addrCount == 0 {
		errStr = err.Error()
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
		remoteName = filename
		metricsUrl = fmt.Sprintf("S3:%s/%s", dsCtx.Dpath, filename)

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
		log.Errorf("Error preparing to download. All errors:%s\n", errStr)
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
			log.Errorf("GetLocalAddr failed: %s\n", err)
			errStr = errStr + "\n" + err.Error()
			continue
		}
		ifname := types.GetMgmtPortFromAddr(ctx.deviceNetworkStatus, ipSrc)
		log.Infof("Using IP source %v if %s transport %v\n",
			ipSrc, ifname, dsCtx.TransportMethod)

		// do the download
		st := &PublishStatus{
			ctx:    ctx,
			status: status,
		}
		err = download(ctx, trType, st, syncOp, serverURL, auth,
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
		zedcloud.ZedCloudSuccess(ifname,
			metricsUrl, 1024, size)
		handleSyncOpResponse(ctx, config, status,
			locFilename, key, "")
		return

	}
	log.Errorf("All source IP addresses failed. All errors:%s\n", errStr)
	handleSyncOpResponse(ctx, config, status, locFilename,
		key, errStr)
}

// DownloadURL format : http://<serverURL>/dpath/filename
func getServerUrl(dsCtx *types.DatastoreContext) (string, error) {
	u, err := url.Parse(dsCtx.DownloadURL)
	if err != nil {
		log.Errorf("URL Parsing failed: %s\n", err)
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

	if status.ObjType == "" {
		log.Fatalf("handleSyncOpResponse: No ObjType for %s\n",
			status.ImageID)
	}

	locDirname := types.DownloadDirname + "/" + status.ObjType
	if errStr != "" {
		// Delete file, and update the storage
		doDelete(ctx, key, locDirname, status)
		// free the reserved storage
		unreserveSpace(ctx, status)
		status.RetryCount++
		status.HandleDownloadFail(errStr)
		publishDownloaderStatus(ctx, status)
		log.Errorf("handleSyncOpResponse(%s): failed with %s\n",
			status.Name, errStr)
		return
	}

	info, err := os.Stat(locFilename)
	if err != nil {
		// Delete file, and update the storage
		doDelete(ctx, key, locDirname, status)
		// free the reserved storage
		unreserveSpace(ctx, status)
		errStr := fmt.Sprintf("%v", err)
		status.RetryCount++
		status.HandleDownloadFail(errStr)
		publishDownloaderStatus(ctx, status)
		log.Errorf("handleSyncOpResponse(%s): failed with %s\n",
			status.Name, errStr)
		return
	}
	size := uint64(info.Size())
	// we need to release the reserved space
	// and convert it to used space
	allocateSpace(ctx, status, size)

	log.Infof("handleSyncOpResponse(%s): successful <%s>\n",
		config.Name, locFilename)
	// We do not clear any status.RetryCount, LastErr, etc. The caller
	// should look at State == DOWNLOADED to determine it is done.

	status.ModTime = time.Now()
	status.State = types.DOWNLOADED
	status.Progress = 100 // Just in case
	status.ClearPendingStatus()
	publishDownloaderStatus(ctx, status)
}

// cloud storage interface functions/APIs
func constructDatastoreContext(config types.DownloaderConfig, status *types.DownloaderStatus, dst types.DatastoreConfig) (*types.DatastoreContext, error) {
	dpath := dst.Dpath
	downloadURL := config.Name
	if !config.NameIsURL {
		downloadURL = dst.Fqdn
		if len(dpath) > 0 {
			downloadURL = downloadURL + "/" + dpath
		}
		if len(config.Name) > 0 {
			downloadURL = downloadURL + "/" + config.Name
		}
	}

	// get the decrypted credentials
	cred, err := getDatastoreCredential(dst)
	if err != nil {
		return nil, err
	}

	dsCtx := types.DatastoreContext{
		DownloadURL:     downloadURL,
		TransportMethod: dst.DsType,
		Dpath:           dpath,
		APIKey:          cred.Identity,
		Password:        cred.Password,
		Region:          dst.Region,
	}
	return &dsCtx, nil
}

func sourceFailureError(ip, ifname, url string, err error) {
	log.Errorf("Source IP %s failed: %s\n", ip, err)
	zedcloud.ZedCloudFailure(ifname, url, 1024, 0)
}

func getDatastoreCredential(dst types.DatastoreConfig) (zconfig.CredentialBlock, error) {
	cred := zconfig.CredentialBlock{}
	if !dst.IsCipher {
		cred.Identity = dst.ApiKey
		cred.Password = dst.Password
		return cred, nil
	}
	if !dst.IsValidCipher {
		errStr := fmt.Sprintf("%s, Cipher Block is not ready", dst.Key())
		log.Errorln(errStr)
		return cred, errors.New(errStr)
	}
	clearData, err := tpmmgr.DecryptCipherBlock(dst.CipherBlock)
	if err != nil {
		log.Errorf("%s, dataStore CipherData decryption failed, %v\n",
			dst.Key(), err)
		return cred, err
	}
	if err := proto.Unmarshal(clearData, &cred); err != nil {
		log.Errorf("%s, dataStore Credential unmarshall failed, %v\n",
			dst.Key(), err)
		return cred, err
	}
	return cred, nil
}
