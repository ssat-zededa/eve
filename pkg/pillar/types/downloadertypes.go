// Copyright (c) 2017 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"time"

	"github.com/lf-edge/eve/pkg/pillar/base"
	uuid "github.com/satori/go.uuid"
	log "github.com/sirupsen/logrus"
)

// The key/index to this is the ImageSha256 which is allocated by the controller or resolver.
type DownloaderConfig struct {
	ImageSha256      string
	ImageID          uuid.UUID // Used for logging
	DatastoreID      uuid.UUID
	Name             string
	Target           string // file path where to download the file
	NameIsURL        bool   // If not we form URL based on datastore info
	AllowNonFreePort bool
	Size             uint64 // In bytes
	FinalObjDir      string // final Object Store
	RefCount         uint
}

func (config DownloaderConfig) Key() string {
	return config.ImageSha256
}

func (config DownloaderConfig) VerifyFilename(fileName string) bool {
	expect := config.Key() + ".json"
	ret := expect == fileName
	if !ret {
		log.Errorf("Mismatch between filename and contained key: %s vs. %s\n",
			fileName, expect)
	}
	return ret
}

// LogCreate :
func (config DownloaderConfig) LogCreate() {
	logObject := base.NewLogObject(base.DownloaderConfigLogType, config.Name,
		config.ImageID, config.LogKey())
	if logObject == nil {
		return
	}
	logObject.CloneAndAddField("target", config.Target).
		AddField("datastore-id", config.DatastoreID).
		AddField("refcount-int64", config.RefCount).
		AddField("size-int64", config.Size).
		Infof("Download config create")
}

// LogModify :
func (config DownloaderConfig) LogModify(old interface{}) {
	logObject := base.EnsureLogObject(base.DownloaderConfigLogType, config.Name,
		config.ImageID, config.LogKey())

	oldConfig, ok := old.(DownloaderConfig)
	if !ok {
		log.Errorf("LogModify: Old object interface passed is not of DownloaderConfig type")
	}
	if oldConfig.Target != config.Target ||
		oldConfig.DatastoreID != config.DatastoreID ||
		oldConfig.RefCount != config.RefCount ||
		oldConfig.Size != config.Size {

		logObject.CloneAndAddField("target", config.Target).
			AddField("datastore-id", config.DatastoreID).
			AddField("refcount-int64", config.RefCount).
			AddField("size-int64", config.Size).
			AddField("old-target", oldConfig.Target).
			AddField("old-datastore-id", oldConfig.DatastoreID).
			AddField("old-refcount-int64", oldConfig.RefCount).
			AddField("old-size-int64", oldConfig.Size).
			Infof("Download config modify")
	}
}

// LogDelete :
func (config DownloaderConfig) LogDelete() {
	logObject := base.EnsureLogObject(base.DownloaderConfigLogType, config.Name,
		config.ImageID, config.LogKey())
	logObject.CloneAndAddField("target", config.Target).
		AddField("datastore-id", config.DatastoreID).
		AddField("refcount-int64", config.RefCount).
		AddField("size-int64", config.Size).
		Infof("Download config delete")

	base.DeleteLogObject(config.LogKey())
}

// LogKey :
func (config DownloaderConfig) LogKey() string {
	return string(base.DownloaderConfigLogType) + "-" + config.Key()
}

// Cert Object may contain multiple objects

type CertConfig struct {
	ServerCert DownloaderConfig
	CertChain  []DownloaderConfig
}

// The key/index to this is the ImageSha256 which comes from DownloaderConfig.
type DownloaderStatus struct {
	ImageSha256      string
	ImageID          uuid.UUID // Used for logging
	DatastoreID      uuid.UUID
	Target           string // file path where we download the file
	Name             string
	ObjType          string
	PendingAdd       bool
	PendingModify    bool
	PendingDelete    bool
	RefCount         uint      // Zero means not downloaded
	LastUse          time.Time // When RefCount dropped to zero
	Expired          bool      // Handshake to client
	NameIsURL        bool      // If not we form URL based on datastore info
	AllowNonFreePort bool
	State            SwState // DOWNLOADED etc
	ReservedSpace    uint64  // Contribution to global ReservedSpace
	Size             uint64  // Once DOWNLOADED; in bytes
	Progress         uint    // In percent i.e., 0-100
	ModTime          time.Time
	// ErrorAndTime provides SetErrorNow() and ClearError()
	ErrorAndTime
	RetryCount int
}

func (status DownloaderStatus) Key() string {
	return status.ImageSha256
}

func (status DownloaderStatus) VerifyFilename(fileName string) bool {
	expect := status.Key() + ".json"
	ret := expect == fileName
	if !ret {
		log.Errorf("Mismatch between filename and contained key: %s vs. %s\n",
			fileName, expect)
	}
	return ret
}

