// Code generated by go-bindata.
// sources:
// 000001_add_jobs.down.sql
// 000001_add_jobs.up.sql
// 000002_add_log.down.sql
// 000002_add_log.up.sql
// bindata.go
// DO NOT EDIT!

package migrations

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func bindataRead(data []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("Read %q: %v", name, err)
	}

	var buf bytes.Buffer
	_, err = io.Copy(&buf, gz)
	clErr := gz.Close()

	if err != nil {
		return nil, fmt.Errorf("Read %q: %v", name, err)
	}
	if clErr != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

type asset struct {
	bytes []byte
	info  os.FileInfo
}

type bindataFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
}

func (fi bindataFileInfo) Name() string {
	return fi.name
}
func (fi bindataFileInfo) Size() int64 {
	return fi.size
}
func (fi bindataFileInfo) Mode() os.FileMode {
	return fi.mode
}
func (fi bindataFileInfo) ModTime() time.Time {
	return fi.modTime
}
func (fi bindataFileInfo) IsDir() bool {
	return false
}
func (fi bindataFileInfo) Sys() interface{} {
	return nil
}

var __000001_add_jobsDownSql = []byte("\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff\x72\x09\xf2\x0f\x50\x08\x71\x74\xf2\x71\x55\xf0\x74\x53\x70\x8d\xf0\x0c\x0e\x09\x56\x48\xca\x2f\xcd\x4b\x4e\x2d\x8a\xcf\xca\x4f\x2a\xb6\x06\x04\x00\x00\xff\xff\x48\x2a\xa6\xb6\x22\x00\x00\x00")

func _000001_add_jobsDownSqlBytes() ([]byte, error) {
	return bindataRead(
		__000001_add_jobsDownSql,
		"000001_add_jobs.down.sql",
	)
}

func _000001_add_jobsDownSql() (*asset, error) {
	bytes, err := _000001_add_jobsDownSqlBytes()
	if err != nil {
		return nil, err
	}

	info := bindataFileInfo{name: "000001_add_jobs.down.sql", size: 34, mode: os.FileMode(420), modTime: time.Unix(1570936992, 0)}
	a := &asset{bytes: bytes, info: info}
	return a, nil
}

var __000001_add_jobsUpSql = []byte("\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff\x54\x8d\x31\x4b\xc5\x30\x14\x46\xf7\xfe\x8a\x6f\x4c\xc0\x49\x47\xa7\x58\x2e\x12\x5e\x9a\xa7\x21\x11\x3a\x35\x79\x7d\x45\x23\x31\x85\x34\x16\x7f\xbe\xd0\xa2\xd5\x3b\xdd\xe1\x7c\xe7\xb4\x86\x84\x25\x58\xf1\xa0\x08\x97\xf9\x33\x8f\x53\x19\xde\xe7\xcb\xd2\xb0\x06\x00\xe2\x15\x3f\x27\xb5\xa5\x47\x32\xdb\xaf\xcf\x16\xda\x29\x05\xe1\xec\x79\x90\xba\x35\xd4\x91\xb6\x78\x32\xb2\x13\xa6\xc7\x89\xfa\x9b\x6d\x3f\xa6\x38\xe5\xda\x89\x16\x58\x43\x19\xdf\x42\x61\x77\xb7\xfc\xd8\x3b\x2d\x9f\x1d\xed\x6c\x0d\xe5\x75\xaa\x2f\x4a\x68\x2c\x1f\x21\xa5\x98\xeb\xdf\xd6\x0e\x9d\xa8\x47\xbc\x7e\x0d\xbb\x18\xcc\xff\x16\x3c\xff\x4f\xac\x29\x64\x30\x7f\x58\x3d\x6f\xf8\xfd\x77\x00\x00\x00\xff\xff\x0a\x0b\x40\x5a\xf1\x00\x00\x00")

func _000001_add_jobsUpSqlBytes() ([]byte, error) {
	return bindataRead(
		__000001_add_jobsUpSql,
		"000001_add_jobs.up.sql",
	)
}

func _000001_add_jobsUpSql() (*asset, error) {
	bytes, err := _000001_add_jobsUpSqlBytes()
	if err != nil {
		return nil, err
	}

	info := bindataFileInfo{name: "000001_add_jobs.up.sql", size: 241, mode: os.FileMode(420), modTime: time.Unix(1570936985, 0)}
	a := &asset{bytes: bytes, info: info}
	return a, nil
}

