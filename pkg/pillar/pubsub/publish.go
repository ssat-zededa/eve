// Copyright (c) 2017-2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package pubsub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/go-cmp/cmp"
	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/sirupsen/logrus"
)

const (
	// Global fixed string for a global subject, i.e. no agent
	Global = "global"
)

// LocalCollection represents an entire local copy of a set of key-value pairs
type LocalCollection map[string][]byte

// Notify simple struct to pass notification messages
type Notify struct{}

// Publication to publish to an individual topic
// Usage:
//  p1, err := pubsublegacy.Publish("foo", fooStruct{})
//  ...
//  // Optional
//  p1.SignalRestarted()
//  ...
//  p1.Publish(key, item)
//  p1.Unpublish(key) to delete
//
//  foo := p1.Get(key)
//  fooAll := p1.GetAll()

// PublicationImpl - Publication Implementation. The main structure that implements
//  Publication interface.
type PublicationImpl struct {
	// Private fields
	topicType   reflect.Type
	agentName   string
	agentScope  string
	topic       string
	km          keyMap
	global      bool
	defaultName string
	updaterList *Updaters
	logger      *logrus.Logger
	log         *base.LogObject

	driver DriverPublisher
}

// IsRestarted has this publication been set to "restarted"
func (pub *PublicationImpl) IsRestarted() bool {
	return pub.km.restarted
}

// Publish publish a key-value pair
func (pub *PublicationImpl) Publish(key string, item interface{}) error {
	topic := TypeToName(item)
	name := pub.nameString()
	if topic != pub.topic {
		errStr := fmt.Sprintf("Publish(%s): item is wrong topic %s",
			name, topic)
		pub.log.Fatalln(errStr)
	}
	val := reflect.ValueOf(item)
	if val.Kind() == reflect.Ptr {
		pub.log.Fatalf("Publish got a pointer for %s", name)
	}
	// Perform a deepCopy in case the caller might change a map etc
	newItem := deepCopy(pub.log, item)
	if m, ok := pub.km.key.Load(key); ok {
		if cmp.Equal(m, newItem) {
			pub.log.Debugf("Publish(%s/%s) unchanged\n", name, key)
			return nil
		}
		// DO NOT log Values. They may contain sensitive information.
		pub.log.Debugf("Publish(%s/%s) replacing due to diff\n",
			name, key)

		loggable, ok := newItem.(base.LoggableObject)
		if ok {
			loggable.LogModify(pub.log, m)
		}
	} else {
		// DO NOT log Values. They may contain sensitive information.
		pub.log.Debugf("Publish(%s/%s) adding Item", name, key)
		loggable, ok := newItem.(base.LoggableObject)
		if ok {
			loggable.LogCreate(pub.log)
		}
	}
	pub.km.key.Store(key, newItem)

	if pub.logger.GetLevel() == logrus.TraceLevel {
		pub.dump("after Publish")
	}
	pub.updatersNotify(name)
	// marshal to json bytes to send to the driver
	b, err := json.Marshal(item)
	if err != nil {
		pub.log.Fatal("json Marshal in socketdriver Publish", err)
	}

	return pub.driver.Publish(key, b)
}

// Unpublish delete a key from the key-value map
func (pub *PublicationImpl) Unpublish(key string) error {
	name := pub.nameString()
	if m, ok := pub.km.key.Load(key); ok {
		// DO NOT log Values. They may contain sensitive information.
		pub.log.Debugf("Unpublish(%s/%s) removing Item", name, key)
		loggable, ok := m.(base.LoggableObject)
		if ok {
			loggable.LogDelete(pub.log)
		}
	} else {
		errStr := fmt.Sprintf("Unpublish(%s/%s): key does not exist",
			name, key)
		pub.log.Errorf("%s\n", errStr)
		return errors.New(errStr)
	}
	pub.km.key.Delete(key)
	if pub.logger.GetLevel() == logrus.TraceLevel {
		pub.dump("after Unpublish")
	}
	pub.updatersNotify(name)

	return pub.driver.Unpublish(key)
}

// SignalRestarted signal that a publication is restarted
func (pub *PublicationImpl) SignalRestarted() error {
	pub.log.Debugf("pub.SignalRestarted(%s)\n", pub.nameString())
	return pub.restartImpl(true)
}

// ClearRestarted clear the restart signal
func (pub *PublicationImpl) ClearRestarted() error {
	pub.log.Debugf("pub.ClearRestarted(%s)\n", pub.nameString())
	return pub.restartImpl(false)
}

// Get the value for a given key
func (pub *PublicationImpl) Get(key string) (interface{}, error) {
	m, ok := pub.km.key.Load(key)
	if ok {
		newIntf := deepCopy(pub.log, m)
		return newIntf, nil
	} else {
		name := pub.nameString()
		errStr := fmt.Sprintf("Get(%s) unknown key %s", name, key)
		return nil, errors.New(errStr)
	}
}

// GetAll enumerate all the key-value pairs for the collection
func (pub *PublicationImpl) GetAll() map[string]interface{} {
	result := make(map[string]interface{})
	assigner := func(key string, val interface{}) bool {
		newVal := deepCopy(pub.log, val)
		result[key] = newVal
		return true
	}
	pub.km.key.Range(assigner)
	return result
}

