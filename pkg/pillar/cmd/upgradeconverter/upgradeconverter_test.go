// Copyright (c) 2017-2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package upgradeconverter

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/lf-edge/eve/pkg/pillar/base"
	"github.com/lf-edge/eve/pkg/pillar/types"
	"github.com/sirupsen/logrus"
)

type testEntry struct {
	oldVersionExists bool
	newVersionExists bool
	oldVersionOlder  bool
	// newConfigPtr - If nil, verifies no NewCfgDir. If not, expect contents
	//  to match.
	newConfigPtr *types.ConfigItemValueMap
	// expectNoOldCfgDir - Verified oldConfigDir to be deleted.
	expectNoOldCfgDir bool
}

func oldGlobalConfig() types.OldGlobalConfig {
	config := types.OldGlobalConfig{}
	config = types.ApplyDefaults(config)
	// Set Some values
	config.ConfigInterval = 300
	config.AllowAppVnc = true
	config.AllowNonFreeBaseImages = types.TS_NONE
	config.DefaultLogLevel = "debug"
	config.AgentSettings["zedagent"] = types.PerAgentSettings{
		LogLevel: "info", RemoteLogLevel: "fatal"}
	return config
}

func newConfigItemValueMap() types.ConfigItemValueMap {
	config := types.DefaultConfigItemValueMap()
	config.SetGlobalValueInt(types.ConfigInterval, 400)
	config.SetGlobalValueBool(types.AllowAppVnc, false)
	config.SetGlobalValueTriState(types.AllowNonFreeBaseImages,
		types.TS_ENABLED)
	config.SetGlobalValueString(types.DefaultLogLevel, "warn")
	config.SetAgentSettingStringValue("zedagent", types.LogLevel, "debug")

	config.SetAgentSettingStringValue("zedagent", types.RemoteLogLevel, "crit")
	return *config
}

func createJSONFile(config interface{}, file string) {

	parentDir := filepath.Dir(file)
	if !fileExists(parentDir) {
		err := os.MkdirAll(parentDir, 0700)
		if err != nil {
			log.Fatalf("Failed to create Dir: %s", parentDir)
		}
		log.Tracef("Created Dir: %s", parentDir)
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		log.Fatalf("createJSONFile: failed to marshall. err %s\n config: %+v",
			err, config)
	}
	err = ioutil.WriteFile(file, configJSON, 0644)
	if err != nil {
		log.Fatalf("createJSONFile: failed to write file err %s", err)
	}
	return
}

func configItemValueMapFromFile(file string) *types.ConfigItemValueMap {
	var newConfig types.ConfigItemValueMap
	cfgJSON, err := ioutil.ReadFile(file)
	if err != nil {
		log.Errorf("***configItemValueMapFromFile - Failed to read from %s. "+
			"Err: %s", file, err)
		return nil
	}
	err = json.Unmarshal(cfgJSON, &newConfig)
	if err == nil {
		return &newConfig
	}
	log.Errorf("***configItemValueMapFromFile - Failed to unmarshall data: %+v",
		cfgJSON)
	return nil
}

func checkNoDir(t *testing.T, dir string) {
	if fileExists(dir) {
		t.Fatalf("***Dir %s Still Present. Expected it to be deleted.", dir)
	}
}

func ucContextForTest() *ucContext {
	//log.SetLevel(log.TraceLevel)
	var err error
	ctxPtr := &ucContext{}
	ctxPtr.persistDir, err = ioutil.TempDir(".", "PersistDir")
	if err != nil {
		log.Fatalf("Failed to create persistDir. err: %s", err)
	}
	ctxPtr.persistConfigDir, err = ioutil.TempDir(".", "PersistConfigDir")
	if err != nil {
		log.Fatalf("Failed to create persistConfigDir. err: %s", err)
	}
	ctxPtr.persistStatusDir, err = ioutil.TempDir(".", "PersistStatusDir")
	if err != nil {
		log.Fatalf("Failed to create persistStatusDir. err: %s", err)
	}
	return ctxPtr
}

func ucContextCleanupDirs(ctxPtr *ucContext) {
	os.RemoveAll(ctxPtr.persistDir)
	ctxPtr.persistDir = ""
	os.RemoveAll(ctxPtr.persistConfigDir)
	ctxPtr.persistConfigDir = ""
	os.RemoveAll(ctxPtr.persistStatusDir)
	ctxPtr.persistStatusDir = ""
}