var __000002_add_logDownSql = []byte("\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff\x72\x09\xf2\x0f\x50\x08\x71\x74\xf2\x71\x55\xf0\x74\x53\x70\x8d\xf0\x0c\x0e\x09\x56\x48\xca\x2f\xcd\x4b\x4e\x2d\x8a\xcf\xc9\x4f\xb7\x06\x04\x00\x00\xff\xff\x19\x67\x76\xef\x21\x00\x00\x00")

func _000002_add_logDownSqlBytes() ([]byte, error) {
	return bindataRead(
		__000002_add_logDownSql,
		"000002_add_log.down.sql",
	)
}

func _000002_add_logDownSql() (*asset, error) {
	bytes, err := _000002_add_logDownSqlBytes()
	if err != nil {
		return nil, err
	}

	info := bindataFileInfo{name: "000002_add_log.down.sql", size: 33, mode: os.FileMode(420), modTime: time.Unix(1570936999, 0)}
	a := &asset{bytes: bytes, info: info}
	return a, nil
}

var __000002_add_logUpSql = []byte("\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff\x84\x8f\x3f\x6f\x83\x30\x10\xc5\x77\x3e\xc5\x8d\x20\x65\x4a\xff\x2c\x9d\x5c\x7a\xad\x50\xb0\x83\xdc\xa3\x12\x13\x76\x01\x35\x96\x5c\x23\x11\x37\xe9\xc7\xaf\xc0\x81\xa6\x2c\xf5\xe2\x93\xee\xf7\xee\xbd\x97\x4a\x64\x84\x40\xec\x31\x47\x78\xef\xbf\x5c\xd3\x0d\xb5\xed\x3f\xa2\x38\x02\x00\x30\x2d\xcc\x2f\x13\x84\x2f\x28\xa7\x59\xec\x09\x44\x99\xe7\xc0\x4a\xda\xd7\x99\x48\x25\x72\x14\x04\x85\xcc\x38\x93\x15\xec\xb0\xda\x4c\xfa\xc6\x9a\xce\x79\xce\x52\x80\x93\x1e\x9a\x83\x1e\xe2\x9b\x6d\xb2\xe8\x03\xd4\x6a\xdf\x5d\x4c\x28\xe3\xf8\x4a\x8c\x17\xd7\x26\x4f\xf8\xcc\xca\x9c\x20\x2d\xa5\x44\x41\xf5\x02\x05\x79\x6f\xdb\xb7\x9c\x89\x71\x3c\x7e\x6a\x6b\x8d\xf3\xd7\x19\x03\xe4\xba\xf3\xff\xd0\xf1\x6c\x7c\x73\xc8\x46\xf3\x39\xed\xf6\x6e\x9d\x36\x40\x45\x3f\xf8\x05\xba\xbf\x5d\x43\x3b\xac\xc0\xb4\xdf\xf5\x54\x2d\x56\xe3\xa7\x92\xbf\xab\x70\x07\x62\x35\xbb\xaa\x0d\xa8\xdf\xe3\x6b\xfc\x64\xb5\x83\x58\x5d\x7a\xa8\x24\x4a\x1e\x7e\x02\x00\x00\xff\xff\x7a\x5b\xb3\x01\xbc\x01\x00\x00")

func _000002_add_logUpSqlBytes() ([]byte, error) {
	return bindataRead(
		__000002_add_logUpSql,
		"000002_add_log.up.sql",
	)
}

func _000002_add_logUpSql() (*asset, error) {
	bytes, err := _000002_add_logUpSqlBytes()
	if err != nil {
		return nil, err
	}

	info := bindataFileInfo{name: "000002_add_log.up.sql", size: 444, mode: os.FileMode(420), modTime: time.Unix(1570937003, 0)}
	a := &asset{bytes: bytes, info: info}
	return a, nil
}

