package pubsub

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/lf-edge/eve/pkg/pillar/base"
	log "github.com/sirupsen/logrus"
)

// SubscriptionImpl handle a subscription to a single agent+topic, optionally scope
// as well. Never should be instantiated directly. Rather, call
// `PubSub.Subscribe*`
type SubscriptionImpl struct {
	C                   <-chan Change
	CreateHandler       SubHandler
	ModifyHandler       SubHandler
	DeleteHandler       SubHandler
	RestartHandler      SubRestartHandler
	SynchronizedHandler SubRestartHandler
	MaxProcessTimeWarn  time.Duration // If set generate warning if ProcessChange
	MaxProcessTimeError time.Duration // If set generate warning if ProcessChange
	Persistent          bool

	// Private fields
	agentName    string
	agentScope   string
	topic        string
	topicType    reflect.Type
	km           keyMap
	userCtx      interface{}
	synchronized bool
	driver       DriverSubscriber
	defaultName  string
}

// MsgChan return the Message Channel for the Subscription.
func (sub *SubscriptionImpl) MsgChan() <-chan Change {
	return sub.C
}

// Activate start the subscription
func (sub *SubscriptionImpl) Activate() error {
	if sub.Persistent {
		sub.populate()
	}
	return sub.driver.Start()
}

// populate is used when activating a persistent subscription to read
// from the json files. This ensures that even if the publisher hasn't started
// yet, the subscriber will be notified with the initial content.
// This sets restarted if the restarted file was found.
// Note that this directly calls handleModify thus unlike subsequent
// changes the agent's handler will be called without going through
// a select on the MsgChan and ProcessChange call.
// Subsequent information from the publisher will be compared in handleModify
// to avoid spurious notifications to the agent.
// XXX can we miss a handleDelete call if the file is deleted after we load?
// Need for a mark and then sweep when handleSynchronized is called?
func (sub *SubscriptionImpl) populate() {
	name := sub.nameString()

	log.Infof("populate(%s)", name)

	pairs, restarted, err := sub.driver.Load()
	if err != nil {
		// Could be a truncated or empty file
		log.Error(err)
		return
	}
	for key, itemB := range pairs {
		log.Infof("populate(%s) key %s", name, key)
		handleModify(sub, key, itemB)
	}
	if restarted {
		handleRestart(sub, true)
	}
	log.Infof("populate(%s) done", name)
}

// ProcessChange process a single change and its parameters. It
// calls the various handlers (if set) and updates the subscribed collection.
// The subscribed collection can be accessed using:
//   foo := s1.Get(key)
//   fooAll := s1.GetAll()
func (sub *SubscriptionImpl) ProcessChange(change Change) {
	start := time.Now()
	log.Debugf("ProcessChange agentName(%s) agentScope(%s) topic(%s): %#v", sub.agentName, sub.agentScope, sub.topic, change)

	switch change.Operation {
	case Restart:
		handleRestart(sub, true)
	case Create:
		handleSynchronized(sub, true)
	case Delete:
		handleDelete(sub, change.Key)
	case Modify:
		handleModify(sub, change.Key, change.Value)
	}
	CheckMaxTimeTopic(sub.agentName, sub.topic, start, sub.MaxProcessTimeWarn, sub.MaxProcessTimeError)
}

// Get - Get object with specified Key from this Subscription.
func (sub *SubscriptionImpl) Get(key string) (interface{}, error) {
	m, ok := sub.km.key.Load(key)
	if ok {
		return m, nil
	} else {
		name := sub.nameString()
		errStr := fmt.Sprintf("Get(%s) unknown key %s", name, key)
		return nil, errors.New(errStr)
	}
}

// GetAll - Enumerate all the key, value for the collection
func (sub *SubscriptionImpl) GetAll() map[string]interface{} {
	result := make(map[string]interface{})
	assigner := func(key string, val interface{}) bool {
		result[key] = val
		return true
	}
	sub.km.key.Range(assigner)
	return result
}

// Iterate - performs some callback function on all items
func (sub *SubscriptionImpl) Iterate(function fn) {
	sub.km.key.Range(function)
}

// Restarted - Check if the Publisher has Restarted
func (sub *SubscriptionImpl) Restarted() bool {
	return sub.km.restarted
}

// Synchronized -
func (sub *SubscriptionImpl) Synchronized() bool {
	return sub.synchronized
}

// Topic returns the string definiting the topic
func (sub *SubscriptionImpl) Topic() string {
	return sub.topic
}

func (sub *SubscriptionImpl) nameString() string {
	var name string
	agentName := sub.agentName
	if agentName == "" {
		agentName = sub.defaultName
	}
	if sub.agentScope == "" {
		name = fmt.Sprintf("%s/%s", sub.agentName, sub.topic)
	} else {
		name = fmt.Sprintf("%s/%s/%s", sub.agentName, sub.agentScope, sub.topic)
	}
	return name
}

