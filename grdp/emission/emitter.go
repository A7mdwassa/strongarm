package emission

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
)

const DefaultMaxListeners = 10

var ErrNoneFunction = errors.New("listener must be a function")

type RecoveryListener func(event interface{}, listener interface{}, err error)

type Emitter struct {
	mu           sync.Mutex
	events       map[interface{}][]reflect.Value
	onces        map[interface{}][]reflect.Value
	recoverer    RecoveryListener
	maxListeners int
}

func NewEmitter() *Emitter {
	return &Emitter{
		events:       make(map[interface{}][]reflect.Value),
		onces:        make(map[interface{}][]reflect.Value),
		maxListeners: DefaultMaxListeners,
	}
}

func (e *Emitter) SetMaxListeners(max int) *Emitter {
	e.mu.Lock()
	e.maxListeners = max
	e.mu.Unlock()
	return e
}

func (e *Emitter) RecoverWith(r RecoveryListener) *Emitter {
	e.recoverer = r
	return e
}

func (e *Emitter) On(event, listener interface{}) *Emitter {
	return e.AddListener(event, listener)
}

func (e *Emitter) Once(event, listener interface{}) *Emitter {
	fn := reflect.ValueOf(listener)
	if fn.Kind() != reflect.Func {
		e.recoverOrPanic(event, listener, ErrNoneFunction)
		return e
	}

	e.mu.Lock()
	if e.maxListeners != -1 && len(e.onces[event])+1 > e.maxListeners {
		fmt.Fprintf(os.Stdout, "Warning: event `%v` exceeded max listeners (%d)\n",
			event, e.maxListeners)
	}

	e.onces[event] = append(e.onces[event], fn)
	e.mu.Unlock()

	return e
}

func (e *Emitter) AddListener(event, listener interface{}) *Emitter {
	fn := reflect.ValueOf(listener)
	if fn.Kind() != reflect.Func {
		e.recoverOrPanic(event, listener, ErrNoneFunction)
		return e
	}

	e.mu.Lock()
	if e.maxListeners != -1 && len(e.events[event])+1 > e.maxListeners {
		fmt.Fprintf(os.Stdout, "Warning: event `%v` exceeded max listeners (%d)\n",
			event, e.maxListeners)
	}

	e.events[event] = append(e.events[event], fn)
	e.mu.Unlock()

	return e
}

func (e *Emitter) Off(event, listener interface{}) *Emitter {
	return e.RemoveListener(event, listener)
}

func (e *Emitter) RemoveListener(event, listener interface{}) *Emitter {
	fn := reflect.ValueOf(listener)
	if fn.Kind() != reflect.Func {
		e.recoverOrPanic(event, listener, ErrNoneFunction)
		return e
	}
	ptr := fn.Pointer()

	e.mu.Lock()
	if arr, ok := e.events[event]; ok {
		e.events[event] = filterByPtr(arr, ptr)
	}
	if arr, ok := e.onces[event]; ok {
		e.onces[event] = filterByPtr(arr, ptr)
	}
	e.mu.Unlock()

	return e
}

func filterByPtr(list []reflect.Value, ptr uintptr) []reflect.Value {
	out := make([]reflect.Value, 0, len(list))
	for _, fn := range list {
		if fn.Pointer() != ptr {
			out = append(out, fn)
		}
	}
	return out
}

func (e *Emitter) Emit(event interface{}, args ...interface{}) *Emitter {
	// Copy listeners safely under lock
	e.mu.Lock()
	mainListeners := append([]reflect.Value{}, e.events[event]...)
	onceListeners := append([]reflect.Value{}, e.onces[event]...)
	// Remove only the executed once listeners
	if len(onceListeners) > 0 {
		e.onces[event] = e.onces[event][len(onceListeners):]
	}
	e.mu.Unlock()

	// Call main listeners
	e.callListeners(mainListeners, event, args...)

	// Call once listeners
	e.callListeners(onceListeners, event, args...)

	return e
}

func (e *Emitter) callListeners(list []reflect.Value, event interface{}, args ...interface{}) {
	if len(list) == 0 {
		return
	}

	var wg sync.WaitGroup
	wg.Add(len(list))

	for _, fn := range list {
		go func(fn reflect.Value) {
			defer wg.Done()
			e.safeCall(fn, event, args...)
		}(fn)
	}

	wg.Wait()
}

func (e *Emitter) safeCall(fn reflect.Value, event interface{}, args ...interface{}) {
	defer func() {
		if r := recover(); r != nil {
			if e.recoverer != nil {
				e.recoverer(event, fn.Interface(), fmt.Errorf("%v", r))
			}
		}
	}()

	t := fn.Type()
	n := t.NumIn()

	values := make([]reflect.Value, n)
	for i := 0; i < n; i++ {
		if i < len(args) && args[i] != nil {
			values[i] = reflect.ValueOf(args[i])
		} else {
			values[i] = reflect.Zero(t.In(i))
		}
	}

	fn.Call(values)
}

func (e *Emitter) GetListenerCount(event interface{}) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.events[event]) + len(e.onces[event])
}

func (e *Emitter) recoverOrPanic(event, listener interface{}, err error) {
	if e.recoverer != nil {
		e.recoverer(event, listener, err)
	} else {
		panic(err)
	}
}
