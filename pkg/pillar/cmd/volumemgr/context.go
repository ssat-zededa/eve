// Copyright (c) 2020 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package volumemgr

import (
	"reflect"

	"github.com/lf-edge/eve/pkg/pillar/pubsub"
	"github.com/lf-edge/eve/pkg/pillar/types"
)

func (ctx *volumemgrContext) subscription(topicType interface{}, objType string) pubsub.Subscription {
	var sub pubsub.Subscription
	val := reflect.ValueOf(topicType)
	if val.Kind() == reflect.Ptr {
		log.Fatalf("subscription got a pointer type: %T", topicType)
	}
	switch typeName := topicType.(type) {
	case types.ContentTreeConfig:
		switch objType {
		case types.AppImgObj:
			sub = ctx.subContentTreeConfig
		case types.BaseOsObj:
			sub = ctx.subBaseOsContentTreeConfig
		default:
			log.Fatalf("subscription: Unknown ObjType %s for %T",
				objType, typeName)
		}
	default:
		log.Fatalf("subscription: Unknown typeName %T",
			typeName)
	}
	return sub
}

func (ctx *volumemgrContext) publication(topicType interface{}, objType string) pubsub.Publication {
	var pub pubsub.Publication
	val := reflect.ValueOf(topicType)
	if val.Kind() == reflect.Ptr {
		log.Fatalf("publication got a pointer type: %T", topicType)
	}
	switch typeName := topicType.(type) {
	case types.ContentTreeStatus:
		switch objType {
		case types.AppImgObj:
			pub = ctx.pubContentTreeStatus
		case types.BaseOsObj:
			pub = ctx.pubBaseOsContentTreeStatus
		default:
			log.Fatalf("publication: Unknown ObjType %s for %T",
				objType, typeName)
		}
	default:
		log.Fatalf("publication: Unknown typeName %T",
			typeName)
	}
	return pub
}
