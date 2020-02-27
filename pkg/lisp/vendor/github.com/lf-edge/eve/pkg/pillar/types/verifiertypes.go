// Copyright (c) 2017 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

// Types which feed in and out of the verifier

package types

import (
	"path"
	"time"

	uuid "github.com/satori/go.uuid"
	log "github.com/sirupsen/logrus"
)

// XXX more than images; rename type and clean up comments
// XXX make clean that Cert/Key are names of them and not PEM content

// Types for verifying the images.
// For now we just verify the sha checksum.
// For defense-in-depth we assume that the ZedManager with the help of
// dom0 has moved the image file to a read-only directory before asking
// for the file to be verified.

// The key/index to this is the ImageID which is allocated by the controller.
type VerifyImageConfig struct {
	ImageID          uuid.UUID // UUID of the image
	Name             string
	ImageSha256      string // sha256 of immutable image
	RefCount         uint
	CertificateChain []string //name of intermediate certificates
	ImageSignature   []byte   //signature of image
	SignatureKey     string   //certificate containing public key
	IsContainer      bool     // Is this image for a Container?
}

func (config VerifyImageConfig) Key() string {
	return config.ImageID.String()
}

func (config VerifyImageConfig) VerifyFilename(fileName string) bool {
	expect := config.Key() + ".json"
	ret := expect == fileName
	if !ret {
		log.Errorf("Mismatch between filename and contained key: %s vs. %s\n",
			fileName, expect)
	}
	return ret
}

// The key/index to this is the ImageID which comes from VerifyImageConfig.
type VerifyImageStatus struct {
	ImageID       uuid.UUID // UUID of the image
	Name          string
	ObjType       string
	PendingAdd    bool
	PendingModify bool
	PendingDelete bool
	FileLocation  string  // Current location; should be info about file
	IsContainer   bool    // Is this image for a Container?
	ImageSha256   string  // sha256 of immutable image
	State         SwState // DELIVERED; LastErr* set if failed
	LastErr       string  // Verification error
	LastErrTime   time.Time
	Size          int64
	RefCount      uint
	LastUse       time.Time // When RefCount dropped to zero
	Expired       bool      // Handshake to client
}

func (status VerifyImageStatus) Key() string {
	return status.ImageID.String()
}

func (status VerifyImageStatus) VerifyFilename(fileName string) bool {
	expect := status.Key() + ".json"
	ret := expect == fileName
	if !ret {
		log.Errorf("Mismatch between filename and contained key: %s vs. %s\n",
			fileName, expect)
	}
	return ret
}

// ImageDownloadDirNames - Returns pendingDirname, verifierDirname, verifiedDirname
// for the image.
func (status VerifyImageStatus) ImageDownloadDirNames() (string, string, string) {
	downloadDirname := DownloadDirname + "/" + status.ObjType

	var pendingDirname, verifierDirname, verifiedDirname string
	pendingDirname = downloadDirname + "/pending/" + status.ImageID.String()
	verifierDirname = downloadDirname + "/verifier/" + status.ImageID.String()
	verifiedDirname = downloadDirname + "/verified/" + status.ImageSha256
	return pendingDirname, verifierDirname, verifiedDirname
}

// ImageDownloadFilenames - Returns pendingFilename, verifierFilename, verifiedFilename
// for the image
func (status VerifyImageStatus) ImageDownloadFilenames() (string, string, string) {
	var pendingFilename, verifierFilename, verifiedFilename string

	pendingDirname, verifierDirname, verifiedDirname :=
		status.ImageDownloadDirNames()
	// Handle names which are paths
	filename := path.Base(status.Name)
	pendingFilename = pendingDirname + "/" + filename
	verifierFilename = verifierDirname + "/" + filename
	verifiedFilename = verifiedDirname + "/" + filename
	return pendingFilename, verifierFilename, verifiedFilename
}

func (status VerifyImageStatus) CheckPendingAdd() bool {
	return status.PendingAdd
}

func (status VerifyImageStatus) CheckPendingModify() bool {
	return status.PendingModify
}

func (status VerifyImageStatus) CheckPendingDelete() bool {
	return status.PendingDelete
}

func (status VerifyImageStatus) Pending() bool {
	return status.PendingAdd || status.PendingModify || status.PendingDelete
}