var _bindataGo = []byte("\x1f\x8b\x08\x00\x00\x00\x00\x00\x00\xff\xd4\x57\xdd\x6e\xdb\x46\x13\xbd\xe6\x3e\xc5\x46\x40\x02\xf2\x83\x3e\x99\xff\x3f\x02\x7c\x93\x38\x05\x72\xd1\x04\x68\x92\xab\x4e\x61\x2c\xb9\xbb\x2a\x5b\x89\x54\x48\x2a\x19\xdb\xf0\xbb\x17\xc3\xa1\x6c\xd9\xb1\x9c\xc0\x4d\x2e\x6a\x80\x12\xb9\xcb\x99\x39\x67\xf7\xcc\x59\xeb\xe4\x44\xbe\x6a\xb5\x91\x2b\xd3\x98\x4e\x0d\x46\xcb\xf2\x42\xae\xda\xff\x97\x75\xa3\xd5\xa0\x16\xe2\xe4\x44\xf6\xed\xae\xab\x4c\xbf\xa4\x7b\x9f\xfe\x82\x73\xa5\xf5\xf9\x5f\x6d\xd9\x2f\x74\xfb\xa5\x59\xf4\x9f\xd6\x0f\xcd\xed\xb6\x77\x66\xc2\x71\x66\xdd\xae\xbe\x0e\xba\x9d\xba\x8d\xd9\x23\x58\xb5\xf4\x74\xf6\x4e\xbe\x7d\xf7\x41\xbe\x3e\x7b\xf3\xe1\x99\x10\x5b\x55\xfd\xad\x56\x46\x6e\xea\x55\xa7\x86\xba\x6d\x7a\x21\xea\xcd\xb6\xed\x06\xe9\x0a\x67\x56\x5e\x0c\xa6\x9f\x09\x67\x56\xb5\x9b\x6d\x67\xfa\xfe\x64\x75\x59\x6f\x69\xc0\x6e\x06\xfa\xaa\x5b\xfe\x3c\xa9\xdb\xdd\x50\xaf\xe9\xa1\x1d\x03\xb6\x6a\xf8\xf3\xc4\xd6\x6b\x43\x37\x34\xd0\x0f\x5d\xdd\xac\xc6\xb9\xa1\xde\x98\x99\xf0\x84\xb0\xbb\xa6\xda\xc3\xfb\xcd\x28\xed\xd2\x8d\xfc\xfd\x0f\x2a\x3b\x97\x8d\xda\x18\xc9\x61\x9e\x74\xf7\xa3\xa6\xeb\xda\xce\x93\x57\xc2\x59\x5d\x8e\x4f\x72\x79\x2a\x09\xd5\xe2\xad\xf9\x42\x49\x4c\xe7\x8e\xb0\xe9\xf9\xe5\xce\x5a\xd3\x8d\x69\x3d\x4f\x38\xb5\x1d\x03\x9e\x9d\xca\xa6\x5e\x53\x0a\xa7\x33\xc3\xae\x6b\xe8\x71\x2e\xed\x66\x58\xbc\xa6\xec\xd6\x9d\x51\x22\xf9\xfc\xd3\x52\x3e\xff\x3c\x63\x24\x63\x2d\x4f\x38\xd7\x42\x38\x9f\x55\x27\xcb\x9d\x95\x5c\x87\x8b\x08\xe7\x9c\xe1\x9c\xca\xba\x5d\xbc\x6a\xb7\x17\xee\x8b\x72\x67\xe7\x72\x75\xe9\x09\xa7\x5a\xbf\xde\x23\x5d\xbc\x5a\xb7\xbd\x71\x3d\xf1\xa3\xf0\x50\x1a\xce\x7f\x24\x91\xe9\x3a\xc6\x3d\x0d\x96\x3b\xbb\x78\x49\xd0\x5d\x6f\x4e\x6f\x88\x6b\x21\x86\x8b\xad\x91\xaa\xef\xcd\x40\x4b\xbe\xab\x06\xca\x32\xf2\x9b\xf6\x43\x38\x75\x63\x5b\x29\xdb\x7e\xf1\x4b\xbd\x36\x6f\x1a\xdb\xde\xc4\x4d\x5b\xb8\x1f\x3f\xc8\x30\xee\xa1\x94\xd3\x36\x0a\xa7\xaf\x2f\xc7\xe7\xba\x19\xd2\x58\x38\x1b\xea\x18\x79\x93\xf4\xd7\x56\x9b\x71\xf0\x43\xbd\x31\x92\x64\xb2\xa0\x3b\xaa\x33\x4a\xc5\xb5\xf5\xfd\x5a\x9e\x7c\xab\x36\xc6\xf5\xa6\x0a\x54\x73\x62\x69\xeb\x05\x55\x17\xd7\x8f\xc4\xbe\xaf\x2f\x29\x76\x44\x73\x37\x94\x80\x3e\x1a\x4a\x58\x5d\xef\x10\xf9\xdd\x04\x44\xed\x5b\x09\x88\x9c\xeb\xdd\x12\xfd\x2a\xc3\xc4\xfe\x78\x92\x37\xfd\x59\xdd\xb9\x9e\x2c\xdb\x76\x7d\x18\xad\xd6\xfd\x37\x98\x5f\xf4\x4c\xdc\x74\x56\x55\xe6\xea\xfa\x20\x7a\x92\x04\xa9\xfc\xfc\xfc\x9e\x17\x9d\xb5\x5f\x9a\xf7\x9f\xd6\xf2\x74\x92\x85\x3b\x03\x0c\x2c\x60\x5e\x02\xfa\x39\xa0\xef\x3f\x7c\x59\x0b\x98\x85\x80\x7e\x01\x68\xe9\xdb\x02\x26\x3e\xc7\x64\x01\x60\x16\xf3\x38\xdd\x27\x09\xa0\xf5\x79\x2c\x89\x00\x33\x1f\x30\xd7\x3c\xe6\x57\x80\xbe\xe1\x3c\x49\x0a\x18\xe7\x80\x95\x02\x0c\x2d\x60\xa5\x01\xe3\x12\x30\x36\x80\xa1\x06\xcc\x15\x60\x65\x79\x3e\xb6\x80\xa1\x02\x2c\x53\x40\x9f\xae\xf8\x2e\x36\xba\x28\x17\xbd\xa3\x52\x7e\x2f\x0c\x0f\x39\xcc\xf6\x86\x75\x64\x49\xa6\x8e\x7a\xc8\xa9\xf6\x7d\x77\xe0\x74\xc2\x71\x8e\xad\xed\x5c\x38\xce\xec\xd8\xf1\x30\x9b\x0b\xc7\xbb\x69\x88\x23\x19\x08\xc5\xff\xc6\x66\x3e\x44\x31\x76\xf3\x8d\x65\x3e\xce\xe2\x5b\xde\x74\x63\x29\xa3\x29\x2c\x4f\xef\x0b\xec\x8a\x5a\x6f\x29\x1f\xa1\x21\xa9\xc3\x96\x32\x8a\xe7\x92\x5a\x65\x79\xd8\x49\x6e\x1c\xfa\xde\x38\x4e\x0d\xb0\xe4\x06\xf9\xd8\xd4\xe8\x06\x49\xe6\x17\x51\x5a\x14\xe1\x5c\xfa\xde\xb5\x70\x14\x15\x7f\x31\x72\xbd\x1a\x09\x2e\xe5\xc4\x93\x90\x2d\xc7\xcf\xeb\x9b\x1d\x50\xf3\x47\xc5\xfd\x71\xfb\x54\x69\x27\x31\x4b\x34\x0a\x58\x82\x55\x02\x18\xf9\x80\x41\x0c\x18\xa7\x80\x36\x03\xb4\x86\x25\x99\x92\xd4\x2a\xc0\xca\x07\x8c\x0b\xc0\x38\x03\x54\x19\x60\x42\xf2\x33\x80\x41\x08\x98\x18\xc0\x42\xf1\x78\x18\x00\x06\x01\x60\xa4\x00\xa3\x04\x30\x2b\x00\x33\x92\x7a\x02\x18\x46\x5c\x33\xa7\x7a\x31\x60\x90\x02\x66\x16\xb0\x34\x80\xda\x07\x54\x21\xa0\xa6\xb9\x12\x50\x6b\x40\x43\xad\x56\x01\x9a\x0c\xb0\x24\xcc\x29\x60\x1e\x03\x86\x09\xd7\xb7\x01\xa0\x9a\xda\xb2\x20\xcc\x05\x60\x14\x01\xe6\x96\x5b\x31\x28\x00\xb5\xe1\xf8\x8a\x72\x86\x80\xa5\x3f\xb5\x94\x0f\x68\x42\xc0\x80\xea\x51\xbb\x51\x8d\x04\x50\x25\xcc\x31\x22\x2c\x25\xa0\xb2\xdc\x96\x84\x55\x53\xeb\x16\x80\x7e\xc2\xd8\x4c\xc5\xfc\x0a\x1f\xb0\x9c\xf8\xea\x18\xb0\x08\xb8\x25\xb3\x9c\xf3\x94\x54\x27\xe2\x56\xad\x32\xc0\x9c\x6c\x45\x01\x16\x25\x60\xaa\xb9\x3e\xcd\x45\x39\xa0\x49\xb8\x0e\xbd\x43\x35\x89\x67\xbc\xe7\x42\x7b\x11\x02\xa6\xb4\x2e\x84\x97\x38\x57\x80\x3a\xe7\x35\x23\x1b\x29\x08\x2b\xad\x9d\x06\x4c\xc9\x7e\x34\xe7\xcc\x88\xdb\x64\x3d\x31\xed\x2b\xed\x5f\xc5\x9a\xa1\x3d\x23\xde\x05\xd5\x2f\xb9\x8e\x4e\xd9\xba\x0a\x0d\xa8\x72\x5e\x93\x92\xf8\x1a\xce\x59\x96\x80\x01\xd9\x59\xc5\x9a\x22\xac\xd1\x74\x4f\xf6\xa5\x2a\x5e\xab\x34\x66\x6d\xd1\x3e\x13\x97\x48\xb3\xa6\x2c\xed\x9f\x66\x1e\xf7\xf5\x49\x97\xaf\x00\x7d\xb2\x47\x1f\x30\x51\xbc\xd7\xdf\x61\x6f\x63\x53\xfc\x7b\x73\x1b\xd3\x3c\x68\x6d\xfc\x9f\xea\xe3\xc6\x36\x46\x3f\xc5\xd6\x0e\xd1\xff\x2c\x53\xdb\x13\x98\x2c\x2d\x8c\x83\x27\x79\x5a\x9e\xfc\x38\x4f\xbb\xf9\x1d\xf0\x9f\x3c\xaf\x0b\x16\x3c\x35\xe3\xb1\xb3\x9a\x9a\x37\xcd\x00\xb3\x14\xd0\x4c\x0d\x77\x54\xcc\xf7\x57\xe3\xa9\x6a\xbe\x9f\xe7\x56\xce\x5f\xff\x24\x7b\x48\xcf\xf7\xe3\xbf\x5f\xd0\x47\x18\xfc\x50\x45\x3f\xc4\x61\x7f\x4a\x47\x4f\x3b\xa5\x8b\x9f\xa0\xe8\xa7\x1f\xd2\x74\xd0\xd1\x41\x46\x87\x03\x19\x66\x1e\x4d\x87\xb4\xcf\x07\x36\x19\x67\x64\xf8\x9e\xf4\x1b\xfa\x80\x69\xc2\xe6\x4e\xf1\x64\xee\x64\xde\x09\x99\x36\x1d\xce\x9a\x7b\x82\x0e\xc0\xfc\x9f\x00\x00\x00\xff\xff\x7f\x58\x79\x62\x00\x10\x00\x00")