func runTestMatrix(t *testing.T, testMatrix map[string]testEntry) {
	logrus.SetLevel(logrus.DebugLevel)
	log = base.NewSourceLogObject(logrus.StandardLogger(), "test", 1234)
	oldConfig := oldGlobalConfig()
	newConfig := newConfigItemValueMap()

	for testname, test := range testMatrix {
		t.Logf("Running test case %s", testname)
		ctxPtr := ucContextForTest()
		if test.oldVersionExists && test.oldVersionOlder {
			createJSONFile(oldConfig, ctxPtr.globalConfigFile())
		}
		if test.newVersionExists {
			createJSONFile(newConfig, ctxPtr.oldConfigItemValueMapFile())
		}
		if test.oldVersionExists && !test.oldVersionOlder {
			time.Sleep(2 * time.Second)
			createJSONFile(oldConfig, ctxPtr.globalConfigFile())
		}
		err := convertGlobalConfig(ctxPtr)
		if err != nil {
			t.Fatalf("Unexpected Failure in GlobalConfigHandler. err: %s", err)
		}
		if test.newConfigPtr == nil {
			checkNoDir(t, ctxPtr.oldConfigItemValueMapDir())
		} else {
			newCfgFromFile := configItemValueMapFromFile(
				ctxPtr.oldConfigItemValueMapFile())
			if !cmp.Equal(test.newConfigPtr, newCfgFromFile) {
				msg := ""
				for key, value := range test.newConfigPtr.GlobalSettings {
					newVal, ok := newCfgFromFile.GlobalSettings[key]
					if !ok {
						msg += fmt.Sprintf("Key %s not present in newCfgFromFile",
							key)
						continue
					} else if value != newVal {
						msg += fmt.Sprintf("Key %s value != newVal\n"+
							"Value: %+v\nnewVal: %+v\n", key, value, newVal)
					}
				}
				t.Fatalf("Expected newConfig !=  Actual newConfig.\nDIFF: %s",
					msg)
			}
		}
		if test.expectNoOldCfgDir {
			checkNoDir(t, ctxPtr.globalConfigDir())
		}
		ucContextCleanupDirs(ctxPtr)
	}

}

func Test_ConvertGlobalConfig(t *testing.T) {
	oldConfig := oldGlobalConfig()
	newConfig := newConfigItemValueMap()
	convertedConfig := oldConfig.MoveBetweenConfigs()

	testMatrix := map[string]testEntry{
		"Convert: Neither Old Version Nor New Version exist.": {
			// Does notthing
			oldVersionExists:  false,
			newVersionExists:  false,
			expectNoOldCfgDir: true,
		},
		"Convert: Old Version Exists, No New Version - Normal Upgrade case": {
			// Old Converted to New
			oldVersionExists:  true,
			newConfigPtr:      convertedConfig,
			expectNoOldCfgDir: true,
		},
		"Convert: Old Version Older than New Version": {
			// oldVersion Ignored. New version used.
			oldVersionExists:  true,
			newVersionExists:  true,
			oldVersionOlder:   true,
			newConfigPtr:      &newConfig,
			expectNoOldCfgDir: true,
		},
		"Convert: Old Version Newer than New Version": {
			// New Version Regenerated ( Convert Old to New)
			oldVersionExists:  true,
			newVersionExists:  true,
			oldVersionOlder:   false,
			newConfigPtr:      convertedConfig,
			expectNoOldCfgDir: true,
		},
		"Convert: Only New Version exists. Upgrade from one new version to another": {
			// New Version untouched
			newVersionExists:  true,
			newConfigPtr:      &newConfig,
			expectNoOldCfgDir: true,
		},
	}
	runTestMatrix(t, testMatrix)
}

