// Copyright (c) 2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package hardwaremodel

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/hardware"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/rackn/gohai/plugins/dmi"
	"github.com/rackn/gohai/plugins/net"
	"github.com/rackn/gohai/plugins/storage"
	"github.com/rackn/gohai/plugins/system"
	"github.com/sirupsen/logrus"
)

// Set from Makefile
var Version = "No version specified"

type info interface {
	Class() string
}

func hwFp(log *base.LogObject, outputFile string) {
	infos := map[string]info{}
	dmiInfo, err := dmi.Gather()
	if err != nil {
		log.Fatalf("Failed to gather DMI information: %v", err)
	}
	infos[dmiInfo.Class()] = dmiInfo
	netInfo, err := net.Gather()
	if err != nil {
		log.Fatalf("Failed to gather network info: %v", err)
	}
	infos[netInfo.Class()] = netInfo
	sysInfo, err := system.Gather()
	if err != nil {
		log.Fatalf("Failed to gather basic OS info: %v", err)
	}
	infos[sysInfo.Class()] = sysInfo
	storInfo, err := storage.Gather()
	if err != nil {
		log.Fatalf("Failed to gather storage info: %v", err)
	}
	var outfile *os.File
	infos[storInfo.Class()] = storInfo
	outfile, err = os.OpenFile(outputFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("hwFp error: %s", err)
	}
	enc := json.NewEncoder(outfile)
	enc.SetIndent("", "  ")
	enc.Encode(infos)
}

var logger *logrus.Logger
var log *base.LogObject

func Run(ps *pubsub.PubSub, loggerArg *logrus.Logger, logArg *base.LogObject) int {
	logger = loggerArg
	log = logArg
	debugPtr := flag.Bool("d", false, "Debug flag")
	versionPtr := flag.Bool("v", false, "Version")
	cPtr := flag.Bool("c", false, "No CRLF")
	hwPtr := flag.Bool("f", false, "Fingerprint hardware")
	outputFilePtr := flag.String("o", "/config/hardwaremodel", "file or device for output")
	flag.Parse()
	outputFile := *outputFilePtr
	if *debugPtr {
		logger.SetLevel(logrus.TraceLevel)
	} else {
		logger.SetLevel(logrus.InfoLevel)
	}
	if *versionPtr {
		fmt.Printf("%s: %s\n", os.Args[0], Version)
		return 0
	}
	if *hwPtr {
		hwFp(log, outputFile)
		return 0
	}
	model := hardware.GetHardwareModelNoOverride(log)
	if *cPtr {
		b := []byte(fmt.Sprintf("%s", model))
		err := ioutil.WriteFile(outputFile, b, 0644)
		if err != nil {
			log.Fatal("WriteFile", err, outputFile)
		}

	} else {
		b := []byte(fmt.Sprintf("%s\n", model))
		err := ioutil.WriteFile(outputFile, b, 0644)
		if err != nil {
			log.Fatal("WriteFile", err, outputFile)
		}
	}
	return 0
}
