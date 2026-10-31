package main

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

var checks int

func check(cond bool, format string, args ...interface{}) {
	checks++
	if !cond {
		panic(fmt.Sprintf("CHECK FAILED: "+format, args...))
	}
}

type Shape interface {
	Area() float64
	Name() string
}

type Rect struct{ W, H float64 }

func (r *Rect) Area() float64 { return r.W * r.H }
func (r *Rect) Name() string  { return "rect" }

type Circle struct{ R float64 }

func (c Circle) Area() float64 { return 3.14159 * c.R * c.R }
func (c Circle) Name() string  { return "circle" }

type Named interface {
	Shape
	Label() string
}

type LabeledRect struct {
	Rect
	Tag string
}

func (l *LabeledRect) Label() string { return l.Tag }

type Number interface {
	~int | ~int64 | ~float64
}

func Sum[T Number](xs []T) T {
	var s T
	for _, x := range xs {
		s += x
	}
	return s
}

type Stack[T any] struct {
	items []T
}

func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }

func (s *Stack[T]) Pop() (T, bool) {
	var zero T
	if len(s.items) == 0 {
		return zero, false
	}
	v := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return v, true
}

type Point struct{ X, Y int }

func divide(a, b int) (q int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered: %v", r)
		}
	}()
	q = a / b
	return
}

func deferOrder() []int {
	var out []int
	func() {
		defer func() { out = append(out, 3) }()
		defer func() { out = append(out, 2) }()
		defer func() { out = append(out, 1) }()
	}()
	return out
}

var initGlobal = func() int { return 42 }()

func init() {
	fmt.Println("features init")
}

