// Copyright (c) 2019 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

// Zboot linux specific calls

// +build linux

package zboot

import (
	"syscall"
)

func zbootMount(devname string, target string, fstype string,
	flags MountFlags, data string) (err error) {
	var flagsLinux uintptr = 0
	if flags&MountFlagRDONLY != 0 {
		flagsLinux |= syscall.MS_RDONLY
	}
	return syscall.Mount(devname, target, fstype, flagsLinux, data)
}
