// Copyright (c) 2013 ActiveState Software Inc. All rights reserved.

package watch

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/masahide/tail/util"
	"gopkg.in/fsnotify.v0"
	"gopkg.in/tomb.v1"
)

const (
	headerSize    = 4 * 1024
	logrotateTime = 24*time.Hour - 5*time.Minute
)

// InotifyFileWatcher uses inotify to monitor file changes.
type InotifyFileWatcher struct {
	Filename string
	Size     int64
	w        *fsnotify.Watcher
	ModTime  time.Time
}

func NewInotifyFileWatcher(filename string, w *fsnotify.Watcher) *InotifyFileWatcher {
	fw := &InotifyFileWatcher{filename, 0, w, time.Time{}}
	return fw
}

func (fw *InotifyFileWatcher) BlockUntilExists(t *tomb.Tomb) error {
	dirname := filepath.Dir(fw.Filename)

	// Watch for new files to be created in the parent directory.
	err := fw.w.WatchFlags(dirname, fsnotify.FSN_CREATE)
	if err != nil {
		return err
	}
	defer fw.w.RemoveWatch(dirname)

	// Do a real check now as the file might have been created before
	// calling `WatchFlags` above.
	if _, err = os.Stat(fw.Filename); !os.IsNotExist(err) {
		// file exists, or stat returned an error.
		return err
	}

	for {
		select {
		case evt, ok := <-fw.w.Event:
			if !ok {
				return fmt.Errorf("inotify watcher has been closed")
			}
			evtName, err := filepath.Abs(evt.Name)
			if err != nil {
				return err
			}
			fwFilename, err := filepath.Abs(fw.Filename)
			if err != nil {
				return err
			}
			if evtName == fwFilename {
				return nil
			}
		case <-t.Dying():
			return tomb.ErrDying
		}
	}
	panic("unreachable")
}

func (fw *InotifyFileWatcher) ChangeEvents(t *tomb.Tomb, fi os.FileInfo) *FileChanges {
	changes := NewFileChanges()

	err := fw.w.Watch(fw.Filename)
	if err != nil {
		util.Fatal("Error watching %v: %v", fw.Filename, err)
	}

	fw.Size = fi.Size()
	fw.ModTime = fi.ModTime()

	go func() {
		defer fw.w.RemoveWatch(fw.Filename)
		defer changes.Close()

		for {
			prevSize := fw.Size

			var evt *fsnotify.FileEvent
			var ok bool

			select {
			case evt, ok = <-fw.w.Event:
				if !ok {
					return
				}
			case <-t.Dying():
				return
			}

			switch {
			case evt.IsDelete():
				fallthrough

			case evt.IsRename():
				changes.NotifyDeleted()
				continue
				//return

			case evt.IsModify():
				fi, err := os.Stat(fw.Filename)
				if err != nil {
					if os.IsNotExist(err) {
						changes.NotifyDeleted()
						return
					}
					// XXX: report this error back to the user
					util.Fatal("Failed to stat file %v: %v", fw.Filename, err)
				}
				fw.Size = fi.Size()

				if prevSize > 0 && prevSize > fw.Size {
					log.Printf("prevSize:%d,fw.Size:%d", prevSize, fw.Size)
					changes.NotifyTruncated()
				} else if prevSize > 0 && prevSize == fw.Size && fw.Size <= headerSize && fi.ModTime().Sub(fw.ModTime) > logrotateTime {
					log.Printf("logrotateTime:%s", logrotateTime)
					// also capture log_header only updates
					changes.NotifyTruncated()
				} else {
					changes.NotifyModified()
				}
				prevSize = fw.Size
			}
		}
	}()

	return changes
}
