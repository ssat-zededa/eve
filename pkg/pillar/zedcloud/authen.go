// Copyright (c) 2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

// Common code to communicate to zedcloud

package zedcloud

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io/ioutil"
	"math/big"
	"mime"
	"net/http"
	"os"

	"github.com/golang/protobuf/proto"
	zauth "github.com/lf-edge/eve/api/go/auth"
	zcert "github.com/lf-edge/eve/api/go/certs"
	zcommon "github.com/lf-edge/eve/api/go/evecommon"
	etpm "github.com/lf-edge/eve/pkg/pillar/evetpm"
	"github.com/lf-edge/eve/pkg/pillar/types"
	fileutils "github.com/lf-edge/eve/pkg/pillar/utils/file"
	"github.com/satori/go.uuid"
)

const (
	hashSha256Len16 = 16 // senderCertHash size of 16
	hashSha256Len32 = 32 // size of 32 bytes
)

// given an envelope protobuf received from controller, verify the authentication
func verifyAuthentication(ctx *ZedCloudContext, c []byte, skipVerify bool) ([]byte, types.SenderResult, error) {
	senderSt := types.SenderStatusNone
	sm := &zauth.AuthContainer{}
	err := proto.Unmarshal(c, sm)
	if err != nil {
		ctx.log.Errorf("verifyAuthentication: can not unmarshal authen content, %v\n", err)
		return nil, senderSt, err
	}

	data := sm.ProtectedPayload.GetPayload()
	if !skipVerify { // no verify for /certs itself
		if len(sm.GetSenderCertHash()) != hashSha256Len16 &&
			len(sm.GetSenderCertHash()) != hashSha256Len32 {
			ctx.log.Errorf("verifyAuthentication: senderCertHash length %d\n",
				len(sm.GetSenderCertHash()))
			err := fmt.Errorf("verifyAuthentication: senderCertHash length error")
			return nil, types.SenderStatusHashSizeError, err
		}

		if ctx.serverSigningCert == nil {
			err := getServerSigingCert(ctx)
			if err != nil {
				ctx.log.Errorf("verifyAuthentication: can not get server cert, %v\n", err)
				return nil, senderSt, err
			}
		}

		switch sm.Algo {
		case zcommon.HashAlgorithm_HASH_ALGORITHM_SHA256_32BYTES:
			if bytes.Compare(sm.GetSenderCertHash(), ctx.serverSigningCertHash) != 0 {
				err := fmt.Errorf("verifyAuthentication: local server cert hash 32bytes does not match in authen")
				ctx.log.Errorf("verifyAuthentication: local server cert hash(%d) does not match in authen (%d) %v, %v",
					len(ctx.serverSigningCertHash), len(sm.GetSenderCertHash()), ctx.serverSigningCertHash, sm.GetSenderCertHash())
				return nil, types.SenderStatusCertMiss, err
			}
		case zcommon.HashAlgorithm_HASH_ALGORITHM_SHA256_16BYTES:
			if bytes.Compare(sm.GetSenderCertHash(), ctx.serverSigningCertHash[:hashSha256Len16]) != 0 {
				err := fmt.Errorf("verifyAuthentication: local server cert hash 16bytes does not match in authen")
				ctx.log.Errorf("verifyAuthentication: local server cert hash(%d) does not match in authen (%d) %v, %v",
					len(ctx.serverSigningCertHash), len(sm.GetSenderCertHash()), ctx.serverSigningCertHash, sm.GetSenderCertHash())
				return nil, types.SenderStatusCertMiss, err
			}
		default:
			ctx.log.Errorf("verifyAuthentication: hash algorithm is not supported\n")
			err := fmt.Errorf("verifyAuthentication: hash algorithm is not supported")
			return nil, types.SenderStatusAlgoFail, err
		}

		hash := ComputeSha(data)
		err = verifyAuthSig(ctx, sm.GetSignatureHash(), ctx.serverSigningCert, hash)
		if err != nil {
			ctx.log.Errorf("verifyAuthentication: verifyAuthSig error %v\n", err)
			return nil, types.SenderStatusSignVerifyFail, err
		}
		ctx.log.Debugf("verifyAuthentication: ok\n")
	}
	return data, senderSt, nil
}

