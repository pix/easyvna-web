//go:build js && wasm

// Package jsutil holds the few syscall/js helpers of the browser build.
package jsutil

import (
	"context"
	"errors"
	"syscall/js"
)

// Await waits for a JavaScript promise. It must be called from a goroutine
// that is not running a js.FuncOf callback (that would deadlock).
func Await(ctx context.Context, p js.Value) (js.Value, error) {
	type result struct {
		v   js.Value
		err error
	}
	ch := make(chan result, 1)
	then := js.FuncOf(func(this js.Value, args []js.Value) any {
		ch <- result{v: arg0(args)}
		return nil
	})
	catch := js.FuncOf(func(this js.Value, args []js.Value) any {
		ch <- result{err: Error(arg0(args))}
		return nil
	})
	defer then.Release()
	defer catch.Release()
	p.Call("then", then, catch)
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		return js.Undefined(), ctx.Err()
	}
}

func arg0(args []js.Value) js.Value {
	if len(args) == 0 {
		return js.Undefined()
	}
	return args[0]
}

// Error turns a rejected value (usually an Error object) into a Go error.
func Error(v js.Value) error {
	if v.Type() == js.TypeObject {
		if m := v.Get("message"); m.Type() == js.TypeString && m.String() != "" {
			return errors.New(m.String())
		}
	}
	if v.Type() == js.TypeString {
		return errors.New(v.String())
	}
	return errors.New("JavaScript error")
}

// Bytes copies a Uint8Array into Go.
func Bytes(v js.Value) []byte {
	if v.Type() != js.TypeObject {
		return nil
	}
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b
}

// Uint8Array copies b into a new JavaScript Uint8Array.
func Uint8Array(b []byte) js.Value {
	a := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(a, b)
	return a
}

// Promise returns a JavaScript promise settled by fn, which runs on its
// own goroutine (so it may block, call Await, etc.).
func Promise(fn func() (any, error)) js.Value {
	var exec js.Func
	exec = js.FuncOf(func(this js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			defer exec.Release()
			v, err := fn()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			resolve.Invoke(v)
		}()
		return nil
	})
	return js.Global().Get("Promise").New(exec)
}
