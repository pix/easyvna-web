//go:build js && wasm

package webserial

import (
	"context"
	"errors"
	"syscall/js"
	"time"

	"github.com/pix/easyvna-web/internal/jsutil"
)

// Open asks the page's Web Serial adapter for the NanoVNA port and returns
// it as a Conn plus a display name. adapter is the page object with
//
//	open(onData(Uint8Array), onEnd(message)) -> Promise<{name, write(Uint8Array) -> Promise, close() -> Promise}>
//
// (EasyVNAHost.serial in web/browser/host.js). The page chooses the port:
// the one picked with requestPort() on a click, else a previously granted one.
func Open(ctx context.Context, adapter js.Value) (*Conn, string, error) {
	var h js.Value // the page's handle, set once open resolves
	write := func(b []byte) error {
		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := jsutil.Await(wctx, h.Call("write", jsutil.Uint8Array(b)))
		return err
	}
	var onData, onEnd js.Func
	release := func() { onData.Release(); onEnd.Release() }
	closeFn := func() error {
		cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err := jsutil.Await(cctx, h.Call("close"))
		// Keep the callbacks alive briefly: the reader loop may still report its end.
		time.AfterFunc(time.Second, release)
		return err
	}
	c := NewConn(write, closeFn)
	onData = js.FuncOf(func(this js.Value, args []js.Value) any {
		c.Push(jsutil.Bytes(args[0]))
		return nil
	})
	onEnd = js.FuncOf(func(this js.Value, args []js.Value) any {
		var err error
		if len(args) > 0 && args[0].Type() == js.TypeString && args[0].String() != "" {
			err = errors.New(args[0].String())
		}
		c.End(err)
		return nil
	})
	v, err := jsutil.Await(ctx, adapter.Call("open", onData, onEnd))
	if err != nil {
		release()
		return nil, "", err
	}
	h = v
	name := "Web Serial"
	if n := h.Get("name"); n.Type() == js.TypeString {
		name = n.String()
	}
	return c, name, nil
}
