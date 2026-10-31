package deepmod

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "unsafe"
)

// Host entry points, resolved through the host binary's symbol table.
//
//go:linkname hostPayload hostkit.Payload
func hostPayload() interface{}

//go:linkname hostSetPayload hostkit.SetPayload
func hostSetPayload(v interface{})

//go:linkname hostReport hostkit.Report
func hostReport(name string, v interface{})

//go:linkname hostPanicValue hostkit.PanicValue
func hostPanicValue(msg string)

//go:linkname hostCall hostkit.Call
func hostCall(f func(int) int, n int) int

//go:linkname hostCallRecover hostkit.CallRecover
func hostCallRecover(f func()) interface{}

//go:linkname hostGo hostkit.Go
func hostGo(f func())

//go:linkname hostWait hostkit.Wait
func hostWait()

//go:linkname hostStringerItab hostkit.StringerItab
func hostStringerItab(v fmt.Stringer) uintptr

//go:linkname hostHostStringerItab hostkit.HostStringerItab
func hostHostStringerItab() uintptr

//go:linkname hostTypeReflect hostkit.HostTypeReflect
func hostTypeReflect() reflect.Type

//go:linkname hostWaitReport hostkit.WaitReport
func hostWaitReport(prefix string, d time.Duration) bool

//go:linkname hostPCName hostkit.PCName
func hostPCName(pc uintptr) string

//go:linkname hostPCNameCallers hostkit.PCNameCallers
func hostPCNameCallers(skip int) string

//go:linkname hostAcceptStringer hostkit.AcceptStringer
func hostAcceptStringer(v fmt.Stringer) string

var checks int

func check(cond bool, format string, args ...interface{}) {
	checks++
	if !cond {
		panic(fmt.Sprintf("DEEP CHECK FAILED: "+format, args...))
	}
}

type Rect struct{ W, H float64 }

func (r *Rect) Area() float64 { return r.W * r.H }
func (r *Rect) Name() string  { return "rect" }

type S struct{ N int }

func (s S) String() string { return "S:" + strconv.Itoa(s.N) }

// localStringer is declared by loaded code and implemented by a host type.
type localStringer interface{ String() string }

type node struct {
	v    int
	s    string
	next *node
	pad  [4]uintptr
}

type finalizable struct{ tag string }

func buildChain(depth int) *node {
	if depth == 0 {
		return nil
	}
	n := &node{v: depth, s: strconv.Itoa(depth), next: buildChain(depth - 1)}
	return n
}

func walkChain() {
	head := buildChain(40000)
	runtime.GC()
	runtime.GC()
	n, want := head, 40000
	for ; n != nil; n, want = n.next, want-1 {
		check(n.v == want && n.s == strconv.Itoa(want), "stack growth chain %d", want)
	}
	check(want == 0, "chain walked to end")
	runtime.KeepAlive(head)
}

func callersTest() []string {
	var pcs [8]uintptr
	n := runtime.Callers(0, pcs[:])
	names := make([]string, 0, n)
	for _, pc := range pcs[:n] {
		names = append(names, hostPCName(pc-1))
	}
	return names
}

func namedHelper() string {
	pc, file, line, ok := runtime.Caller(0)
	check(ok && strings.HasSuffix(file, "deep.go") && line > 0, "runtime.Caller")
	fn := runtime.FuncForPC(pc)
	check(fn != nil, "FuncForPC")
	return fn.Name()
}

func deferLoop(n int) (c int) {
	for i := 0; i < n; i++ {
		func() {
			defer func() { c++ }()
			if i%1000 == 0 {
				_ = i
			}
		}()
	}
	return
}

func crossBoundary() {
	// host value -> host interface
	p := hostPayload()
	fs, ok := p.(fmt.Stringer)
	check(ok && fs.String() == "HostType(42,abc)", "host value as fmt.Stringer: %v", p)
	check(hostStringerItab(fs) == hostHostStringerItab(), "itab identity for host type + host interface")

	// host value -> loaded interface
	ls, ok := p.(localStringer)
	check(ok && strings.HasPrefix(ls.String(), "HostType("), "host value as loaded interface")

	// reflect type identity across the module boundary
	check(reflect.TypeOf(p) == hostTypeReflect(), "reflect.Type identity: %v", reflect.TypeOf(p))
	check(reflect.TypeOf(p).String() == "*hostkit.HostType", "reflect type name")

	// host calls back into loaded code
	check(hostCall(func(x int) int { return x*x + 1 }, 7) == 50, "host -> module callback")

	// loaded value passed to a host interface parameter
	check(hostAcceptStringer(S{9}) == "got:S:9", "module value as host interface")

	// loaded code recovers a panic raised in the host
	var rec interface{}
	func() {
		defer func() { rec = recover() }()
		hostPanicValue("host boom")
	}()
	check(rec == "host boom", "recover host panic: %v", rec)

	// host recovers a panic raised in loaded code
	r := hostCallRecover(func() { panic("module boom") })
	check(r == "module boom", "host recovers module panic: %v", r)

	// module goroutine running on a host-spawned goroutine
	var n int64
	hostGo(func() { atomic.AddInt64(&n, 1) })
	hostWait()
	check(atomic.LoadInt64(&n) == 1, "host goroutine runs module closure")
}