func Test_MoveConfigItem(t *testing.T) {
	oldConfig := newConfigItemValueMap()
	newConfig := types.DefaultConfigItemValueMap()
	newConfig.SetGlobalValueString(types.DefaultLogLevel, "trace")

	type moveTestEntry struct {
		oldVersionExists bool
		newVersionExists bool
		oldVersionOlder  bool
		// newConfigPtr - If nil, verifies no NewCfgDir. If not, expect contents
		//  to match.
		newConfigPtr *types.ConfigItemValueMap
		// expectOldDir - Verified old dir to still exist
		expectOldDir bool
	}

	testMatrix := map[string]moveTestEntry{
		"Move: Neither Old Version Nor New Version exist.": {
			// Nothing copied/created
			oldVersionExists: false,
			newVersionExists: false,
		},
		"Move: Old Version Exists, No New Version - Normal copy case": {
			oldVersionExists: true,
			newConfigPtr:     &oldConfig,
		},
		"Move: Old Version Older than New Version": {
			// oldVersion Ignored. New version used.
			oldVersionExists: true,
			newVersionExists: true,
			oldVersionOlder:  true,
			newConfigPtr:     newConfig,
		},
		"Move: Old Version Newer than New Version": {
			// New Version Regenerated by copying old
			oldVersionExists: true,
			newVersionExists: true,
			oldVersionOlder:  false,
			newConfigPtr:     &oldConfig,
		},
		"Move: Only New Version exists. Upgrade from one new version to another": {
			// New Version untouched
			newVersionExists: true,
			newConfigPtr:     newConfig,
		},
	}

	logrus.SetLevel(logrus.DebugLevel)
	log = base.NewSourceLogObject(logrus.StandardLogger(), "test", 1234)
	for testname, test := range testMatrix {
		t.Logf("Running test case %s", testname)
		ctxPtr := ucContextForTest()
		if test.oldVersionExists && test.oldVersionOlder {
			createJSONFile(oldConfig, ctxPtr.oldConfigItemValueMapFile())
		}
		if test.newVersionExists {
			createJSONFile(newConfig, ctxPtr.newConfigItemValueMapFile())
		}
		if test.oldVersionExists && !test.oldVersionOlder {
			time.Sleep(2 * time.Second)
			createJSONFile(oldConfig, ctxPtr.oldConfigItemValueMapFile())
		}
		err := moveConfigItemValueMap(ctxPtr)
		if err != nil {
			t.Fatalf("Unexpected Failure in convertGlobalConfig. err: %s", err)
		}
		if test.newConfigPtr == nil {
			checkNoDir(t, ctxPtr.oldConfigItemValueMapDir())
		} else {
			newCfgFromFile := configItemValueMapFromFile(
				ctxPtr.newConfigItemValueMapFile())
			if !cmp.Equal(test.newConfigPtr, newCfgFromFile) {
				msg := ""
				for key, value := range test.newConfigPtr.GlobalSettings {
					newVal, ok := newCfgFromFile.GlobalSettings[key]
					if !ok {
						msg += fmt.Sprintf("Key %s not present in newCfgFromFile",
							key)
						continue
					} else if value != newVal {
						msg += fmt.Sprintf("Key %s value != newVal\n"+
							"Value: %+v\nnewVal: %+v\n", key, value, newVal)
					}
				}
				t.Fatalf("Expected newConfig !=  Actual newConfig.\nDIFF: %s",
					msg)
			}
		}
		if !test.expectOldDir {
			checkNoDir(t, ctxPtr.oldConfigItemValueMapDir())
		}
		ucContextCleanupDirs(ctxPtr)
	}
}

func Test_ApplyDefaultConfigItem(t *testing.T) {
	oldConfig := newConfigItemValueMap()
	delete(oldConfig.GlobalSettings, types.DefaultLogLevel)
	delete(oldConfig.GlobalSettings, types.AllowNonFreeBaseImages)
	delete(oldConfig.GlobalSettings, types.UsbAccess)
	delete(oldConfig.GlobalSettings, types.DiskScanMetricInterval)
	defaultConfig := types.DefaultConfigItemValueMap()
	newConfig := types.DefaultConfigItemValueMap()
	newConfig.UpdateItemValues(&oldConfig)
	badConfig := types.DefaultConfigItemValueMap()
	badConfig.UpdateItemValues(&oldConfig)
	delete(badConfig.GlobalSettings, types.DiskScanMetricInterval)

	type applyTestEntry struct {
		// fileExists causes creation of oldConfig
		fileExists bool
		// newConfigPtr - If nil, verifies no NewCfgDir. If not, expect contents
		//  to match.
		newConfigPtr *types.ConfigItemValueMap
		// expectDiffs: there should be diffs
		expectDiffs bool
	}

	testMatrix := map[string]applyTestEntry{
		"Apply:  Old Version does not exist": {
			// Gets default
			fileExists:   false,
			newConfigPtr: defaultConfig,
		},
		"Apply: Old Version Exists": {
			fileExists:   true,
			newConfigPtr: newConfig,
		},
		"Apply: Old Version Exists but mismatch": {
			fileExists:   true,
			newConfigPtr: badConfig,
			expectDiffs:  true,
		},
	}

	logrus.SetLevel(logrus.DebugLevel)
	log = base.NewSourceLogObject(logrus.StandardLogger(), "test", 1234)
	for testname, test := range testMatrix {
		t.Logf("Running test case %s", testname)
		ctxPtr := ucContextForTest()
		if test.fileExists {
			createJSONFile(oldConfig, ctxPtr.newConfigItemValueMapFile())
		}
		err := applyDefaultConfigItem(ctxPtr)
		if err != nil {
			t.Fatalf("Unexpected Failure in applyDefaultConfigItem. err: %s", err)
		}
		if test.newConfigPtr == nil {
			checkNoDir(t, ctxPtr.newConfigItemValueMapDir())
		} else {
			newCfgFromFile := configItemValueMapFromFile(
				ctxPtr.newConfigItemValueMapFile())
			if !cmp.Equal(test.newConfigPtr, newCfgFromFile) {
				msg := fmt.Sprintf("DIFF: %+v",
					cmp.Diff(test.newConfigPtr, newCfgFromFile))
				if test.expectDiffs {
					t.Logf("Expected diff. Got %s", msg)
				} else {
					t.Fatalf("Expected newConfig != Actual newConfig.\n%s",
						msg)
				}
			} else if test.expectDiffs {
				t.Fatalf("Expected diff but got none")
			}
		}
		ucContextCleanupDirs(ctxPtr)
	}
}
