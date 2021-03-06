/***** BEGIN LICENSE BLOCK *****
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this file,
# You can obtain one at http://mozilla.org/MPL/2.0/.
#
# The Initial Developer of the Original Code is the Mozilla Foundation.
# Portions created by the Initial Developer are Copyright (C) 2012
# the Initial Developer. All Rights Reserved.
#
# Contributor(s):
#   Rob Miller (rmiller@mozilla.com)
#   Mike Trinkala (trink@mozilla.com)
#
# ***** END LICENSE BLOCK *****/

package pipeline

import (
	"code.google.com/p/go-uuid/uuid"
	"code.google.com/p/goprotobuf/proto"
	"encoding/json"
	"fmt"
	"github.com/mozilla-services/heka/message"
	"log"
	"sync"
)

// Encapsulates access to a set of DecoderRunners.
type DecoderSet interface {
	// Returns running DecoderRunner registered under the specified name, or
	// nil and ok == false if no such name is registered.
	ByName(name string) (decoder DecoderRunner, ok bool)
	// Returns running DecoderRunner registered for the specified Heka
	// protocol encoding header.
	ByEncoding(enc message.Header_MessageEncoding) (decoder DecoderRunner, ok bool)
	// Returns the full set of running DecoderRunners, indexed by names under
	// which the were registered.
	AllByName() (decoders map[string]DecoderRunner)
}

type decoderSet struct {
	byName     map[string]DecoderRunner
	byEncoding []DecoderRunner
}

func newDecoderSet(wrappers map[string]*PluginWrapper) (ds *decoderSet, err error) {
	length := int32(topHeaderMessageEncoding) + 1
	ds = &decoderSet{
		byName:     make(map[string]DecoderRunner),
		byEncoding: make([]DecoderRunner, length),
	}
	var (
		d       Decoder
		dInt    interface{}
		dRunner DecoderRunner
		enc     message.Header_MessageEncoding
		name    string
		w       *PluginWrapper
		ok      bool
	)
	for name, w = range wrappers {
		if dInt, err = w.CreateWithError(); err != nil {
			return nil, fmt.Errorf("Failed creating decoder %s: %s", name, err)
		}
		if d, ok = dInt.(Decoder); !ok {
			return nil, fmt.Errorf("Not Decoder type: %s", name)
		}
		dRunner = NewDecoderRunner(name, d)
		ds.byName[name] = dRunner
	}
	for enc, name = range DecodersByEncoding {
		if dRunner, ok = ds.byName[name]; !ok {
			return nil, fmt.Errorf("Encoding registered decoder doesn't exist: %s",
				name)
		}
		ds.byEncoding[enc] = dRunner
	}
	return
}

func (ds *decoderSet) ByName(name string) (decoder DecoderRunner, ok bool) {
	decoder, ok = ds.byName[name]
	return
}

func (ds *decoderSet) ByEncoding(enc message.Header_MessageEncoding) (
	decoder DecoderRunner, ok bool) {

	iEnc := int(enc)
	if !(iEnc >= 0 && iEnc < len(ds.byEncoding)) {
		return
	}
	if decoder = ds.byEncoding[enc]; decoder != nil {
		ok = true
	}
	return
}

func (ds *decoderSet) AllByName() (decoders map[string]DecoderRunner) {
	return ds.byName
}

// Heka PluginRunner for Decoder plugins. Decoding is typically a simpler job,
// so these runners handle a bit more than the others.
type DecoderRunner interface {
	PluginRunner
	// Returns associated Decoder plugin object.
	Decoder() Decoder
	// Starts the DecoderRunner so it's listening for incoming PipelinePacks.
	// Should decrement the wait group after shut down has completed.
	Start(h PluginHelper, wg *sync.WaitGroup)
	// Returns the channel into which incoming PipelinePacks to be decoded
	// should be dropped.
	InChan() chan *PipelinePack
	// UUID to distinguish the duplicate instances of the same registered
	// Decoder plugin type from each other.
	UUID() string
}

type dRunner struct {
	pRunnerBase
	inChan chan *PipelinePack
	uuid   string
}

// Creates and returns a new (but not yet started) DecoderRunner for the
// provided Decoder plugin.
func NewDecoderRunner(name string, decoder Decoder) DecoderRunner {
	return &dRunner{
		pRunnerBase: pRunnerBase{name: name, plugin: decoder.(Plugin)},
		uuid:        uuid.NewRandom().String(),
		inChan:      make(chan *PipelinePack, Globals().PluginChanSize),
	}
}

func (dr *dRunner) Decoder() Decoder {
	return dr.plugin.(Decoder)
}

func (dr *dRunner) Start(h PluginHelper, wg *sync.WaitGroup) {
	go func() {
		var pack *PipelinePack

		defer func() {
			if r := recover(); r != nil {
				dr.LogError(fmt.Errorf("PANIC: %s", r))
				if pack != nil {
					pack.Recycle()
				}
				if Globals().Stopping {
					wg.Done()
				} else {
					dr.Start(h, wg)
				}
			}
		}()

		var err error
		for pack = range dr.inChan {
			if err = dr.Decoder().Decode(pack); err != nil {
				dr.LogError(err)
				pack.Recycle()
				continue
			}
			pack.Decoded = true
			h.PipelineConfig().router.InChan() <- pack
		}
		dr.LogMessage("stopped")
		wg.Done()
	}()
}

func (dr *dRunner) InChan() chan *PipelinePack {
	return dr.inChan
}

func (dr *dRunner) UUID() string {
	return dr.uuid
}

func (dr *dRunner) LogError(err error) {
	log.Printf("Decoder '%s' error: %s", dr.name, err)
}

func (dr *dRunner) LogMessage(msg string) {
	log.Printf("Decoder '%s': %s", dr.name, msg)
}

// Heka Decoder plugin interface.
type Decoder interface {
	// Extract data loaded into the PipelinePack (usually in pack.MsgBytes)
	// and use it to populated pack.Message message object.
	Decode(pack *PipelinePack) error
}

// Decoder for converting JSON strings into Message objects.
type JsonDecoder struct{}

func (self *JsonDecoder) Init(config interface{}) error {
	return nil
}

func (self *JsonDecoder) Decode(pack *PipelinePack) error {
	return json.Unmarshal(pack.MsgBytes, pack.Message)
}

// Decoder for converting ProtocolBuffer data into Message objects.
type ProtobufDecoder struct{}

func (self *ProtobufDecoder) Init(config interface{}) error {
	return nil
}

func (self *ProtobufDecoder) Decode(pack *PipelinePack) error {
	return proto.Unmarshal(pack.MsgBytes, pack.Message)
}