func getServerSigingCert(ctx *ZedCloudContext) error {
	certBytes, err := ioutil.ReadFile(types.ServerSigningCertFileName)
	if err != nil {
		ctx.log.Errorf("getServerSigningCert: can not read in server cert file, %v\n", err)
		return err
	}
	block, _ := pem.Decode(certBytes)
	if block == nil {
		err := fmt.Errorf("getServerSigningCert: can not get client Cert")
		return err
	}

	sCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		ctx.log.Errorf("getServerSigningCert: can not parse cert %v\n", err)
		return err
	}

	// hash verify using PEM bytes from cloud
	ctx.serverSigningCertHash = ComputeSha(certBytes)
	ctx.serverSigningCert = sCert

	return nil
}

// ClearCloudCert - zero out cached cloud certs in client zedcloudCtx
func ClearCloudCert(ctx *ZedCloudContext) {
	ctx.serverSigningCert = nil
	ctx.serverSigningCertHash = nil
}

// verify the signed data with cloud certificate public key
func verifyAuthSig(ctx *ZedCloudContext, signature []byte, cert *x509.Certificate, hash []byte) error {

	ctx.log.Debugf("verifyAuthsig sigdata (len %d) %v\n", len(hash), hash)

	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash, signature)
		if err != nil {
			return err
		}
		ctx.log.Debugf("verifyAuthSig: verify rsa ok\n")
	case *ecdsa.PublicKey:

		sigHalflen, err := ecdsakeyBytes(pub)
		if err != nil {
			return err
		}
		rbytes := signature[0:sigHalflen]
		sbytes := signature[sigHalflen:]
		r := new(big.Int)
		s := new(big.Int)
		r.SetBytes(rbytes)
		s.SetBytes(sbytes)
		ok := ecdsa.Verify(pub, hash, r, s)
		if !ok {
			return errors.New("ecdsa image signature verification failed")
		}
		ctx.log.Debugf("verifyAuthSig: verify ecdsa ok\n")
	default:
		ctx.log.Errorf("verifyAuthSig: unknown type of public key %T\n", pub)
		return errors.New("unknown type of public key")
	}
	return nil
}

// add an authentication envelope protobuf when sending http POST
func addAuthentication(ctx *ZedCloudContext, b *bytes.Buffer, useOnboard bool) (*bytes.Buffer, error) {
	var data []byte
	if b != nil {
		data = b.Bytes()
	}
	body := zauth.AuthBody{
		Payload: data,
	}
	sm := zauth.AuthContainer{}
	sm.ProtectedPayload = &body

	cert, err := getMyDevCert(ctx, useOnboard)
	if err != nil {
		ctx.log.Debugf("addAuthenticate: get client cert failed\n")
		return nil, err
	}

	// assign our certificate hash of 32 bytes
	if useOnboard {
		sm.SenderCertHash = ctx.onBoardCertHash
		sm.SenderCert = ctx.onBoardCertBytes
		if len(sm.SenderCert) == 0 {
			err := fmt.Errorf("addAuthentication: SenderCert empty")
			ctx.log.Errorf("addAuthenticate: get sender cert failed, %v\n", err)
			return nil, err
		}
		ctx.log.Debugf("addAuthenticate: onboard senderCert size %d\n", len(sm.SenderCert))
	} else {
		sm.SenderCertHash = ctx.deviceCertHash
	}
	if sm.SenderCertHash == nil {
		err := fmt.Errorf("addAuthentication: SenderCertHash empty")
		return nil, err
	}

	sig, err := signAuthData(ctx, data, cert)
	if err != nil {
		ctx.log.Debugf("addAuthenticate: sign auth data error %v\n", err)
		return nil, err
	}
	sm.SignatureHash = sig
	sm.Algo = zcommon.HashAlgorithm_HASH_ALGORITHM_SHA256_32BYTES

	data2, err := proto.Marshal(&sm)
	if err != nil {
		ctx.log.Debugf("addAuthenticate: auth marshal error %v\n", err)
		return nil, err
	}

	size := int64(proto.Size(&sm))
	buf := bytes.NewBuffer(data2)
	ctx.log.Debugf("addAuthenticate: payload size %d, sig %v\n", size, sig)
	return buf, nil
}

