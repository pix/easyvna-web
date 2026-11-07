//go:build windows

package serialport

import (
	"golang.org/x/sys/windows/registry"
)

// List returns the USB serial ports present right now (registry lookup).
func List() []Candidate { return ListWindows(hklm{}) }

type hklm struct{}

func (hklm) SubKeys(path string) ([]string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	return k.ReadSubKeyNames(-1)
}

func (hklm) String(path, name string) (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	return v, err
}

func (hklm) Values(path string) (map[string]string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	names, err := k.ReadValueNames(-1)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, n := range names {
		if v, _, err := k.GetStringValue(n); err == nil {
			out[n] = v
		}
	}
	return out, nil
}
