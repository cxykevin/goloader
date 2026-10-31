package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"hostkit"

	"github.com/pkujhd/goloader"
)

type arrayFlags struct {
	File    []string
	PkgPath []string
}

func (i *arrayFlags) String() string { return "files" }

func (i *arrayFlags) Set(value string) error {
	file, path := value, ""
	// A Windows object path starts with a drive letter, so only a colon
	// that is not the drive separator can start the package path.
	if n := strings.LastIndex(value, ":"); n >= 0 && !(n == 1 && (len(value) == 2 || value[2] == '\\' || value[2] == '/')) {
		file, path = value[:n], value[n+1:]
	}
	i.File = append(i.File, file)
	i.PkgPath = append(i.PkgPath, path)
	return nil
}

var (
	printfRef  = fmt.Printf
	printlnRef = fmt.Println
)

type keepStruct struct{ A, B int }

func (k *keepStruct) Sum() int { return k.A + k.B }

var keepFuncs = []interface{}{
	strings.Contains, strings.HasPrefix, strings.TrimSuffix, strings.ToUpper,
	strings.Repeat, (*strings.Builder).WriteString, (*strings.Builder).String,
	time.After, errors.Is, errors.New, sort.Ints, sort.Slice,
	strconv.Itoa, strconv.Atoi, fmt.Errorf, fmt.Sprintf, fmt.Sprint,
	(*sync.WaitGroup).Add, (*sync.WaitGroup).Done, (*sync.WaitGroup).Wait,
	(*sync.Mutex).Lock, (*sync.Mutex).Unlock, (*sync.Once).Do,
	reflect.TypeOf, reflect.ValueOf, runtime.GC,
	runtime.SetFinalizer, runtime.Caller, runtime.Callers, runtime.FuncForPC, runtime.KeepAlive,
	debug.Stack, atomic.AddInt64, atomic.LoadInt64,
	time.Now, time.Since, time.Sleep,
}

// keepSymbols makes sure that entry points used by loaded objects are linked
// into this host binary (the Go linker otherwise drops unreferenced code).
func keepSymbols() {
	runtime.KeepAlive(keepFuncs)
	hostkit.Keep()

	printfRef("%s", "")
	printlnRef()
	vals := []int{3, 1, 2}
	sort.Ints(vals)
	_ = strings.Repeat("x", 2)

	ch := make(chan int, 1)
	ch <- 1
	close(ch)
	for range ch {
	}
	select {
	case <-ch:
	default:
	}

	rt := reflect.TypeOf(keepStruct{})
	if m, ok := rt.MethodByName("Sum"); ok {
		_ = m.Func
	}
	rv := reflect.ValueOf(&keepStruct{1, 2}).MethodByName("Sum")
	if rv.IsValid() {
		_ = rv.Call(nil)
	}
	_ = reflect.TypeOf(&keepStruct{}).Elem().NumField()
	_ = reflect.ValueOf(keepStruct{}).Field(0).Int()
	_ = reflect.TypeOf(map[keepStruct]string{}).Key().Name()
	sv := reflect.ValueOf(&keepStruct{}).Elem().Field(0)
	sv.SetInt(1)

	_ = time.After(0)
	_ = time.Now()
	_ = time.Since(time.Now())
	time.Sleep(0)
	_ = errors.Is(fmt.Errorf("wrap: %w", errors.New("e")), errors.New("e"))
	_ = strconv.Itoa(1)
	_, _ = strconv.Atoi("1")
	var sb strings.Builder
	sb.WriteString("x")
	_ = sb.String()
	var wg sync.WaitGroup
	wg.Add(1)
	go wg.Done()
	wg.Wait()

	var cbuf [4]uintptr
	_ = runtime.Callers(0, cbuf[:])
	if pc, _, _, ok := runtime.Caller(0); ok {
		_ = runtime.FuncForPC(pc)
	}
	_ = debug.Stack()
	var ai int64
	atomic.AddInt64(&ai, 1)
	_ = atomic.LoadInt64(&ai)
	runtime.GC()
}

func rssKB() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return -1
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return -1
	}
	pages, _ := strconv.ParseInt(f[1], 10, 64)
	return pages * int64(os.Getpagesize()) / 1024
}

func callSym(cm *goloader.CodeModule, name string, recoverPanic bool) (panicked interface{}) {
	ptr := cm.Syms[name]
	if ptr == 0 {
		fmt.Println("Load error! not find function:", name)
		return "missing symbol"
	}
	container := (uintptr)(unsafe.Pointer(&ptr))
	fn := *(*func())(unsafe.Pointer(&container))
	if recoverPanic {
		defer func() { panicked = recover() }()
	}
	fn()
	os.Stdout.Sync()
	return nil
}