// Iterate - performs some callback function on all items
func (pub *PublicationImpl) Iterate(function base.StrMapFunc) {
	pub.km.key.Range(function)
}

// Close the publisher
func (pub *PublicationImpl) Close() error {
	items := pub.GetAll()
	for key := range items {
		pub.log.Infof("Close(%s) unloading key %s",
			pub.nameString(), key)
		pub.Unpublish(key)
	}
	pub.ClearRestarted()
	pub.driver.Stop()
	return nil
}

// methods just for this implementation of Publisher

// updatersNotify send a notification to all the matching channels which does not yet
// have one queued.
func (pub *PublicationImpl) updatersNotify(name string) {
	pub.updaterList.lock.Lock()
	for _, nn := range pub.updaterList.servers {
		if nn.name != name {
			continue
		}
		select {
		case nn.ch <- Notify{}:
			pub.log.Debugf("updaterNotify sent to %s/%d\n",
				nn.name, nn.instance)
		default:
			pub.log.Debugf("updaterNotify NOT sent to %s/%d\n",
				nn.name, nn.instance)
		}
	}
	pub.updaterList.lock.Unlock()
}

// Only reads json files. Sets restarted if that file was found.
func (pub *PublicationImpl) populate() {
	name := pub.nameString()

	pub.log.Debugf("populate(%s)\n", name)

	pairs, restarted, err := pub.driver.Load()
	if err != nil {
		// Could be a truncated or empty file
		pub.log.Error(err)
		return
	}
	for key, itemB := range pairs {
		item, err := parseTemplate(pub.log, itemB, pub.topicType)
		if err != nil {
			// Handle bad files such as those of size zero
			pub.log.Error(err)
			continue
		}
		pub.km.key.Store(key, item)
	}
	pub.km.restarted = restarted
	pub.log.Debugf("populate(%s) done\n", name)
}

// go routine which runs the AF_UNIX server.
func (pub *PublicationImpl) publisher() {
	pub.driver.Start()
}

// DetermineDiffs update a provided LocalCollection to the current state,
// and return the deleted keys before the added/modified ones
func (pub *PublicationImpl) DetermineDiffs(localCollection LocalCollection) []string {
	var keys []string
	name := pub.nameString()
	items := pub.GetAll()
	// Look for deleted
	for localKey := range localCollection {
		_, ok := items[localKey]
		if !ok {
			pub.log.Debugf("determineDiffs(%s): key %s deleted\n",
				name, localKey)
			delete(localCollection, localKey)
			keys = append(keys, localKey)
		}
	}
	// Look for new/changed
	for originKey, origin := range items {
		originb, err := json.Marshal(origin)
		if err != nil {
			pub.log.Fatalf("json Marshal in DetermineDiffs for origin key %s: %v", originKey, err)
		}

		local := lookupLocal(localCollection, originKey)
		if local == nil {
			pub.log.Debugf("determineDiffs(%s): key %s added\n",
				name, originKey)
			localCollection[originKey] = originb
			keys = append(keys, originKey)
		} else if bytes.Compare(originb, local) != 0 {
			pub.log.Debugf("determineDiffs(%s): key %s replacing due to diff\n",
				name, originKey)
			// XXX is deepCopy needed?
			localCollection[originKey] = originb
			keys = append(keys, originKey)
		} else {
			pub.log.Debugf("determineDiffs(%s): key %s unchanged\n",
				name, originKey)
		}
	}
	return keys
}

func (pub *PublicationImpl) nameString() string {
	var name string
	switch {
	case pub.global:
		name = Global
	case pub.agentScope == "":
		name = fmt.Sprintf("%s/%s", pub.agentName, pub.topic)
	default:
		name = fmt.Sprintf("%s/%s/%s", pub.agentName, pub.agentScope, pub.topic)
	}
	return name
}

// Record the restarted state and send over socket/file.
func (pub *PublicationImpl) restartImpl(restarted bool) error {
	name := pub.nameString()
	pub.log.Infof("pub.restartImpl(%s, %v)\n", name, restarted)

	if restarted == pub.km.restarted {
		pub.log.Infof("pub.restartImpl(%s, %v) value unchanged\n",
			name, restarted)
		return nil
	}
	pub.km.restarted = restarted
	if restarted {
		// XXX lock on restarted to make sure it gets noticed?
		// XXX bug?
		// Implicit in updaters lock??
		pub.updatersNotify(name)
	}
	return pub.driver.Restart(restarted)
}

func (pub *PublicationImpl) dump(infoStr string) {

	name := pub.nameString()
	pub.log.Debugf("dump(%s) %s\n", name, infoStr)
	dumper := func(key string, val interface{}) bool {
		_, err := json.Marshal(val)
		if err != nil {
			pub.log.Fatal("json Marshal in dump", err)
		}
		// DO NOT log Values. They may contain sensitive information.
		pub.log.Debugf("\tkey %s", key)
		return true
	}
	pub.km.key.Range(dumper)
	pub.log.Debugf("\trestarted %t\n", pub.km.restarted)
}
