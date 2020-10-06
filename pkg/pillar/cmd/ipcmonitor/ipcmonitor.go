// Copyright (c) 2017,2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

// Connect to and watch for updates for a given agent, agentScope, and topic.
// Uses AF_UNIX socket
//
// Example usage: to monitor what zedmanager publishes in DomainConfig use
// ipcmonitor -a zedmanager -t DomainConfig
//     That corresponds to the state in /var/run/zedmanager/DomainConfig/*.json
//     but with ongoing updates and deletes.
// For agents with agentScope, such as downloader and verifier, use e.g.,
// ipcmonitor -a zedmanager -s appImg.obj -t DownloaderConfiga
//     which corresponds to /var/run/zedmanager/appImg.obj/DownloaderConfig/

package ipcmonitor

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"strings"

	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/sirupsen/logrus"
)

var debugOverride bool // From command line arg

var logger *logrus.Logger
var log *base.LogObject

func Run(ps *pubsub.PubSub, loggerArg *logrus.Logger, logArg *base.LogObject) int {
	logger = loggerArg
	log = logArg
	agentNamePtr := flag.String("a", "zedrouter",
		"Agent name")
	agentScopePtr := flag.String("s", "", "agentScope")
	topicPtr := flag.String("t", "DeviceNetworkStatus",
		"topic")
	debugPtr := flag.Bool("d", false, "Debug flag")
	persistentPtr := flag.Bool("P", false, "Persistent flag")
	formatPtr := flag.String("f", "go", "format flag, defaults to 'go', supports: 'go', 'json'")
	flag.Parse()
	agentName := *agentNamePtr
	agentScope := *agentScopePtr
	topic := *topicPtr
	debugOverride = *debugPtr
	if debugOverride {
		logger.SetLevel(logrus.TraceLevel)
	} else {
		logger.SetLevel(logrus.InfoLevel)
	}
	if *persistentPtr {
		testPersistent(ps, agentName, agentScope, topic)
		return 0
	}
	format := *formatPtr

	name := nameString(agentName, agentScope, topic)
	sockName := fmt.Sprintf("/var/run/%s.sock", name)
	s, err := net.Dial("unixpacket", sockName)
	if err != nil {
		log.Fatal("Dial:", err)
	}
	req := fmt.Sprintf("request %s", topic)
	s.Write([]byte(req))
	buf := make([]byte, 65536)
	for {
		res, err := s.Read(buf)
		if err != nil {
			log.Fatal("Read:", err)
		}
		if res == len(buf) {
			// Likely truncated
			log.Fatalf("Message likely truncated\n")
		}
		reply := strings.Split(string(buf[0:res]), " ")
		count := len(reply)
		if count < 2 {
			log.Errorf("Too short: %v\n", reply)
			continue
		}
		msg := reply[0]
		t := reply[1]

		if t != topic {
			log.Errorf("Mismatched topic %s vs. %s for %s\n",
				t, topic, msg)
			continue
		}

		switch msg {
		case "hello", "restarted", "complete":
			log.Infof("Got message %s type %s\n", msg, t)

		case "delete":
			if count < 3 {
				log.Errorf("Too short delete: %v\n", reply)
				continue
			}
			recvKey := reply[2]

			key, err := base64.StdEncoding.DecodeString(recvKey)
			if err != nil {
				log.Errorf("base64: %s\n", err)
			}
			log.Infof("delete type %s key %s\n", t, key)

		case "update":
			if count < 4 {
				log.Errorf("Too short update: %v\n", reply)
				continue
			}
			if count > 4 {
				log.Errorf("Too long update: %v\n", reply)
				continue
			}
			recvKey := reply[2]
			key, err := base64.StdEncoding.DecodeString(recvKey)
			if err != nil {
				log.Errorf("base64: %s\n", err)
			}
			recvVal := reply[3]
			val, err := base64.StdEncoding.DecodeString(recvVal)
			if err != nil {
				log.Errorf("base64: %s\n", err)
			}

			switch format {
			case "go":
				var output interface{}
				if err := json.Unmarshal(val, &output); err != nil {
					log.Fatal(err, "json Unmarshal")
				}
				log.Infof("update type %s key %s val %+v\n",
					t, key, output)
			case "json":
				var out bytes.Buffer
				if err = json.Indent(&out, val, "", "\t"); err != nil {
					log.Fatalf("unable to indent json: %v", err)
				}
				log.Infof("update type %s key %s: %s\n", t, key, out.String())
			default:
				log.Fatalf("unsupported format: %s", format)
			}

		default:
			log.Errorf("Unknown message: %s\n", msg)
		}
	}
}

func nameString(agentname, agentscope, topic string) string {
	if agentscope == "" {
		return fmt.Sprintf("%s/%s", agentname, topic)
	} else {
		return fmt.Sprintf("%s/%s/%s", agentname, agentscope, topic)
	}
}

func testPersistent(ps *pubsub.PubSub, agentName string, agentScope string, topic string) {
	ctx := 3
	sub, err := ps.NewSubscription(pubsub.SubscriptionOptions{
		AgentName:   agentName,
		AgentScope:  agentScope,
		MyAgentName: agentName,
		// XXX hard-coded; need nameToType ;-)
		TopicImpl:     types.DevicePortConfigList{},
		Activate:      false,
		Persistent:    true,
		Ctx:           &ctx,
		CreateHandler: handleCreate,
		ModifyHandler: handleModify,
		DeleteHandler: handleDelete,
	})
	if err != nil {
		log.Fatal(err)
	}
	sub.Activate()
}

func handleCreate(ctxArg interface{}, key string,
	statusArg interface{}) {

	log.Infof("handleCreate(%s) type %T\n", key, statusArg)
	switch statusArg.(type) {
	case types.DevicePortConfigList:
		dpcl := statusArg.(types.DevicePortConfigList)
		log.Infof("DPCL %+v\n", dpcl)
	}
}

func handleModify(ctxArg interface{}, key string,
	statusArg interface{}) {

	log.Infof("handleModify(%s) type %T\n", key, statusArg)
	switch statusArg.(type) {
	case types.DevicePortConfigList:
		dpcl := statusArg.(types.DevicePortConfigList)
		log.Infof("DPCL %+v\n", dpcl)
	}
}

func handleDelete(ctxArg interface{}, key string,
	statusArg interface{}) {

	log.Infof("handleDelete(%s) type %T\n", key, statusArg)
	switch statusArg.(type) {
	case types.DevicePortConfigList:
		dpcl := statusArg.(types.DevicePortConfigList)
		log.Infof("DPCL %+v\n", dpcl)
	}
}