func main() {
	var files arrayFlags
	flag.Var(&files, "o", "load go object file")
	var parseFile = flag.String("parse", "", "parse go object file")
	var run = flag.String("run", "main.main", "run function")
	var run2 = flag.String("run2", "", "second function to run")
	var times = flag.Int("times", 1, "run count")
	var stress = flag.Int("stress", 0, "load/unload stress iterations")
	var dumpMissing = flag.Bool("missing", false, "print unresolved symbols and exit")
	var profileOut = flag.String("profile", "", "write CPU profile here")
	var profileSym = flag.String("profile-sym", "", "symbol expected in the CPU profile")
	var recoverPanic = flag.Bool("recover", false, "recover panics from the loaded function")

	flag.Parse()
	keepSymbols()

	if *parseFile != "" {
		obj, err := goloader.Parse(*parseFile, "")
		fmt.Printf("%v\n", obj)
		if err != nil {
			fmt.Printf("error reading %s: %v\n", *parseFile, err)
		}
		return
	}

	if len(files.File) == 0 {
		flag.PrintDefaults()
		return
	}

	symPtr := make(map[string]uintptr)
	if err := goloader.RegSymbol(symPtr); err != nil {
		fmt.Println(err)
		return
	}
	w := sync.WaitGroup{}
	str := make([]string, 0)
	goloader.RegTypes(symPtr, http.ListenAndServe, http.Dir("/"),
		http.Handler(http.FileServer(http.Dir("/"))), http.FileServer, http.HandleFunc,
		&http.Request{}, &http.Server{}, (&http.ServeMux{}).Handle)
	goloader.RegTypes(symPtr, runtime.LockOSThread, &w, w.Wait)
	goloader.RegTypes(symPtr, fmt.Sprint, str)

	linker, err := goloader.ReadObjs(files.File, files.PkgPath)
	if err != nil {
		fmt.Println(err)
		return
	}

	if *dumpMissing {
		missing := goloader.UnresolvedSymbols(linker, symPtr)
		fmt.Printf("missing %d symbols\n%s\n", len(missing), strings.Join(missing, "\n"))
		return
	}

	if *stress > 0 {
		start := rssKB()
		step := *stress / 4
		if step == 0 {
			step = 1
		}
		for i := 0; i < *stress; i++ {
			cm, err := goloader.Load(linker, symPtr)
			if err != nil {
				fmt.Println("Load error:", err)
				return
			}
			if p := callSym(cm, *run, false); p != nil {
				fmt.Println("run panicked:", p)
				return
			}
			cm.Unload()
			runtime.GC()
			if i%step == 0 || i == *stress-1 {
				fmt.Printf("stress %d/%d rss=%dKB\n", i+1, *stress, rssKB())
			}
		}
		end := rssKB()
		fmt.Printf("stress done: start=%dKB end=%dKB delta=%dKB\n", start, end, end-start)
		return
	}

	var profFile *os.File
	if *profileOut != "" {
		profFile, err = os.Create(*profileOut)
		if err != nil {
			fmt.Println("profile:", err)
			return
		}
		if err := pprof.StartCPUProfile(profFile); err != nil {
			fmt.Println("profile:", err)
			return
		}
	}

	var mmapByte []byte
	for i := 0; i < *times; i++ {
		codeModule, err := goloader.Load(linker, symPtr)
		if err != nil {
			fmt.Println("Load error:", err)
			return
		}
		if p := callSym(codeModule, *run, *recoverPanic); p != nil {
			fmt.Printf("recovered: %v\n", p)
		}
		if *run2 != "" {
			if p := callSym(codeModule, *run2, *recoverPanic); p != nil {
				fmt.Printf("recovered %s: %v\n", *run2, p)
			}
		}
		if profFile != nil {
			// Symbolize while the module is still mapped.
			pprof.StopCPUProfile()
			profFile.Close()
			profFile = nil
		}
		codeModule.Unload()
		if mmapByte == nil {
			mmapByte, _ = goloader.Mmap(4096)
			copy(mmapByte, make([]byte, 4096))
		} else {
			goloader.Munmap(mmapByte)
			mmapByte = nil
		}
	}

	if *profileOut != "" {
		if profFile != nil {
			pprof.StopCPUProfile()
			profFile.Close()
		}
		raw, err := os.ReadFile(*profileOut)
		if err != nil {
			fmt.Println("profile read:", err)
		} else {
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				fmt.Println("profile gzip:", err)
			} else {
				plain, _ := io.ReadAll(zr)
				found := *profileSym == "" || bytes.Contains(plain, []byte(*profileSym))
				fmt.Printf("profile: %d bytes decompressed, contains %q: %v\n", len(plain), *profileSym, found)
			}
		}
	}

	for _, r := range hostkit.Reports() {
		fmt.Println("report:", r)
	}
}