func (status DownloaderStatus) CheckPendingAdd() bool {
	return status.PendingAdd
}

func (status DownloaderStatus) CheckPendingModify() bool {
	return status.PendingModify
}

func (status DownloaderStatus) CheckPendingDelete() bool {
	return status.PendingDelete
}

func (status DownloaderStatus) Pending() bool {
	return status.PendingAdd || status.PendingModify || status.PendingDelete
}

// ClearPendingStatus : Clear Pending Status for DownloaderStatus
func (status *DownloaderStatus) ClearPendingStatus() {
	if status.PendingAdd {
		status.PendingAdd = false
	}
	if status.PendingModify {
		status.PendingModify = false
	}
}

// HandleDownloadFail : Do Failure specific tasks
func (status *DownloaderStatus) HandleDownloadFail(errStr string) {
	status.SetErrorNow(errStr)
	status.ClearPendingStatus()
}

// LogCreate :
func (status DownloaderStatus) LogCreate() {
	logObject := base.NewLogObject(base.DownloaderStatusLogType, status.Name,
		status.ImageID, status.LogKey())
	if logObject == nil {
		return
	}
	logObject.CloneAndAddField("state", status.State.String()).
		AddField("refcount-int64", status.RefCount).
		AddField("size-int64", status.Size).
		Infof("Download status create")
}

// LogModify :
func (status DownloaderStatus) LogModify(old interface{}) {
	logObject := base.EnsureLogObject(base.DownloaderStatusLogType, status.Name,
		status.ImageID, status.LogKey())

	oldStatus, ok := old.(DownloaderStatus)
	if !ok {
		log.Errorf("LogModify: Old object interface passed is not of DownloaderStatus type")
	}
	if oldStatus.State != status.State ||
		oldStatus.RefCount != status.RefCount ||
		oldStatus.Size != status.Size {

		logObject.CloneAndAddField("state", status.State.String()).
			AddField("refcount-int64", status.RefCount).
			AddField("size-int64", status.Size).
			AddField("old-state", oldStatus.State.String()).
			AddField("old-refcount-int64", oldStatus.RefCount).
			AddField("old-size-int64", oldStatus.Size).
			Infof("Download status modify")
	}

	if status.HasError() {
		errAndTime := status.ErrorAndTime
		logObject.CloneAndAddField("state", status.State.String()).
			AddField("error", errAndTime.Error).
			AddField("error-time", errAndTime.ErrorTime).
			Errorf("Download status modify")
	}
}

// LogDelete :
func (status DownloaderStatus) LogDelete() {
	logObject := base.EnsureLogObject(base.DownloaderStatusLogType, status.Name,
		status.ImageID, status.LogKey())
	logObject.CloneAndAddField("state", status.State.String()).
		AddField("refcount-int64", status.RefCount).
		AddField("size-int64", status.Size).
		Infof("Download status delete")

	base.DeleteLogObject(status.LogKey())
}

// LogKey :
func (status DownloaderStatus) LogKey() string {
	return string(base.DownloaderStatusLogType) + "-" + status.Key()
}

type GlobalDownloadConfig struct {
	MaxSpace uint64 // Number of kbytes allowed in types.DownloadDirname
}

// These are all in kbytes
type GlobalDownloadStatus struct {
	UsedSpace      uint64 // Number of kbytes used in types.DownloadDirname
	ReservedSpace  uint64 // Reserved for ongoing downloads
	RemainingSpace uint64 // MaxSpace - UsedSpace - ReservedSpace
}

// DatastoreContext : datastore detail
type DatastoreContext struct {
	DownloadURL     string
	TransportMethod string // Download Method S3/HTTP/SFTP etc.
	Dpath           string
	APIKey          string
	Password        string
	Region          string
}

// AllowNonFreePort looks at GlobalConfig to determine which policy
// to apply for the download of the object.
func AllowNonFreePort(gc ConfigItemValueMap, objType string) bool {

	switch objType {
	case AppImgObj:
		return gc.GlobalValueTriState(AllowNonFreeAppImages) == TS_ENABLED
	case BaseOsObj:
		return gc.GlobalValueTriState(AllowNonFreeBaseImages) == TS_ENABLED
	case CertObj:
		return (gc.GlobalValueTriState(AllowNonFreeBaseImages) == TS_ENABLED) ||
			(gc.GlobalValueTriState(AllowNonFreeAppImages) == TS_ENABLED)
	default:
		log.Fatalf("AllowNonFreePort: Unknown ObjType %s\n",
			objType)
		return false
	}
}

// ResolveConfig key/index to this is the combination of
// DatastoreID which is allocated by the controller, name
// and the sequence counter.
// It will resolve the tag in name to sha256
type ResolveConfig struct {
	DatastoreID      uuid.UUID
	Name             string
	AllowNonFreePort bool
	Counter          uint32
}