func main() {
	// 1. basic scalar/string ops
	check(initGlobal == 42, "init global")
	check(strings.ToUpper("go1.27") == "GO1.27", "strings")
	check(strings.Repeat("ab", 3) == "ababab", "repeat")
	var sb strings.Builder
	for i := 0; i < 100; i++ {
		sb.WriteString(strconv.Itoa(i))
	}
	check(len(sb.String()) == 190, "builder len %d", len(sb.String()))

	// 2. slices/arrays/maps
	arr := [5]int{5, 4, 3, 2, 1}
	sl := arr[:]
	sort.Ints(sl)
	check(fmt.Sprint(arr) == "[1 2 3 4 5]", "sort.Ints %v", arr)

	var grown []int
	for i := 0; i < 10000; i++ {
		grown = append(grown, i*i)
	}
	check(len(grown) == 10000 && grown[9999] == 9999*9999, "append growth")

	m := map[Point]string{}
	for i := 0; i < 1000; i++ {
		m[Point{i, i * 2}] = strconv.Itoa(i)
	}
	total := 0
	for k, v := range m {
		n, _ := strconv.Atoi(v)
		check(n == k.X && k.Y == k.X*2, "map struct key")
		total++
	}
	check(total == 1000, "map len %d", total)

	// 3. interfaces / type assertions / type switch
	var shapes []Shape = []Shape{&Rect{2, 3}, Circle{1}}
	check(shapes[0].Area() == 6, "rect area")
	check(shapes[0].Name() == "rect", "rect name")
	check(fmt.Sprintf("%.2f", shapes[1].Area()) == "3.14", "circle area %v", shapes[1].Area())

	r, ok := shapes[0].(*Rect)
	check(ok && r.W == 2, "type assertion ok")
	c, ok := shapes[1].(Circle)
	check(ok && c.R == 1, "type assertion value")
	_, ok = shapes[0].(Circle)
	check(!ok, "type assertion negative")

	var lr Named = &LabeledRect{Rect{4, 5}, "tag"}
	check(lr.Label() == "tag" && lr.Area() == 20, "embedded interface + struct")

	describe := func(i interface{}) string {
		switch v := i.(type) {
		case nil:
			return "nil"
		case int:
			return "int:" + strconv.Itoa(v)
		case string:
			return "string:" + v
		case Shape:
			return "shape:" + v.Name()
		case *Rect:
			return "rect"
		default:
			return fmt.Sprintf("other:%T", v)
		}
	}
	check(describe(7) == "int:7", "switch int")
	check(describe("x") == "string:x", "switch string")
	check(describe(shapes[1]) == "shape:circle", "switch shape")
	check(describe(nil) == "nil", "switch nil")

	var empty interface{}
	check(empty == nil, "nil interface")
	empty = []byte("abc")
	check(len(empty.([]byte)) == 3, "empty interface bytes")

	// 4. generics
	check(Sum([]int{1, 2, 3, 4}) == 10, "generic int")
	check(Sum([]int64{1 << 40, 1}) == (1<<40)+1, "generic int64")
	check(Sum([]float64{1.5, 2.5}) == 4, "generic float")

	var st Stack[string]
	for _, s := range []string{"a", "b", "c"} {
		st.Push(s)
	}
	last, ok := st.Pop()
	check(ok && last == "c", "stack pop")
	_, _ = st.Pop()
	_, _ = st.Pop()
	_, ok = st.Pop()
	check(!ok, "stack empty")

	var fs Stack[Shape]
	fs.Push(&Rect{1, 1})
	v, _ := fs.Pop()
	check(v.Area() == 1, "generic interface stack")

	// 5. closures / method values / func values
	counter := 0
	inc := func() int { counter++; return counter }
	inc()
	inc()
	check(counter == 2, "closure")

	rect := &Rect{3, 4}
	areaFn := rect.Area
	methodVal := rect.Name
	check(areaFn() == 12 && methodVal() == "rect", "method value")
	funcs := []func() float64{areaFn, func() float64 { return shapes[1].Area() }}
	check(funcs[0]() == 12, "func slice")

	// 6. defer/recover/named returns
	q, err := divide(10, 2)
	check(err == nil && q == 5, "divide ok")
	_, err = divide(1, 0)
	check(err != nil && strings.Contains(err.Error(), "recovered"), "divide recovered %v", err)

	order := deferOrder()
	check(fmt.Sprint(order) == "[1 2 3]", "defer order %v", order)

	// 7. panic through callbacks in std library
	vals := []float64{3, 1, 2}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	check(fmt.Sprint(vals) == "[1 2 3]", "sort.Slice callback")

	// 8. concurrency, channels, sync
	var wg sync.WaitGroup
	var mu sync.Mutex
	sum := 0
	for i := 1; i <= 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			mu.Lock()
			sum += n
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	check(sum == 1275, "goroutines sum %d", sum)

	ch := make(chan int, 10)
	for i := 0; i < 10; i++ {
		ch <- i
	}
	close(ch)
	chSum := 0
	for x := range ch {
		chSum += x
	}
	check(chSum == 45, "channel sum")

	timeout := make(chan bool)
	select {
	case <-timeout:
		check(false, "unexpected timeout")
	case <-time.After(10 * time.Millisecond):
	}

	var once sync.Once
	n := 0
	for i := 0; i < 10; i++ {
		once.Do(func() { n++ })
	}
	check(n == 1, "sync.Once")

	// 9. errors
	sentinel := errors.New("boom")
	wrapped := fmt.Errorf("wrap: %w", sentinel)
	inner := wrapped.(interface{ Unwrap() error }).Unwrap()
	check(inner == sentinel, "wrapped error identity")
	sw := (*[2]uintptr)(unsafe.Pointer(&sentinel))
	iw := (*[2]uintptr)(unsafe.Pointer(&inner))
	check(sw[0] == iw[0], "interface itab identity across modules")
	check(errors.Is(wrapped, sentinel), "errors.Is")

	// 10. reflection on loaded types
	rt := reflect.TypeOf(Rect{})
	check(rt.Name() == "Rect" && rt.NumField() == 2, "reflect struct %v", rt)
	check(reflect.TypeOf(&Rect{}).Elem().Name() == "Rect", "reflect ptr")
	check(reflect.TypeOf(Point{}).NumField() == 2, "reflect numfield")
	rf := reflect.ValueOf(Point{4, 5}).Field(1)
	check(rf.Int() == 5, "reflect field")
	check(reflect.ValueOf(Point{4, 5}).Field(0).CanSet() == false, "reflect canset")
	check(reflect.TypeOf(shapes[0]).String() == "*main.Rect", "reflect type string %v", reflect.TypeOf(shapes[0]))
	check(reflect.TypeOf(map[Point]string{}).Key().Name() == "Point", "reflect map key")

	// 11. formatting of loaded values (uses type metadata + reflection)
	check(fmt.Sprintf("%v", Point{1, 2}) == "{1 2}", "fmt struct")
	check(fmt.Sprintf("%+v", Point{1, 2}) == "{X:1 Y:2}", "fmt plusv")
	check(strings.Contains(fmt.Sprintf("%T", Point{}), "main.Point"), "fmt %%T")
	check(fmt.Sprintf("%d", 42) == "42", "fmt int")
	check(fmt.Sprintf("%q", "hi") == `"hi"`, "fmt string")

	// 12. GC pressure with loaded types
	for round := 0; round < 5; round++ {
		var keep []*LabeledRect
		for i := 0; i < 2000; i++ {
			keep = append(keep, &LabeledRect{Rect{float64(i), 2}, "x"})
		}
		runtime.GC()
		check(keep[1999].W == 1999 && keep[0].Tag == "x", "gc keep")
		if round%2 == 0 {
			keep = nil
		}
		runtime.GC()
	}
	runtime.GC()

	// 13. unsafe round trip
	x := int64(0x0102030405060708)
	p := unsafe.Pointer(&x)
	b := (*byte)(p)
	check(*b == 0x08, "unsafe byte %x", *b)

	// 14. traceback inside loaded code (panics recovered after deep nesting)
	deep := func(n int) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("deep: %v", r)
			}
		}()
		var rec func(int)
		rec = func(k int) {
			if k == 0 {
				panic("bottom")
			}
			rec(k - 1)
		}
		rec(n)
		return nil
	}
	e := deep(50)
	check(e != nil && strings.Contains(e.Error(), "bottom"), "deep panic")

	fmt.Printf("features: %d checks passed\n", checks)
}