func bindataGoBytes() ([]byte, error) {
	return bindataRead(
		_bindataGo,
		"bindata.go",
	)
}

func bindataGo() (*asset, error) {
	bytes, err := bindataGoBytes()
	if err != nil {
		return nil, err
	}

	info := bindataFileInfo{name: "bindata.go", size: 8192, mode: os.FileMode(420), modTime: time.Unix(1570937005, 0)}
	a := &asset{bytes: bytes, info: info}
	return a, nil
}

// Asset loads and returns the asset for the given name.
// It returns an error if the asset could not be found or
// could not be loaded.
func Asset(name string) ([]byte, error) {
	cannonicalName := strings.Replace(name, "\\", "/", -1)
	if f, ok := _bindata[cannonicalName]; ok {
		a, err := f()
		if err != nil {
			return nil, fmt.Errorf("Asset %s can't read by error: %v", name, err)
		}
		return a.bytes, nil
	}
	return nil, fmt.Errorf("Asset %s not found", name)
}

// MustAsset is like Asset but panics when Asset would return an error.
// It simplifies safe initialization of global variables.
func MustAsset(name string) []byte {
	a, err := Asset(name)
	if err != nil {
		panic("asset: Asset(" + name + "): " + err.Error())
	}

	return a
}

// AssetInfo loads and returns the asset info for the given name.
// It returns an error if the asset could not be found or
// could not be loaded.
func AssetInfo(name string) (os.FileInfo, error) {
	cannonicalName := strings.Replace(name, "\\", "/", -1)
	if f, ok := _bindata[cannonicalName]; ok {
		a, err := f()
		if err != nil {
			return nil, fmt.Errorf("AssetInfo %s can't read by error: %v", name, err)
		}
		return a.info, nil
	}
	return nil, fmt.Errorf("AssetInfo %s not found", name)
}

