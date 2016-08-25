// Copyright 2016 zxfonline@sina.com. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package fsnotify

import (
	"errors"
	"fmt"
	"os"

	"sync/atomic"

	originalFsm "github.com/fsnotify/fsnotify"
	"github.com/zxfonline/config"
	"github.com/zxfonline/golog"
)

var (
	fsm       = NewFileSytemMonitor(nil)
	loadstate int32
)

//默认的文件监控器
func DefaultFsm() *FileSystemMonitor {
	return fsm
}

//递归删除指定文件或目录、子目录的监控
func Del(path string) {
	fsm.Del(path)
}

//监控指定路径的文件或者目录并注册事件
func Add(path string, action func(FEvent) error) {
	fsm.Add(path, action)
}

//更新监控信息
func loadMonitor(event FEvent) error {
	configurl := event.Name
	//读取初始化配置文件
	cfg, err := config.ReadDefault(configurl)
	if err != nil {
		return fmt.Errorf("加载文件监控列表[%s]错误,error=%v", configurl, err)
	}
	//解析系统环境变量
	section := config.DEFAULT_SECTION
	if options, _ := cfg.SectionOptions(section); options != nil {
		for _, option := range options {
			//on=true 表示开启监控，off表示不监控
			if on, err := cfg.Bool(section, option); err != nil {
				golog.Errorf("FSM TABLE 节点解析错误:section=%s,option=%s,error=%v", section, option, err)
			} else if on {
				fsm.Add(option, nil)
			} else {
				fsm.Del(option)
			}
		}
	}
	log.Debugln("LOAD FSM Monitor List OK")
	return nil
}

//开启监控
func Start() {
	if !atomic.CompareAndSwapInt32(&loadstate, 0, 1) {
		return
	}
	configurl := os.Getenv("fsm_monitor")
	if configurl == "" {
		panic(errors.New(`没找到系统变量:"fsm_monitor"`))
	}
	configurl = TransPath(configurl)
	fsm.Start(loadMonitor, configurl)
	err := loadMonitor(FEvent{originalFsm.Event{Name: configurl, Op: originalFsm.Write}})
	if err != nil {
		panic(err)
	}
}

//监控是否关闭
func Closed() bool {
	return fsm.Closed()
}

//监控关闭
func Close() {
	fsm.Close()
}