func gcAndFinalizer() {
	// heap objects of a loaded type, kept alive by loaded code
	var keep []*node
	for i := 0; i < 500; i++ {
		keep = append(keep, &node{v: i, s: strconv.Itoa(i)})
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].v > keep[j].v })
	runtime.GC()
	for i := 0; i < 500; i++ {
		check(keep[i].v == 499-i && keep[i].s == strconv.Itoa(499-i), "module heap data after GC")
	}

	// finalizer declared and run by loaded code
	func() {
		obj := &finalizable{tag: "fin-tag"}
		runtime.SetFinalizer(obj, func(o *finalizable) { hostReport("finalizer", o.tag) })
		obj = nil
	}()
	check(hostWaitReport("finalizer=fin-tag", 10*time.Second), "finalizer ran")
}

func concurrency() {
	var wg sync.WaitGroup
	var total int64
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for k := 0; k < 300; k++ {
				v := &node{v: g*1000 + k, s: strconv.Itoa(g * k)}
				if v.s != strconv.Itoa(g*k) {
					panic("corrupted module heap object")
				}
				atomic.AddInt64(&total, int64(len(v.s)))
				if k%100 == 0 {
					runtime.GC()
				}
			}
		}(g)
	}
	wg.Wait()
	check(total > 0, "concurrent allocation total")
}

func misc() {
	// method expression and interface switch with loaded types
	f := (*Rect).Area
	check(f(&Rect{3, 4}) == 12, "method expression")

	// loaded type implementating host fmt.Stringer, formatted by host fmt
	check(fmt.Sprintf("%v", S{5}) == "S:5", "fmt uses module Stringer")
	check(fmt.Sprint(S{6}) == "S:6", "fmt.Sprint module Stringer")

	// interface-keyed map mixing host and loaded values
	m := map[interface{}]int{}
	m[S{1}] = 1
	m[&Rect{1, 1}] = 2
	m["x"] = 3
	if _, ok := m[S{1}]; !ok {
		panic("interface key lookup")
	}
	check(len(m) == 3, "interface key map len")

	// reflect mutation of loaded memory
	rv := reflect.ValueOf(&Rect{}).Elem()
	rv.Field(0).SetFloat(2.5)
	rv.Field(1).SetFloat(4)
	check(rv.Addr().Interface().(*Rect).Area() == 10, "reflect set on module memory")

	// errors and defer ordering
	sentinel := errors.New("sentinel")
	wrapped := fmt.Errorf("w: %w", sentinel)
	check(errors.Is(wrapped, sentinel), "errors.Is in module")
	check(deferLoop(20000) == 20000, "20k open-coded defers")

	// runtime.Callers must not elide loaded frames
	names := callersTest()
	joined := strings.Join(names[:4], " ")
	check(strings.Contains(joined, "deepmod.callersTest"), "runtime.Callers loaded frame: %v", names)
	check(strings.Contains(joined, "deepmod.misc"), "runtime.Callers caller frame: %v", names)

	// runtime introspects loaded frames
	name := namedHelper()
	check(strings.Contains(name, "deepmod."), "FuncForPC name %q", name)
	stack := string(debug.Stack())
	check(strings.Contains(stack, "deepmod."), "debug.Stack contains module frames")

	// host-side symbolization of loaded-module PCs
	burnPC := reflect.ValueOf(burnCPU).Pointer()
	hostReport("pcname", hostPCName(burnPC))
	hostReport("pcname.callers", hostPCNameCallers(0))
}

func burnCPU() {
	deadline := time.Now().Add(150 * time.Millisecond)
	x := 0
	for time.Now().Before(deadline) {
		for i := 0; i < 2000; i++ {
			x += i * i
		}
	}
	hostReport("burn", x > 0)
}

// Quick does a small amount of work, for load/unload stress runs.
func Quick() {
	p := hostPayload()
	fs, ok := p.(fmt.Stringer)
	if !ok || fs.String() != "HostType(42,abc)" {
		panic("quick: host interface conversion failed")
	}
	f := (*Rect).Area
	if f(&Rect{2, 3}) != 6 || fmt.Sprint(S{1}) != "S:1" {
		panic("quick: method dispatch failed")
	}
	_ = buildChain(50)
	runtime.GC()
}

// PanicBoom panics after traversing module and host frames; the host harness
// recovers it to check unwinding from loaded code into host code.
func PanicBoom() {
	defer func() {
		if r := recover(); r != nil {
			panic(fmt.Sprintf("wrapped(%v)", r))
		}
	}()
	hostCall(func(x int) int {
		if x == 1 {
			panic("inner boom")
		}
		return x
	}, 1)
}

func Run() {
	crossBoundary()
	gcAndFinalizer()
	concurrency()
	misc()
	walkChain()
	burnCPU()
	fmt.Printf("deep: %d checks passed\n", checks)
}