// Key : DatastoreID, name and sequence counter are used
// to differentiate different config
func (config ResolveConfig) Key() string {
	return fmt.Sprintf("%s+%s+%v", config.DatastoreID.String(), config.Name, config.Counter)
}

// VerifyFilename will verify the key name
func (config ResolveConfig) VerifyFilename(fileName string) bool {
	expect := config.Key() + ".json"
	ret := expect == fileName
	if !ret {
		log.Errorf("Mismatch between filename and contained key: %s vs. %s\n",
			fileName, expect)
	}
	return ret
}

// LogCreate :
func (config ResolveConfig) LogCreate() {
	logObject := base.NewLogObject(base.ResolveConfigLogType, config.Name,
		config.DatastoreID, config.LogKey())
	if logObject == nil {
		return
	}
	logObject.Infof("Resolve config create")
}

// LogModify :
func (config ResolveConfig) LogModify(old interface{}) {
	logObject := base.EnsureLogObject(base.ResolveConfigLogType, config.Name,
		config.DatastoreID, config.LogKey())

	// Why would it change?
	logObject.Infof("Resolve config modify")
}

// LogDelete :
func (config ResolveConfig) LogDelete() {
	logObject := base.EnsureLogObject(base.ResolveConfigLogType, config.Name,
		config.DatastoreID, config.LogKey())
	logObject.Infof("Resolve config delete")

	base.DeleteLogObject(config.LogKey())
}

// LogKey :
func (config ResolveConfig) LogKey() string {
	return string(base.ResolveConfigLogType) + "-" + config.Key()
}

// ResolveStatus key/index to this is the combination of
// DatastoreID, name and the sequence counter which comes
// from the ResolveConfig
type ResolveStatus struct {
	DatastoreID uuid.UUID
	Name        string
	ImageSha256 string
	Counter     uint32
	RetryCount  int
	// ErrorAndTime provides SetErrorNow() and ClearError()
	ErrorAndTime
}

// Key : DatastoreID, name and sequence counter are used
// to differentiate different config
func (status ResolveStatus) Key() string {
	return fmt.Sprintf("%s+%s+%v", status.DatastoreID.String(), status.Name, status.Counter)
}

// VerifyFilename will verify the key name
func (status ResolveStatus) VerifyFilename(fileName string) bool {
	expect := status.Key() + ".json"
	ret := expect == fileName
	if !ret {
		log.Errorf("Mismatch between filename and contained key: %s vs. %s\n",
			fileName, expect)
	}
	return ret
}

// LogCreate :
func (status ResolveStatus) LogCreate() {
	logObject := base.NewLogObject(base.ResolveStatusLogType, status.Name,
		status.DatastoreID, status.LogKey())
	if logObject == nil {
		return
	}
	logObject.CloneAndAddField("image-sha256", status.ImageSha256).
		AddField("retry-count-int64", status.RetryCount).
		Infof("Resolve status create")
}

// LogModify :
func (status ResolveStatus) LogModify(old interface{}) {
	logObject := base.EnsureLogObject(base.ResolveStatusLogType, status.Name,
		status.DatastoreID, status.LogKey())

	oldStatus, ok := old.(ResolveStatus)
	if !ok {
		log.Errorf("LogModify: Old object interface passed is not of ResolveStatus type")
	}
	if oldStatus.ImageSha256 != status.ImageSha256 ||
		oldStatus.RetryCount != status.RetryCount {

		logObject.CloneAndAddField("image-sha256", status.ImageSha256).
			AddField("retry-count-int64", status.RetryCount).
			AddField("old-image-sha256", oldStatus.ImageSha256).
			AddField("old-retry-count-int64", oldStatus.RetryCount).
			Infof("Resolve status modify")
	}

	if status.HasError() {
		errAndTime := status.ErrorAndTime
		logObject.CloneAndAddField("image-sha256", status.ImageSha256).
			AddField("retry-count-int64", status.RetryCount).
			AddField("error", errAndTime.Error).
			AddField("error-time", errAndTime.ErrorTime).
			Errorf("Resolve status modify")
	}
}

// LogDelete :
func (status ResolveStatus) LogDelete() {
	logObject := base.EnsureLogObject(base.ResolveStatusLogType, status.Name,
		status.DatastoreID, status.LogKey())
	logObject.CloneAndAddField("image-sha256", status.ImageSha256).
		AddField("retry-count-int64", status.RetryCount).
		Infof("Resolve status delete")

	base.DeleteLogObject(status.LogKey())
}

// LogKey :
func (status ResolveStatus) LogKey() string {
	return string(base.ResolveStatusLogType) + "-" + status.Key()
}
