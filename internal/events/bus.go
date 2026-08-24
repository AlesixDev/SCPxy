package events

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const ringSize = 512

type Level byte

const (
	Debug Level = iota
	Info
	Warn
	Error
)

var levelNames = map[string]Level{"debug": Debug, "info": Info, "warn": Warn, "error": Error}

func ParseLevel(raw string) (Level, bool) {
	level, ok := levelNames[strings.ToLower(raw)]

	return level, ok
}

func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	}

	return "?????"
}

type Entry struct {
	Time    time.Time
	Level   Level
	Scope   string
	Message string
}

type Bus struct {
	mu   sync.RWMutex
	min  Level
	ring []Entry
	subs map[int]chan Entry
	next int
}

func NewBus(min Level) *Bus {
	return &Bus{
		min:  min,
		ring: make([]Entry, 0, ringSize),
		subs: make(map[int]chan Entry),
	}
}

func (b *Bus) SetLevel(level Level) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.min = level
}

func (b *Bus) Level() Level {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return b.min
}

func (b *Bus) Subscribe() (int, <-chan Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.next
	b.next++
	ch := make(chan Entry, 256)
	b.subs[id] = ch

	return id, ch
}

func (b *Bus) Unsubscribe(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch, ok := b.subs[id]

	if !ok {
		return
	}

	delete(b.subs, id)
	close(ch)
}

func (b *Bus) History() []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]Entry, len(b.ring))
	copy(out, b.ring)

	return out
}

func (b *Bus) Log(level Level, scope, format string, args ...any) {
	b.mu.Lock()

	if level < b.min {
		b.mu.Unlock()
		return
	}

	entry := Entry{
		Time:    time.Now(),
		Level:   level,
		Scope:   scope,
		Message: fmt.Sprintf(format, args...),
	}

	if len(b.ring) == ringSize {
		copy(b.ring, b.ring[1:])
		b.ring[ringSize-1] = entry
	}

	if len(b.ring) < ringSize {
		b.ring = append(b.ring, entry)
	}

	subs := make([]chan Entry, 0, len(b.subs))

	for _, ch := range b.subs {
		subs = append(subs, ch)
	}

	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- entry:
		default:
		}
	}
}

func (b *Bus) Debugf(scope, format string, args ...any) {
	b.Log(Debug, scope, format, args...)
}

func (b *Bus) Infof(scope, format string, args ...any) {
	b.Log(Info, scope, format, args...)
}

func (b *Bus) Warnf(scope, format string, args ...any) {
	b.Log(Warn, scope, format, args...)
}

func (b *Bus) Errorf(scope, format string, args ...any) {
	b.Log(Error, scope, format, args...)
}