func getMyDevCert(ctx *ZedCloudContext, isOnboard bool) (tls.Certificate, error) {
	var cert tls.Certificate
	var err error
	if isOnboard {
		if ctx.onBoardCert == nil {
			cert, err = tls.LoadX509KeyPair(types.OnboardCertName,
				types.OnboardKeyName)
			if err != nil {
				ctx.log.Debugf("getMyDevCert: get onboard cert error %v\n", err)
				return cert, err
			}

			onboardCertpem, err := ioutil.ReadFile(types.OnboardCertName)
			if err != nil {
				ctx.log.Debugf("getMyDevCert: get onboard certbytes error %v\n", err)
				return cert, err
			}
			ctx.onBoardCertBytes = []byte(base64.StdEncoding.EncodeToString(onboardCertpem))

			// device side of cert hash is calculated from the x509 cert, not from PEM bytes
			ctx.onBoardCertHash, err = certToSha256(ctx, cert)
			if err != nil {
				return cert, err
			}
			ctx.onBoardCert = &cert
			ctx.log.Debugf("getMyDevCert: onboard cert with hash %v\n", string(ctx.onBoardCertHash))
		} else {
			cert = *ctx.onBoardCert
		}
	} else {
		if ctx.deviceCert == nil {
			cert, err = GetClientCert()
			if err != nil {
				ctx.log.Errorf("getMyDevCert: get client cert error %v\n", err)
				return cert, err
			}

			// device side of cert hash is calculated from the x509 cert, not from PEM bytes
			ctx.deviceCertHash, err = certToSha256(ctx, cert)
			if err != nil {
				return cert, err
			}
			ctx.deviceCert = &cert
			ctx.log.Debugf("getMyDevCert: device cert with hash %v\n", string(ctx.deviceCertHash))
		} else {
			cert = *ctx.deviceCert
		}
	}
	return cert, nil
}

// sign hash'ed data with certificate private key
func signAuthData(ctx *ZedCloudContext, sigdata []byte, cert tls.Certificate) ([]byte, error) {
	ctx.log.Debugf("sending sigdata (len %d) %v\n", len(sigdata), sigdata)
	hash := ComputeSha(sigdata)

	var sigres []byte
	switch key := cert.PrivateKey.(type) {
	default:
		err := fmt.Errorf("signAuthData: privatekey default, type %T", key)
		return nil, err
	case etpm.TpmPrivateKey:
		r, s, err := etpm.TpmSign(hash)
		if err != nil {
			ctx.log.Errorf("signAuthData: tpmSign error %v\n", err)
			return nil, err
		}
		ctx.log.Debugf("r.bytes %d s.bytes %d\n", len(r.Bytes()),
			len(s.Bytes()))
		sigres, err = RSCombinedBytes(ctx, r.Bytes(), s.Bytes(), key.PublicKey.(*ecdsa.PublicKey))
		if err != nil {
			return nil, err
		}
		ctx.log.Debugf("signAuthData: tpm sigres (len %d): %x\n", len(sigres), sigres)
	case *ecdsa.PrivateKey:
		r, s, err := ecdsa.Sign(rand.Reader, key, hash)
		if err != nil {
			ctx.log.Errorf("signAuthData: ecdsa sign error %v\n", err)
			return nil, err
			//ctx.log.Fatal("ecdsa.Sign: ", err)
		}
		ctx.log.Debugf("r.bytes %d s.bytes %d\n", len(r.Bytes()),
			len(s.Bytes()))
		sigres, err = RSCombinedBytes(ctx, r.Bytes(), s.Bytes(), &key.PublicKey)
		if err != nil {
			return nil, err
		}
		ctx.log.Debugf("signAuthData: ecdas sigres (len %d): %x\n",
			len(sigres), sigres)
	}
	return sigres, nil
}

