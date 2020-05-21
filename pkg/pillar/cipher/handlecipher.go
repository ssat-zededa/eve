// Copyright (c) 2018-2019 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package cipher

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/ioutil"

	zconfig "github.com/lf-edge/eve/api/go/config"
	zcommon "github.com/lf-edge/eve/api/go/evecommon"
	"github.com/lf-edge/eve/pkg/pillar/cmd/tpmmgr"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	log "github.com/sirupsen/logrus"
)

// DecryptCipherContext has subscriptions to controller certs
// and cipher contexts for doing decryption
type DecryptCipherContext struct {
	SubControllerCert pubsub.Subscription
	SubCipherContext  pubsub.Subscription
}

// look up controller cert
func lookupControllerCert(ctx *DecryptCipherContext, key string) *types.ControllerCert {
	log.Infof("lookupControllerCert(%s)\n", key)
	sub := ctx.SubControllerCert
	item, err := sub.Get(key)
	if err != nil {
		log.Errorf("lookupControllerCert(%s) not found\n", key)
		return nil
	}
	status := item.(types.ControllerCert)
	log.Infof("lookupControllerCert(%s) Done\n", key)
	return &status
}

// look up cipher context
func lookupCipherContext(ctx *DecryptCipherContext, key string) *types.CipherContext {
	log.Infof("lookupCipherContext(%s)\n", key)
	sub := ctx.SubCipherContext
	item, err := sub.Get(key)
	if err != nil {
		log.Errorf("lookupCipherContext(%s) not found\n", key)
		return nil
	}
	status := item.(types.CipherContext)
	log.Infof("lookupCipherContext(%s) done\n", key)
	return &status
}

func getControllerCert(ctx *DecryptCipherContext,
	cipherBlock types.CipherBlockStatus) ([]byte, error) {

	log.Infof("getControllerCert for %s\n", cipherBlock.CipherBlockID)
	cipherContext := lookupCipherContext(ctx, cipherBlock.CipherContextID)
	if cipherContext == nil {
		errStr := fmt.Sprintf("cipher context %s not found\n",
			cipherBlock.CipherContextID)
		log.Error(errStr)
		return []byte{}, errors.New(errStr)
	}
	controllerCert := lookupControllerCert(ctx, cipherContext.ControllerCertKey())
	if controllerCert == nil {
		errStr := fmt.Sprintf("controller cert %s not found\n",
			cipherContext.ControllerCertKey())
		log.Error(errStr)
		return []byte{}, errors.New(errStr)
	}
	if controllerCert.HasError() {
		errStr := fmt.Sprintf("controller cert has following error: %v",
			controllerCert.Error)
		log.Error(errStr)
		return []byte{}, errors.New(errStr)
	}
	log.Infof("getControllerCert for %s Done\n", cipherBlock.CipherBlockID)
	return controllerCert.Cert, nil
}

func getDeviceCert(ctx *DecryptCipherContext,
	cipherBlock types.CipherBlockStatus) ([]byte, error) {

	log.Infof("getDeviceCert for %s\n", cipherBlock.CipherBlockID)
	cipherContext := lookupCipherContext(ctx, cipherBlock.CipherContextID)
	if cipherContext == nil {
		errStr := fmt.Sprintf("cipher context %s not found\n",
			cipherBlock.CipherContextID)
		log.Error(errStr)
		return []byte{}, errors.New(errStr)
	}
	// TBD:XXX as of now, only one
	certBytes, err := ioutil.ReadFile(types.DeviceCertName)
	if err != nil {
		errStr := fmt.Sprintf("getDeviceCert failed while reading device certificate: %v",
			err)
		log.Error(errStr)
		return []byte{}, errors.New(errStr)
	}
	if computeAndMatchHash(certBytes, cipherContext.DeviceCertHash,
		cipherContext.HashScheme) {
		log.Infof("getDeviceCert for %s Done\n", cipherBlock.CipherBlockID)
		return certBytes, nil
	}
	errStr := fmt.Sprintf("getDeviceCert for %s not found\n",
		cipherBlock.CipherBlockID)
	log.Error(errStr)
	return []byte{}, errors.New(errStr)
}

// hash function
func computeAndMatchHash(cert []byte, suppliedHash []byte,
	hashScheme zcommon.HashAlgorithm) bool {

	switch hashScheme {
	case zcommon.HashAlgorithm_HASH_ALGORITHM_INVALID:
		return false

	case zcommon.HashAlgorithm_HASH_ALGORITHM_SHA256_16BYTES:
		h := sha256.New()
		h.Write(cert)
		computedHash := h.Sum(nil)
		return bytes.Equal(suppliedHash, computedHash[:16])

	case zcommon.HashAlgorithm_HASH_ALGORITHM_SHA256_32BYTES:
		h := sha256.New()
		h.Write(cert)
		computedHash := h.Sum(nil)
		return bytes.Equal(suppliedHash, computedHash)
	}
	return false
}

