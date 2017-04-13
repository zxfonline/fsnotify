// Copyright 2016 zxfonline@sina.com. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fsnotify

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zxfonline/golog"

	"sync/atomic"

	originalFsm "github.com/fsnotify/fsnotify"
	"github.com/zxfonline/chanutil"
	"github.com/zxfonline/fileutil"
	. "github.com/zxfonline/trace"
	"golang.org/x/net/trace"
)

var log *golog.Logger = golog.New("FileMonitor")

/**文件监控器
* 对指定目录下的所有文件的修改进行监控
* 注意：监控文件夹内部不要做文件夹的手动改名或移动操作，否则无法监控改名后或移动后的文件夹
 */
type FileSystemMonitor struct {
	files    map[string]*LessFileState //监控的所有文件
	w        *originalFsm.Watcher
	stopD    chanutil.DoneChan
	watchDir map[string]bool               //正在监控的文件夹
	actions  map[string]func(FEvent) error //监控的路径事件
	//default ignore all file start with "."
	ignoreFilter func(path string) bool
	//文件更改监控更新间隔时间，该时间内的更改不处理
	WaitTime   time.Duration
	startstate int32
}

type FEvent struct {
	originalFsm.Event
	tr trace.Trace
}

func (fe *FEvent) TraceFinish() {
	if fe.tr != nil {
		fe.tr.Finish()
		fe.tr = nil
	}
}

func (fe *FEvent) TracePrintf(format string, a ...interface{}) {
	if fe.tr != nil {
		fe.tr.LazyPrintf(format, a...)
	}
}

func (fe *FEvent) TraceErrorf(format string, a ...interface{}) {
	if fe.tr != nil {
		fe.tr.LazyPrintf(format, a...)
		fe.tr.SetError()
	}
}

type LessFileState struct {
	Time time.Time
}

func (p *FileSystemMonitor) Close() {
	if !atomic.CompareAndSwapInt32(&p.startstate, 1, 2) {
		return
	}
	p.stopD.SetDone()
	p.w.Close()
}

func (p *FileSystemMonitor) Closed() bool {
	return p.stopD.R().Done()
}

func (p *FileSystemMonitor) Start(action func(FEvent) error, paths ...string) error {
	if !atomic.CompareAndSwapInt32(&p.startstate, 0, 1) {
		return errors.New("fsm closed")
	}
	go p.start(action, paths)
	return nil
}

func (p *FileSystemMonitor) start(action func(FEvent) error, paths []string) {
	for _, path := range paths {
		p.Add(path, action)
	}
	p.doMonitor()
}

//递归删除指定路径的监控文件或者目录、子目录
func (p *FileSystemMonitor) Del(path string) {
	if atomic.LoadInt32(&p.startstate) != 1 {
		log.Warnf("Add Watch Error:fms no closed or not opening,Path:%v", path)
		return
	}
	p.deletePath(path)
}

//监控指定路径的文件或者目录 当action=nil时，不替换原有的事件
func (p *FileSystemMonitor) Add(path string, action func(FEvent) error) {
	if atomic.LoadInt32(&p.startstate) != 1 {
		log.Warnf("Add Watch Error:fms no closed or not opening,Path:%v", path)
		return
	}
	path = fileutil.TransPath(path)
	if fi, err := os.Stat(path); err != nil {
		log.Warnf("Add Watch Error:%v,Path:%v", err, path)
	} else if fi == nil {
		return
	} else if fi.IsDir() {
		p.adddir(path, action)
	} else if p.ignoreFilter(path) {
		log.Debugf("monitor ignore file,path:%v", path)
	} else {
		if _, ok := p.files[path]; !ok {
			if err := p.w.Add(path); err != nil {
				log.Warnf("Add Watch Error:%v,Path:%v", err, path)
				return
			}
		}
		p.files[path] = &LessFileState{Time: time.Now()}
		if action != nil {
			p.actions[path] = action
			log.Infof("Replace Watch:%v", path)
		} else {
			log.Infof("Add Watch:%v,no action", path)
		}
	}
}

func (p *FileSystemMonitor) adddir(dir string, action func(FEvent) error) {
	if e := filepath.Walk(dir, func(path string, f os.FileInfo, err error) error {
		if f == nil {
			return nil
		}
		path = fileutil.TransPath(path)
		if f.IsDir() {
			if p.ignoreFilter(path) {
				log.Debugf("monitor ignore file,path:%v", path)
				return nil
			}
			if v, ok := p.watchDir[path]; !ok || !v {
				if err := p.w.Add(path); err != nil {
					log.Warnf("Add Watch Error:%v,Path:%v", err, path)
					return nil
				}
				p.watchDir[path] = true
			}
			if action != nil {
				p.actions[path] = action
				log.Infof("Replace Watch:%v", path)
			} else {
				log.Infof("Add Watch:%v,no action", path)
			}
		} else {
			p.files[path] = &LessFileState{Time: time.Now()}
			if action != nil {
				p.actions[path] = action
			}
			log.Debugf("Found File:%v", path)
		}
		return nil
	}); e != nil {
		log.Warnf("Add Watch Error:%v,Path:%v", e, dir)
	}
}

