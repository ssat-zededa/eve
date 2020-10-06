// Copyright (c) 2019,2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

const (
	// TmpDirname - temporary dir. for agents to use.
	TmpDirname = "/var/tmp/zededa"

	// PersistDir - Location to store persistent files.
	PersistDir = "/persist"
	// PersistConfigDir is where we keep some configuration across reboots
	PersistConfigDir = PersistDir + "/config"
	// PersistStatusDir is where we keep some configuration across reboots
	PersistStatusDir = PersistDir + "/status"
	// CertificateDirname - Location of certificates
	CertificateDirname = PersistDir + "/certs"
	// SealedDirName - directory sealed under TPM PCRs
	SealedDirName = PersistDir + "/vault"
	// VolumeEncryptedDirName - sealed directory used to store volumes
	VolumeEncryptedDirName = SealedDirName + "/volumes"
	// ClearDirName - directory which is not encrypted
	ClearDirName = PersistDir + "/clear"
	// VolumeClearDirName - Not encrypted directory used to store volumes
	VolumeClearDirName = ClearDirName + "/volumes"
	// PersistDebugDir - Location for service specific debug/traces
	PersistDebugDir = PersistDir + "/agentdebug"

	// IdentityDirname - Config dir
	IdentityDirname = "/config"
	// SelfRegFile - name of self-register-filed file
	SelfRegFile = IdentityDirname + "/self-register-failed"
	// ServerFileName - server file
	ServerFileName = IdentityDirname + "/server"
	// DeviceCertName - device certificate
	DeviceCertName = IdentityDirname + "/device.cert.pem"
	// DeviceKeyName - device private key (if not in TPM)
	DeviceKeyName = IdentityDirname + "/device.key.pem"
	// OnboardCertName - Onboard certificate
	OnboardCertName = IdentityDirname + "/onboard.cert.pem"
	// OnboardKeyName - onboard key
	OnboardKeyName = IdentityDirname + "/onboard.key.pem"
	// RootCertFileName - what we trust for signatures and object encryption
	RootCertFileName = IdentityDirname + "/root-certificate.pem"
	// V2TLSCertShaFilename - find TLS root cert for API V2 based on this sha
	V2TLSCertShaFilename = CertificateDirname + "/v2tlsbaseroot-certificates.sha256"
	// UUIDFileName - device UUID
	UUIDFileName = IdentityDirname + "/uuid"

	// APIV1FileName - user can statically allow for API v1
	APIV1FileName = IdentityDirname + "/Force-API-V1"

	// ServerSigningCertFileName - filename for server signing leaf certificate
	ServerSigningCertFileName = CertificateDirname + "/server-signing-cert.pem"

	// ShareCertDirname - directory to place private proxy server certificates
	ShareCertDirname = "/usr/local/share/ca-certificates"

	// AppImgObj - name of app image type
	AppImgObj = "appImg.obj"
	// BaseOsObj - name of base image type
	BaseOsObj = "baseOs.obj"
	//ITokenFile contains the integrity token sent in attestation response
	ITokenFile = "/var/run/eve.integrity_token"
	//EveVersionFile contains the running version of EVE
	EveVersionFile = "/run/eve-release"
	//DefaultVaultName is the name of the default vault
	DefaultVaultName = "Application Data Store"
	//WatchdogFileDir is the dir to add .touch files for watchdog to monitor
	WatchdogFileDir = "/run/watchdog/file"
)