// RSCombinedBytes - combine r & s into fixed length bytes
func RSCombinedBytes(ctx *ZedCloudContext, rBytes, sBytes []byte, pubKey *ecdsa.PublicKey) ([]byte, error) {
	keySize, err := ecdsakeyBytes(pubKey)
	if err != nil {
		ctx.log.Errorf("RSCombinedBytes: ecdsa key bytes error %v", err)
		return nil, err
	}
	rsize := len(rBytes)
	ssize := len(sBytes)
	if rsize > keySize || ssize > keySize {
		errStr := fmt.Sprintf("RSCombinedBytes: error. keySize %d, rSize %d, sSize %d", keySize, rsize, ssize)
		return nil, errors.New(errStr)
	}

	// basically the size is 32 bytes. the r and s needs to be both left padded to two 32 bytes slice
	// into a single signature buffer
	buffer := make([]byte, keySize*2)
	startPos := keySize - rsize
	copy(buffer[startPos:], rBytes)
	startPos = keySize*2 - ssize
	copy(buffer[startPos:], sBytes)
	return buffer[:], nil
}

func ecdsakeyBytes(pubKey *ecdsa.PublicKey) (int, error) {
	curveBits := pubKey.Curve.Params().BitSize
	keyBytes := curveBits / 8
	if curveBits%8 > 0 {
		keyBytes++
	}

	if keyBytes%8 > 0 {
		errStr := fmt.Sprintf("ecdsa pubkey size error, curveBits %d", curveBits)
		return 0, errors.New(errStr)
	}
	return keyBytes, nil
}

// ComputeSha - Compute sha256 on data
func ComputeSha(data []byte) []byte {
	h := sha256.New()
	h.Write(data)
	hash := h.Sum(nil)
	return hash
}

func checkMimeProtoType(r *http.Response) bool {
	if r == nil {
		return false
	}
	var ctTypeProtoStr = "application/x-proto-binary"
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	mimeType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}

	return mimeType == ctTypeProtoStr
}

// VerifySigningCertChain - verify signing certificate chain from controller
func VerifySigningCertChain(ctx *ZedCloudContext, content []byte) ([]byte, error) {
	sm := &zcert.ZControllerCert{}
	err := proto.Unmarshal(content, sm)
	if err != nil {
		errStr := fmt.Sprintf("unmarshal error, %v", err)
		ctx.log.Errorln("VerifySigningCertChain: " + errStr)
		return nil, errors.New(errStr)
	}

	// prepare intermediate certs and validate the payload
	var sigCertBytes []byte
	var keyCnt, signKeyCnt int
	interm := x509.NewCertPool()
	for _, cert := range sm.GetCerts() {
		keyCnt++
		switch cert.Type {
		case zcert.ZCertType_CERT_TYPE_CONTROLLER_INTERMEDIATE:
			ok := interm.AppendCertsFromPEM(cert.GetCert())
			if !ok {
				errStr := fmt.Sprintf("intermediate cert append fail")
				ctx.log.Errorln("VerifySigningCertChain: " + errStr)
				return nil, errors.New(errStr)
			}

		case zcert.ZCertType_CERT_TYPE_CONTROLLER_SIGNING:
			signKeyCnt++
			if signKeyCnt > 1 {
				errStr := fmt.Sprintf("received more than one signing cert")
				ctx.log.Errorln("VerifySigningCertChain: " + errStr)
				return nil, errors.New(errStr)
			}
			sigCertBytes = cert.GetCert()

		case zcert.ZCertType_CERT_TYPE_CONTROLLER_ECDH_EXCHANGE:
			// nothing to do

		default:
			errStr := fmt.Sprintf("unknown certificate type(%d) received", cert.Type)
			ctx.log.Errorln("VerifySigningCertChain: " + errStr)
			return nil, errors.New(errStr)
		}
	}

	ctx.log.Debugf("VerifySigningCertChain: key count %d\n", keyCnt)
	if signKeyCnt == 0 {
		errStr := fmt.Sprintf("failed to acquire signing cert")
		ctx.log.Errorln("VerifySigningCertChain: " + errStr)
		return nil, errors.New(errStr)
	}

	// verify signature of certificates
	for _, cert := range sm.GetCerts() {
		if cert.Type == zcert.ZCertType_CERT_TYPE_CONTROLLER_SIGNING ||
			cert.Type == zcert.ZCertType_CERT_TYPE_CONTROLLER_ECDH_EXCHANGE {
			certByte := cert.GetCert()
			if err := verifySignature(ctx, certByte, interm); err != nil {
				errStr := fmt.Sprintf("signature verification fail")
				ctx.log.Errorln("VerifySigningCertChain: " + errStr)
				return nil, err
			}
		}
	}
	ctx.log.Debugf("VerifySigningCertChain: success\n")
	return sigCertBytes, nil
}