// AssetNames returns the names of the assets.
func AssetNames() []string {
	names := make([]string, 0, len(_bindata))
	for name := range _bindata {
		names = append(names, name)
	}
	return names
}

// _bindata is a table, holding each asset generator, mapped to its name.
var _bindata = map[string]func() (*asset, error){
	"000001_add_jobs.down.sql": _000001_add_jobsDownSql,
	"000001_add_jobs.up.sql": _000001_add_jobsUpSql,
	"000002_add_log.down.sql": _000002_add_logDownSql,
	"000002_add_log.up.sql": _000002_add_logUpSql,
	"bindata.go": bindataGo,
}

// AssetDir returns the file names below a certain
// directory embedded in the file by go-bindata.
// For example if you run go-bindata on data/... and data contains the
// following hierarchy:
//     data/
//       foo.txt
//       img/
//         a.png
//         b.png
// then AssetDir("data") would return []string{"foo.txt", "img"}
// AssetDir("data/img") would return []string{"a.png", "b.png"}
// AssetDir("foo.txt") and AssetDir("notexist") would return an error
// AssetDir("") will return []string{"data"}.
func AssetDir(name string) ([]string, error) {
	node := _bintree
	if len(name) != 0 {
		cannonicalName := strings.Replace(name, "\\", "/", -1)
		pathList := strings.Split(cannonicalName, "/")
		for _, p := range pathList {
			node = node.Children[p]
			if node == nil {
				return nil, fmt.Errorf("Asset %s not found", name)
			}
		}
	}
	if node.Func != nil {
		return nil, fmt.Errorf("Asset %s not found", name)
	}
	rv := make([]string, 0, len(node.Children))
	for childName := range node.Children {
		rv = append(rv, childName)
	}
	return rv, nil
}

