// Copyright 2016 The appc Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package typesV2

import (
	"encoding/json"
	"errors"

	"github.com/appc/docker2aci/lib/common"
)

var (
	ErrIncorrectMediaType = errors.New("incorrect mediaType")
	ErrMissingConfig      = errors.New("the config field is empty")
	ErrMissingLayers      = errors.New("the layers field is empty")
)

type ImageManifest struct {
	SchemaVersion int                    `json:"schemaVersion"`
	MediaType     string                 `json:"mediaType"`
	Config        *ImageManifestDigest   `json:"config"`
	Layers        []*ImageManifestDigest `json:"layers"`
	Annotations   map[string]string      `json:"annotations"`
}

type ImageManifestDigest struct {
	MediaType string `json:"mediaType"`
	Size      int    `json:"size"`
	Digest    string `json:"digest"`
}

func (im *ImageManifest) String() string {
	manblob, err := json.Marshal(im)
	if err != nil {
		return err.Error()
	}
	return string(manblob)
}

func (im *ImageManifest) PrettyString() string {
	manblob, err := json.MarshalIndent(im, "", "    ")
	if err != nil {
		return err.Error()
	}
	return string(manblob)
}

func (im *ImageManifest) Validate() error {
	if im.MediaType != common.MediaTypeDockerV22Manifest && im.MediaType != common.MediaTypeOCIV1Manifest {
		return ErrIncorrectMediaType
	}
	if im.Config == nil {
		return ErrMissingConfig
	}
	if len(im.Layers) == 0 {
		return ErrMissingLayers
	}
	return nil
}

type ImageConfig struct {
	Created      string                `json:"created"`
	Author       string                `json:"author"`
	Architecture string                `json:"architecture"`
	OS           string                `json:"os"`
	Config       *ImageConfigConfig    `json:"config"`
	RootFS       *ImageConfigRootFS    `json:"rootfs"`
	History      []*ImageConfigHistory `json:"history"`
}

type ImageConfigConfig struct {
	User         string              `json:"User"`
	Memory       int                 `json:"Memory"`
	MemorySwap   int                 `json:"MemorySwap"`
	CpuShares    int                 `json:"CpuShares"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	Env          []string            `json:"Env"`
	Entrypoint   []string            `json:"Entrypoint"`
	Cmd          []string            `json:"Cmd"`
	Volumes      map[string]struct{} `json:"Volumes"`
	WorkingDir   string              `json:"WorkingDir"`
}

type ImageConfigRootFS struct {
	DiffIDs []string `json:"diff_ids"`
	Type    string   `json:"type"`
}

type ImageConfigHistory struct {
	Created    string `json:"created,omitempty"`
	Author     string `json:"author,omitempty"`
	CreatedBy  string `json:"created_by,omitempty"`
	Comment    string `json:"comment,omitempty"`
	EmptyLayer bool   `json:"empty_layer,omitempty"`
}

func (ic *ImageConfig) String() string {
	manblob, err := json.Marshal(ic)
	if err != nil {
		return err.Error()
	}
	return string(manblob)
}

func (ic *ImageConfig) PrettyString() string {
	manblob, err := json.MarshalIndent(ic, "", "    ")
	if err != nil {
		return err.Error()
	}
	return string(manblob)
}
