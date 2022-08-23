// Copyright © 2016 Zlatko Čalušić
//
// Use of this source code is governed by an MIT-style license that can be found in the LICENSE file.

package sysinfo

import (
	"bufio"
	"bytes"
	"fmt"
	"golang.org/x/sys/unix"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// StorageDevice information.
type StorageDevice struct {
	Name       string               `json:"name,omitempty"`
	Driver     string               `json:"driver,omitempty"`
	Vendor     string               `json:"vendor,omitempty"`
	Model      string               `json:"model,omitempty"`
	Serial     string               `json:"serial,omitempty"`
	Size       uint                 `json:"size,omitempty"` // device size in MB
	Partitions map[string]Partition `json:"partitions,omitempty"`
}

type Partition struct {
	MountPoint    string `json:"mountPoint,omitempty"`
	Size          uint   `json:"size,omitempty"`          // partition size in MB
	AvailableSize uint   `json:"availableSize,omitempty"` // available space in MB
}

func getSerial(name, fullpath string) (serial string) {
	var f *os.File
	var err error

	// Modern location/format of the udev database.
	if dev := slurpFile(path.Join(fullpath, "dev")); dev != "" {
		if f, err = os.Open(path.Join("/run/udev/data", "b"+dev)); err == nil {
			goto scan
		}
	}

	// Legacy location/format of the udev database.
	if f, err = os.Open(path.Join("/dev/.udev/db", "block:"+name)); err == nil {
		goto scan
	}

	// No serial :(
	return

scan:
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		if sl := strings.Split(s.Text(), "="); len(sl) == 2 {
			if sl[0] == "E:ID_SERIAL_SHORT" {
				serial = sl[1]
				break
			}
		}
	}

	return
}

func (si *SysInfo) getStorageInfo() {
	kbSize := 1000
	if si.Config.KBSize != 0 {
		kbSize = si.Config.KBSize
	}
	sysBlock := "/sys/block"
	devices, err := ioutil.ReadDir(sysBlock)
	if err != nil {
		return
	}

	procMounts := "/proc/mounts"
	var mountsInfo []byte
	mountsInfo, err = ioutil.ReadFile(procMounts)
	if err != nil {
		return
	}
	partmounts := make(map[string]string)
	s := bufio.NewScanner(bytes.NewBuffer(mountsInfo))
	for {
		if s.Scan() {
			line := s.Text()
			if strings.Index(line, "/dev/") == 0 {
				mountinfo := strings.Split(line, " ")
				_, exist := partmounts[mountinfo[0]]
				if !exist {
					partmounts[mountinfo[0]] = mountinfo[1]
				}
			}
		} else {
			break
		}
	}

	procParts := "/proc/partitions"
	var partsInfo []byte
	partsInfo, err = ioutil.ReadFile(procParts)
	mountsInfo, err = ioutil.ReadFile(procMounts)
	if err != nil {
		return
	}
	partsizes := make(map[string]string)
	s = bufio.NewScanner(bytes.NewBuffer(partsInfo))
	for {
		if s.Scan() {
			line := s.Text()
			regex := regexp.MustCompile(`\w+`)
			partinfo := regex.FindAllString(line, -1)
			if len(partinfo) == 4 {
				partsizes[partinfo[3]] = partinfo[2]
			}
		} else {
			break
		}
	}

	si.Storage = make([]StorageDevice, 0)
	for _, link := range devices {
		fullpath := path.Join(sysBlock, link.Name())
		dev, err := os.Readlink(fullpath)
		if err != nil {
			continue
		}

		if strings.HasPrefix(dev, "../devices/virtual/") {
			continue
		}

		// We could filter all removable devices here, but some systems boot from USB flash disks, and then we
		// would filter them, too. So, let's filter only floppies and CD/DVD devices, and see how it pans out.
		if strings.HasPrefix(dev, "../devices/platform/floppy") || slurpFile(path.Join(fullpath, "device", "type")) == "5" {
			continue
		}

		device := StorageDevice{
			Name:   link.Name(),
			Model:  slurpFile(path.Join(fullpath, "device", "model")),
			Serial: getSerial(link.Name(), fullpath),
		}

		if driver, err := os.Readlink(path.Join(fullpath, "device", "driver")); err == nil {
			device.Driver = path.Base(driver)
		}

		if vendor := slurpFile(path.Join(fullpath, "device", "vendor")); !strings.HasPrefix(vendor, "0x") {
			device.Vendor = vendor
		}

		size, _ := strconv.ParseUint(slurpFile(path.Join(fullpath, "size")), 10, 64)
		device.Size = uint(size * 512 / (uint64(kbSize) * uint64(kbSize))) // MiB
		devpath := fmt.Sprintf("/dev/%s", device.Name)
		parts := make(map[string]Partition)
		for part, mp := range partmounts {
			if strings.Index(part, devpath) == 0 {
				partName := part[5:]
				var psize uint
				sizeStr, ok := partsizes[partName]
				if ok {
					size, _ := strconv.ParseUint(sizeStr, 10, 64)
					psize = uint(size * 1024 / uint64(kbSize) / uint64(kbSize))
				}
				partition := Partition{
					MountPoint: mp,
					Size:       psize,
				}
				asize, err := diskUsage(mp)
				if err == nil {
					partition.AvailableSize = uint(asize / 1024 / 1024)
				}
				parts[partName] = partition

			}
		}
		if len(parts) > 0 {
			device.Partitions = parts
		}
		si.Storage = append(si.Storage, device)
	}
}

func diskUsage(path string) (used uint64, err error) {
	var stat unix.Statfs_t
	if err = unix.Statfs(path, &stat); err != nil {
		return
	}
	used = stat.Bavail * uint64(stat.Bsize)
	return
}