// DecryptCipherBlock : Decryption API, for encrypted object information received from controller
func DecryptCipherBlock(ctx *DecryptCipherContext,
	cipherBlock types.CipherBlockStatus) ([]byte, error) {
	if len(cipherBlock.CipherData) == 0 {
		return []byte{}, errors.New("Invalid Cipher Payload")
	}
	cipherContext := lookupCipherContext(ctx, cipherBlock.CipherContextID)
	if cipherContext == nil {
		errStr := fmt.Sprintf("cipher context %s not found\n",
			cipherBlock.CipherContextID)
		log.Error(errStr)
		return []byte{}, errors.New(errStr)
	}
	switch cipherContext.KeyExchangeScheme {
	case zconfig.KeyExchangeScheme_KEA_NONE:
		return []byte{}, errors.New("No Key Exchange Scheme")

	case zconfig.KeyExchangeScheme_KEA_ECDH:
		clearData, err := decryptCipherBlockWithECDH(ctx, cipherBlock)
		if err != nil {
			return []byte{}, err
		}
		if ret := validateDataHash(clearData,
			cipherBlock.ClearTextHash); !ret {
			return []byte{}, errors.New("Data Validation Failed")
		}
		return clearData, nil
	}
	return []byte{}, errors.New("Unsupported Cipher Key Exchange Scheme")
}

func decryptCipherBlockWithECDH(ctx *DecryptCipherContext,
	cipherBlock types.CipherBlockStatus) ([]byte, error) {
	cert, err := getControllerCertEcdhKey(ctx, cipherBlock)
	if err != nil {
		log.Errorf("Could not extract ECDH Certificate Information")
		return []byte{}, err
	}
	cipherContext := lookupCipherContext(ctx, cipherBlock.CipherContextID)
	if cipherContext == nil {
		errStr := fmt.Sprintf("cipher context %s not found\n",
			cipherBlock.CipherContextID)
		log.Error(errStr)
		return []byte{}, errors.New(errStr)
	}
	switch cipherContext.EncryptionScheme {
	case zconfig.EncryptionScheme_SA_NONE:
		return []byte{}, errors.New("No Encryption")

	case zconfig.EncryptionScheme_SA_AES_256_CFB:
		if len(cipherBlock.InitialValue) == 0 {
			return []byte{}, errors.New("Invalid Initial value")
		}
		clearData := make([]byte, len(cipherBlock.CipherData))
		err = tpmmgr.DecryptSecretWithEcdhKey(cert.X, cert.Y,
			cipherBlock.InitialValue, cipherBlock.CipherData, clearData)
		if err != nil {
			errStr := fmt.Sprintf("Decryption failed with error %v\n", err)
			log.Error(errStr)
			return []byte{}, errors.New(errStr)
		}
		return clearData, nil
	}
	return []byte{}, errors.New("Unsupported Encryption protocol")
}

func getControllerCertEcdhKey(ctx *DecryptCipherContext,
	cipherBlock types.CipherBlockStatus) (*ecdsa.PublicKey, error) {
	var ecdhPubKey *ecdsa.PublicKey
	block, err := getControllerCert(ctx, cipherBlock)
	if err != nil {
		return nil, err
	}
	certs := []*x509.Certificate{}
	for b, rest := pem.Decode(block); b != nil; b, rest = pem.Decode(rest) {
		if b.Type == "CERTIFICATE" {
			c, e := x509.ParseCertificates(b.Bytes)
			if e != nil {
				continue
			}
			certs = append(certs, c...)
		}
	}
	if len(certs) == 0 {
		return nil, errors.New("No X509 Certificate")
	}
	// use the first valid certificate in the chain
	switch certs[0].PublicKey.(type) {
	case *ecdsa.PublicKey:
		ecdhPubKey = certs[0].PublicKey.(*ecdsa.PublicKey)
	default:
		return ecdhPubKey, errors.New("Not ECDSA Key")
	}
	return ecdhPubKey, nil
}

// validateDataHash : returns true, on hash match
func validateDataHash(data []byte, suppliedHash []byte) bool {
	if len(data) == 0 || len(suppliedHash) == 0 {
		return false
	}
	h := sha256.New()
	h.Write(data)
	computedHash := h.Sum(nil)
	return bytes.Equal(suppliedHash, computedHash)
}