func (sub *SubscriptionImpl) dump(infoStr string) {
	name := sub.nameString()
	log.Debugf("dump(%s) %s\n", name, infoStr)
	dumper := func(key string, val interface{}) bool {
		_, err := json.Marshal(val)
		if err != nil {
			log.Fatal("json Marshal in dump", err)
		}
		// DO NOT log Values. They may contain sensitive information.
		log.Debugf("\tkey %s", key)
		return true
	}
	sub.km.key.Range(dumper)
	log.Debugf("\trestarted %t\n", sub.km.restarted)
	log.Debugf("\tsynchronized %t\n", sub.synchronized)
}

// handlers
func handleModify(ctxArg interface{}, key string, itemcb []byte) {
	sub := ctxArg.(*SubscriptionImpl)
	name := sub.nameString()
	log.Debugf("pubsub.handleModify(%s) key %s\n", name, key)
	item, err := parseTemplate(itemcb, sub.topicType)
	if err != nil {
		errStr := fmt.Sprintf("handleModify(%s): json failed %s",
			name, err)
		log.Errorln(errStr)
		return
	}
	created := false
	m, ok := sub.km.key.Load(key)
	if ok {
		if cmp.Equal(m, item) {
			log.Debugf("pubsub.handleModify(%s/%s) unchanged\n",
				name, key)
			return
		}
		log.Debugf("pubsub.handleModify(%s/%s) replacing due to diff",
			name, key)
		loggable, ok := item.(base.LoggableObject)
		if ok {
			loggable.LogModify(m)
		}
	} else {
		// DO NOT log Values. They may contain sensitive information.
		log.Debugf("pubsub.handleModify(%s) add for key %s\n",
			name, key)
		created = true
		loggable, ok := item.(base.LoggableObject)
		if ok {
			loggable.LogCreate()
		}
	}
	sub.km.key.Store(key, item)
	if log.GetLevel() == log.DebugLevel {
		sub.dump("after handleModify")
	}
	if created && sub.CreateHandler != nil {
		(sub.CreateHandler)(sub.userCtx, key, item)
	} else if sub.ModifyHandler != nil {
		(sub.ModifyHandler)(sub.userCtx, key, item)
	}
	log.Debugf("pubsub.handleModify(%s) done for key %s\n", name, key)
}

func handleDelete(ctxArg interface{}, key string) {
	sub := ctxArg.(*SubscriptionImpl)
	name := sub.nameString()
	log.Debugf("pubsub.handleDelete(%s) key %s\n", name, key)

	m, ok := sub.km.key.Load(key)
	if !ok {
		log.Errorf("pubsub.handleDelete(%s) %s key not found\n",
			name, key)
		return
	}
	loggable, ok := m.(base.LoggableObject)
	if ok {
		loggable.LogDelete()
	}
	// DO NOT log Values. They may contain sensitive information.
	log.Debugf("pubsub.handleDelete(%s) key %s", name, key)
	sub.km.key.Delete(key)
	if log.GetLevel() == log.DebugLevel {
		sub.dump("after handleDelete")
	}
	if sub.DeleteHandler != nil {
		(sub.DeleteHandler)(sub.userCtx, key, m)
	}
	log.Debugf("pubsub.handleDelete(%s) done for key %s\n", name, key)
}

func handleRestart(ctxArg interface{}, restarted bool) {
	sub := ctxArg.(*SubscriptionImpl)
	name := sub.nameString()
	log.Debugf("pubsub.handleRestart(%s) restarted %v\n", name, restarted)
	if restarted == sub.km.restarted {
		log.Debugf("pubsub.handleRestart(%s) value unchanged\n", name)
		return
	}
	sub.km.restarted = restarted
	if sub.RestartHandler != nil {
		(sub.RestartHandler)(sub.userCtx, restarted)
	}
	log.Debugf("pubsub.handleRestart(%s) done for restarted %v\n",
		name, restarted)
}

func handleSynchronized(ctxArg interface{}, synchronized bool) {
	sub := ctxArg.(*SubscriptionImpl)
	name := sub.nameString()
	log.Debugf("pubsub.handleSynchronized(%s) synchronized %v\n", name, synchronized)
	if synchronized == sub.synchronized {
		log.Debugf("pubsub.handleSynchronized(%s) value unchanged\n", name)
		return
	}
	sub.synchronized = synchronized
	if sub.SynchronizedHandler != nil {
		(sub.SynchronizedHandler)(sub.userCtx, synchronized)
	}
	log.Debugf("pubsub.handleSynchronized(%s) done for synchronized %v\n",
		name, synchronized)
}
