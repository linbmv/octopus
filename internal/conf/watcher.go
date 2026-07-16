package conf

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

type Watcher struct {
	path        string
	fsWatcher   *fsnotify.Watcher
	done        chan struct{}
	closeOnce   sync.Once
	wg          sync.WaitGroup
	mu          sync.RWMutex
	subscribers map[chan Config]struct{}
}

func Watch(path string) (*Watcher, error) {
	if path == "" {
		path = LoadedPath()
	}
	if path == "" {
		return nil, fmt.Errorf("config file path is empty")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create config watcher: %w", err)
	}
	w := &Watcher{
		path:        absPath,
		fsWatcher:   fsWatcher,
		done:        make(chan struct{}),
		subscribers: make(map[chan Config]struct{}),
	}
	if err := fsWatcher.Add(filepath.Dir(absPath)); err != nil {
		_ = fsWatcher.Close()
		return nil, fmt.Errorf("watch config directory: %w", err)
	}
	w.wg.Add(1)
	go w.run()
	return w, nil
}

func (w *Watcher) Subscribe() <-chan Config {
	ch := make(chan Config, 1)
	w.mu.Lock()
	w.subscribers[ch] = struct{}{}
	w.mu.Unlock()
	return ch
}

func (w *Watcher) Close() error {
	var err error
	w.closeOnce.Do(func() {
		close(w.done)
		err = w.fsWatcher.Close()
		w.wg.Wait()
		w.mu.Lock()
		for ch := range w.subscribers {
			close(ch)
		}
		w.subscribers = nil
		w.mu.Unlock()
	})
	return err
}

func (w *Watcher) run() {
	defer w.wg.Done()
	var timer *time.Timer
	var timerCh <-chan time.Time
	for {
		select {
		case <-w.done:
			if timer != nil {
				timer.Stop()
			}
			return
		case event, ok := <-w.fsWatcher.Events:
			if !ok {
				return
			}
			if !w.isReloadEvent(event) {
				continue
			}
			timer = resetReloadTimer(timer)
			timerCh = timer.C
		case <-timerCh:
			timerCh = nil
			w.reload()
		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return
			}
			log.Warnf("config watcher error: %v", err)
		}
	}
}

func (w *Watcher) isReloadEvent(event fsnotify.Event) bool {
	return sameFile(event.Name, w.path) && event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0
}

func resetReloadTimer(timer *time.Timer) *time.Timer {
	if timer == nil {
		return time.NewTimer(100 * time.Millisecond)
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(100 * time.Millisecond)
	return timer
}

func (w *Watcher) reload() {
	config, err := loadConfigFile(w.path)
	if err == nil {
		err = Set(config)
	}
	if err != nil {
		log.Warnf("config reload rejected: %v", err)
		return
	}
	w.notify(config)
}

func (w *Watcher) notify(config Config) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for ch := range w.subscribers {
		select {
		case ch <- config:
		default:
			select {
			case <-ch:
			default:
			}
			ch <- config
		}
	}
}

func loadConfigFile(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.AutomaticEnv()
	v.SetEnvPrefix(APP_NAME)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	setDefaultsFor(v)
	if err := v.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("read config file: %w", err)
	}
	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return Config{}, fmt.Errorf("decode config file: %w", err)
	}
	if err := Validate(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func sameFile(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	return leftErr == nil && rightErr == nil && leftAbs == rightAbs
}
