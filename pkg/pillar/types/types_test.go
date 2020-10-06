package types

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestParseTriState(t *testing.T) {
	testMatrix := map[string]struct {
		err   error
		ts    TriState
		value string
	}{
		"Value none": {
			err:   nil,
			ts:    TS_NONE,
			value: "none",
		},

		"Value enable": {
			err:   nil,
			ts:    TS_ENABLED,
			value: "enable",
		},
		"Value enabled": {
			err:   nil,
			ts:    TS_ENABLED,
			value: "enabled",
		},
		"Value on": {
			err:   nil,
			ts:    TS_ENABLED,
			value: "on",
		},
		"Value disabled": {
			err:   nil,
			ts:    TS_DISABLED,
			value: "disabled",
		},
		"Value disable": {
			err:   nil,
			ts:    TS_DISABLED,
			value: "disable",
		},
		"Value off": {
			err:   nil,
			ts:    TS_DISABLED,
			value: "off",
		},
		"Value bad-value": {
			err:   fmt.Errorf("Bad value: bad-value"),
			ts:    TS_NONE,
			value: "bad-value",
		},
	}

	for testname, test := range testMatrix {
		t.Logf("Running test %s", testname)
		ts, err := ParseTriState(test.value)
		assert.IsType(t, test.err, err)
		assert.Equal(t, test.ts, ts)
	}
}