// .bashrc true
// a.txt false
func DefaultIgnoreFilter(path string) bool {
	if path == "./" {
		return false
	}
	path = fileutil.TransPath(path)
	if strings.HasPrefix(filepath.Base(path), ".") {
		return true
	}
	return false
}

func (p *FileSystemMonitor) doMonitor() {
	for q := false; !q; {
		select {
		case v := <-p.w.Events:
			path := v.Name
			path = fileutil.TransPath(path)
			fi, err := os.Stat(path)
			if err == nil && fi != nil {
				if fi.IsDir() { //文件夹存在
					if v.Op&originalFsm.Create == originalFsm.Create || v.Op&originalFsm.Rename == originalFsm.Rename {
						ppath := strings.Replace(filepath.Dir(path), "\\", "/", -1)
						if action, ok := p.actions[ppath]; ok && action != nil {
							p.adddir(path, action)
						} else {
							log.Warnf("Add Watch Error:parent path no action event,File:%v", path)
						}
					}
				} else {
					if v.Op&originalFsm.Create == originalFsm.Create || v.Op&originalFsm.Rename == originalFsm.Rename {
						if _, ok := p.files[path]; !ok {
							ppath := strings.Replace(filepath.Dir(path), "\\", "/", -1)
							if action, ok := p.actions[ppath]; ok && action != nil {
								p.files[path] = &LessFileState{Time: time.Now()}
								p.actions[path] = action
								log.Debugf("Found File:%v", path)
							} else {
								log.Infof("Found File Error:parent path no action event,File:%v", path)
							}
						}
					} else if v.Op&originalFsm.Write == originalFsm.Write {
						if fs, ok := p.files[path]; ok {
							if fs.Time.Before(time.Now()) {
								fs.Time = time.Now().Add(p.WaitTime)
								ev := FEvent{originalFsm.Event{Name: path, Op: v.Op}, nil}
								if action, ok := p.actions[path]; ok && action != nil {
									if err := doAction(action, ev); err != nil {
										log.Warnf("write event error:%v,path:%v", err, path)
									} else {
										log.Debugf("write event:%v", ev.String())
									}
								} else {
									log.Debugf("write event no action:%v", ev.String())
								}
							}
						}
					}
				}
			} else if v.Op&originalFsm.Remove == originalFsm.Remove || v.Op&originalFsm.Rename == originalFsm.Rename { //文件或文件夹不存在
				p.deletePath(path)
			}
		case err := <-p.w.Errors:
			log.Warnf("monitor error:%v", err)
		case <-p.stopD:
			q = true
		}
	}
}

func doAction(action func(FEvent) error, ev FEvent) (err error) {
	if EnableTracing {
		ev.tr = trace.New("fsm", ev.Name)
	}
	defer func() {
		if e := recover(); e != nil {
			switch e.(type) {
			case error:
				err = e.(error)
			default:
				err = fmt.Errorf("%v", e)
			}
			ev.TraceErrorf("fsm err:%v", err)
		}
		ev.TraceFinish()
	}()
	err = action(ev)
	return
}

//将路径转成监控文件统一使用的路径格式
//func TransPath(path string) string {
//	path = filepath.Clean(path)
//	// 不用绝对路径
//	//	if !filepath.IsAbs(path) {
//	//		if abs, err := filepath.Abs(path); err == nil {
//	//			path = abs
//	//		}
//	//	}
//	path = strings.Replace(path, "\\", "/", -1)
//	return path
//}

func (p *FileSystemMonitor) deletePath(path string) {
	path = fileutil.TransPath(path)
	if v, ok := p.watchDir[path]; ok && v {
		p.watchDir[path] = false
		if err := p.w.Remove(path); err != nil {
			log.Warnf("delete dir:%v,err:%v", path, err)
		} else {
			log.Infof("delete dir:%v", path)
		}
		for sp, _ := range p.files { //删除子目录监控
			if strings.HasPrefix(sp, path) {
				delete(p.files, sp)
				sup := strings.Replace(filepath.Dir(sp), "\\", "/", -1)
				if v, ok := p.watchDir[sup]; ok && v {
					p.watchDir[sup] = false
					if err := p.w.Remove(sup); err != nil {
						log.Warnf("delete dir:%v,err:%v", sup, err)
					} else {
						log.Infof("delete dir:%v", sup)
					}
				}
			}
		}
	} else if _, ok := p.files[path]; ok {
		delete(p.files, path)
		if err := p.w.Remove(path); err != nil {
			log.Warnf("delete file:%v,err:%v", path, err)
		} else {
			log.Infof("delete file:%v", path)
		}
	}
}

//监控文件夹内部不要做文件夹的手动改名或移动操作，否则无法监控改名后或移动后的文件夹
func NewFileSytemMonitor(ignoreFilter func(path string) bool) *FileSystemMonitor {
	if ignoreFilter == nil {
		ignoreFilter = DefaultIgnoreFilter
	}
	w, err := originalFsm.NewWatcher()
	if err != nil {
		panic(err)
	}
	return &FileSystemMonitor{
		watchDir:     make(map[string]bool),
		actions:      make(map[string]func(FEvent) error),
		files:        make(map[string]*LessFileState),
		w:            w,
		stopD:        chanutil.NewDoneChan(),
		ignoreFilter: ignoreFilter,
		WaitTime:     time.Duration(0.2 * float64(time.Second)),
	}
}
