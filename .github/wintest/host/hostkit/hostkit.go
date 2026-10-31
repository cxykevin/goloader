package hostkit

import (
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

// HostType is a concrete type owned by the host binary. Loaded code must be
// able to convert it to host interfaces, reflect on it and call its methods.
type HostType struct {
	N int
	S string
}

func (h *HostType) String() string { return fmt.Sprintf("HostType(%d,%s)", h.N, h.S) }

var (
	mu      sync.Mutex
	reports []string

	payload            interface{} = &HostType{42, "abc"}
	hostStringerItabV  fmt.Stringer
	hostPayloadReflect = reflect.TypeOf(&HostType{})
)

func Report(name string, v interface{}) {
	mu.Lock()
	reports = append(reports, fmt.Sprintf("%s=%v", name, v))
	mu.Unlock()
}

func Reports() []string {
	mu.Lock()
	defer mu.Unlock()
	out := make([]string, len(reports))
	copy(out, reports)
	return out
}

func WaitReport(prefix string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		runtime.GC()
		mu.Lock()
		for _, r := range reports {
			if len(r) >= len(prefix) && r[:len(prefix)] == prefix {
				mu.Unlock()
				return true
			}
		}
		mu.Unlock()
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func Payload() interface{}     { return payload }
func SetPayload(v interface{}) { payload = v }

func PanicValue(msg string) { panic(msg) }

func Call(f func(int) int, n int) int { return f(n) }

func CallRecover(f func()) (r interface{}) {
	defer func() { r = recover() }()
	f()
	return nil
}

var goWG sync.WaitGroup

func Go(f func()) {
	goWG.Add(1)
	go func() {
		defer goWG.Done()
		f()
	}()
}

func Wait() { goWG.Wait() }

func StringerItab(v fmt.Stringer) uintptr {
	return (*[2]uintptr)(unsafe.Pointer(&v))[0]
}

func HostStringerItab() uintptr {
	var v fmt.Stringer = &HostType{7, "x"}
	return (*[2]uintptr)(unsafe.Pointer(&v))[0]
}

func HostTypeReflect() reflect.Type { return hostPayloadReflect }

// AcceptStringer receives an interface value produced by loaded code and
// calls its method from host code.
func AcceptStringer(v fmt.Stringer) string { return "got:" + v.String() }

// PCName resolves a program counter to a function name from host code.
func PCName(pc uintptr) string {
	f := runtime.FuncForPC(pc)
	if f == nil {
		return "<nil>"
	}
	file, line := f.FileLine(pc)
	return fmt.Sprintf("%s %s:%d", f.Name(), file, line)
}

// PCNameCallers resolves the caller PC captured by the caller.
func PCNameCallers(skip int) string {
	pc, _, _, ok := runtime.Caller(skip + 1)
	if !ok {
		return "<nocaller>"
	}
	return PCName(pc)
}

var KeepFns = []interface{}{
	Report, Reports, WaitReport, Payload, SetPayload, PanicValue, Call,
	CallRecover, Go, Wait, StringerItab, HostStringerItab, HostTypeReflect,
	PCName, PCNameCallers, AcceptStringer,
	(*HostType).String,
}

func Keep() {
	runtime.KeepAlive(KeepFns)
	runtime.KeepAlive(hostStringerItabV)
	runtime.KeepAlive(hostPayloadReflect)
}