type bintree struct {
	Func     func() (*asset, error)
	Children map[string]*bintree
}
var _bintree = &bintree{nil, map[string]*bintree{
	"000001_add_jobs.down.sql": &bintree{_000001_add_jobsDownSql, map[string]*bintree{}},
	"000001_add_jobs.up.sql": &bintree{_000001_add_jobsUpSql, map[string]*bintree{}},
	"000002_add_log.down.sql": &bintree{_000002_add_logDownSql, map[string]*bintree{}},
	"000002_add_log.up.sql": &bintree{_000002_add_logUpSql, map[string]*bintree{}},
	"bindata.go": &bintree{bindataGo, map[string]*bintree{}},
}}

// RestoreAsset restores an asset under the given directory
func RestoreAsset(dir, name string) error {
	data, err := Asset(name)
	if err != nil {
		return err
	}
	info, err := AssetInfo(name)
	if err != nil {
		return err
	}
	err = os.MkdirAll(_filePath(dir, filepath.Dir(name)), os.FileMode(0755))
	if err != nil {
		return err
	}
	err = ioutil.WriteFile(_filePath(dir, name), data, info.Mode())
	if err != nil {
		return err
	}
	err = os.Chtimes(_filePath(dir, name), info.ModTime(), info.ModTime())
	if err != nil {
		return err
	}
	return nil
}

// RestoreAssets restores an asset under the given directory recursively
func RestoreAssets(dir, name string) error {
	children, err := AssetDir(name)
	// File
	if err != nil {
		return RestoreAsset(dir, name)
	}
	// Dir
	for _, child := range children {
		err = RestoreAssets(dir, filepath.Join(name, child))
		if err != nil {
			return err
		}
	}
	return nil
}

func _filePath(dir, name string) string {
	cannonicalName := strings.Replace(name, "\\", "/", -1)
	return filepath.Join(append([]string{dir}, strings.Split(cannonicalName, "/")...)...)
}