func verifySignature(ctx *ZedCloudContext, certByte []byte, interm *x509.CertPool) error {

	block, _ := pem.Decode(certByte)
	if block == nil {
		errStr := fmt.Sprintf("certificate block decode fail")
		ctx.log.Errorln("verifySignature: " + errStr)
		return errors.New(errStr)
	}

	leafcert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		errStr := fmt.Sprintf("certificate parse fail, %v", err)
		ctx.log.Errorln("verifySignature: " + errStr)
		return errors.New(errStr)
	}

	// Get the root certificate from file
	signingRoots := x509.NewCertPool()
	caCert, err := ioutil.ReadFile(types.RootCertFileName)
	if err != nil {
		errStr := fmt.Sprintf("root certificate read fail, %v", err)
		ctx.log.Errorln("verifySignature: " + errStr)
		return err
	}
	if !signingRoots.AppendCertsFromPEM(caCert) {
		errStr := fmt.Sprintf("root certificate append fail, %s",
			types.RootCertFileName)
		ctx.log.Errorln("verifySignature: " + errStr)
		return errors.New(errStr)
	}

	opts := x509.VerifyOptions{
		Roots: signingRoots,
		// for signing, not to verify the server name
		Intermediates: interm,
	}
	if _, err := leafcert.Verify(opts); err != nil {
		errStr := fmt.Sprintf("signature verification fail, %v", err)
		ctx.log.Errorln("verifySignature: " + errStr)
		return errors.New(errStr)
	}
	return nil
}

func UpdateServerCert(ctx *ZedCloudContext, certByte []byte) error {

	// decode the certificate
	block, _ := pem.Decode(certByte)
	if block == nil {
		errStr := fmt.Sprintf("certificate decode fail")
		ctx.log.Errorln("UpdateServerCert: " + errStr)
		return errors.New(errStr)
	}

	// parse the certificate
	leafCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		errStr := fmt.Sprintf("certificate parse fail, %v", err)
		ctx.log.Errorln("UpdateServerCert: " + errStr)
		return errors.New(errStr)
	}

	// write to the file
	if err := fileutils.WriteRename(types.ServerSigningCertFileName,
		certByte); err != nil {
		errStr := fmt.Sprintf("file write err %v", err)
		ctx.log.Errorln("UpdateServerCert: " + errStr)
		return errors.New(errStr)
	}

	// store the certificate
	ctx.serverSigningCert = leafCert

	// store the certificate hash
	ctx.serverSigningCertHash = ComputeSha(certByte)
	return nil
}

// UseV2API - check the controller cert file and use V2 api if it exist
// by default it is running V2, unless /config/Force-API-V1 file exists
func UseV2API() bool {
	_, err := os.Stat(types.APIV1FileName)
	if err == nil {
		return false
	}
	return true
}

// URLPathString - generate url for either v1 or v1 API path
func URLPathString(server string, isV2api bool, devUUID uuid.UUID, action string) string {
	var urlstr string
	if !isV2api {
		urlstr = server + "/api/v1/edgedevice/" + action
	} else {
		urlstr = server + "/api/v2/edgedevice/"
		if devUUID != nilUUID {
			urlstr = urlstr + "id/" + devUUID.String() + "/"
		}
		urlstr = urlstr + action
	}
	return urlstr
}

// the cloud controller lookup prefers the hash computed from x509 cert
func certToSha256(ctx *ZedCloudContext, cert tls.Certificate) ([]byte, error) {
	if len(cert.Certificate) == 0 {
		err := fmt.Errorf("certToSha256: no cert entry")
		return nil, err
	}

	c, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		ctx.log.Infof("certToSha256: parse cert entry err %v\n", err)
		return nil, err
	}

	return ComputeSha(c.Raw), nil
}
