# Goloader 插件适配指南（AI 可读版）

> 适用对象：想在 goloader 之上实现「主程序 + 可热加载插件」的工程或 AI coding agent。
> 适用范围：Go 1.8–1.27（本文行为以 go1.27.3、windows/amd64 与 linux/amd64 实测为准）。
> 本文把「插件」定义为：用 `go tool compile` 编译出的 `.o` 目标文件，由 host 进程在运行时加载、调用、卸载。
> 先读 §0、§5、§7；实现时照 §4 的模板做；排错看 §11。

---

## 0. 30 秒结论（TL;DR）

1. host 侧固定流程：`RegSymbol` →（按需 `RegTypes`）→ `ReadObjs` → `Load` → 从 `CodeModule.Syms` 取地址调用 → `Unload`。
2. 插件必须用**与 host 完全相同的 Go 工具链、GOOS、GOARCH** 编译，并且 `go tool compile -p <pkgpath>` 与加载时的 `<pkgpath>` 一致。
3. 插件引用的每个外部函数/类型都必须已经存在于 host 二进制。host 没有链接进来的 stdlib 符号，会在 `Load` 时报 `unresolve external:<name>`。
4. **`CodeModule.Syms[name]` 是代码地址（text address），不是 Go func 值**。直接 `*(*func())(unsafe.Pointer(&addr))` 会崩溃；必须构造 `funcval{code}`（§5）。
5. goloader 不是沙箱：加载的对象拥有宿主进程的全部权限。只加载可信对象。

---

## 1. 心智模型

### 1.1 两个阶段

| 阶段 | 输入 | 产物 | 工具 |
|---|---|---|---|
| 编译期 | 插件 `.go` 源码 + host 的 importcfg | `plugin.o`（Go object，含导出数据/符号） | `go list`、`go tool compile` |
| 运行期 | `plugin.o` + host 符号表 | 可调用的 `CodeModule` | `goloader.ReadObjs`、`Load`、`Unload` |

### 1.2 运行期流水线

```text
host 可执行文件
   │  RegSymbol(symPtr)         扫描 host 的符号表（pclntab/typelinks/itablinks）
   │  RegTypes(symPtr, ...)     把 host 侧具体类型/接口类型登记到 symPtr
   ▼
ReadObjs(files, pkgPaths)       解析 .o，建立 Linker（此时还没有可执行内存）
   ▼
Load(linker, symPtr)            mmap 代码/数据段 → 重定位 → 注册 moduledata → 执行 init
   ▼
CodeModule.Syms[name] = 代码地址
   ▼
构造 funcval 并调用
   ▼
Unload()                        munmap 段、注销模块；此后一切地址失效
```

关键事实：

- `Load` 会执行插件的包级 `init`（以及依赖包的 init），每次 `Load` 都执行一次。
- 同一个 `Linker` 可以 `Load`/`Unload` 多次；每次 `Load` 得到独立 `CodeModule`。
- `Linker` 只是解析结果；真正分配内存和写入运行时的是 `Load`。

---

## 2. 术语表

| 术语 | 含义 |
|---|---|
| host | 主程序，提供 Go runtime、GC、stdio、网络等，也提供插件可调用的符号 |
| plugin object | `go tool compile` 产出的 `.o`（也支持 `.a` 归档）；不是 `plugin` 包的 `.so` |
| importcfg | 文本文件，把 import path 映射到导出数据文件；`go tool compile` 必需 |
| pkgpath | 编译时 `-p` 指定的包路径；决定符号前缀，如 `demo/sig.Add` |
| symbol | 对象文件/二进制中的符号名，如 `main.main`、`demo/sig.Add`、`demo/sig.(*T).M` |
| linker | `goloader.ReadObjs` 的返回值；已解析、未加载 |
| CodeModule | `goloader.Load` 的返回值；已可执行，含 `Syms` |
| funcval | Go 函数值的运行时表示：`struct{ fn uintptr; ... }`，函数值本身指向它 |
| RegSymbol | 把 host 二进制的符号表灌进 `map[string]uintptr` |
| RegTypes | 把 host 侧类型（含接口/结构体/func 签名）登记进 `symPtr` |

---

## 3. 代码地图（定位用）

| 路径 | 作用 | 关键入口 |
|---|---|---|
| `link.go` | 对外 API 包 | `RegSymbol`、`RegTypes`、`ReadObjs`、`Load`、`Unload`、`UnresolvedSymbols`、`Serialize`/`UnSerialize` |
| `link/register.go` | 扫描 host 可执行文件、linkname、typelinks、itablinks | `RegSymbol` |
| `link/type.go` | 类型注册（递归展开指针/函数/结构体/map） | `RegTypes` |
| `link/readobj.go` | 读取对象、解析包/符号 | `ReadObjs`、`ReadDependPackages` |
| `link/ld.go` | 内存布局、重定位、写入 moduledata | `Load`、`Unload`、`UnresolvedSymbols` |
| `link/serialize.go` | 把 Linker 序列化/反序列化（供 builder/runner 预打包） | `Serialize`、`UnSerialize` |
| `obj/`、`objabi/` | Go object 解析与版本差异 | 一般不需要改 |
| `examples/loader/loader.go` | 参考 host：参数解析、RegTypes、多次 Load | 可直接照抄结构 |
| `examples/inter/` + `examples/basecontext/` | 多包插件（主包依赖另一个对象包） | 跨包符号解析示例 |
| `.github/wintest/` | Windows 深度测试脚手架（host、hostkit、deep、features） | 真实可用的复杂调用模式 |
| `.github/workflows/go.yml` | Go 1.8–1.27 矩阵构建与示例运行 | 版本兼容基线 |

---

## 4. 最小可用流程

### 4.1 前置：给 GOROOT 补 `cmd/objfile`

goloader 的 `link` 包 import 了 `cmd/objfile/...`，标准 GOROOT 只有 `src/cmd/internal`：

```bash
cp -r "$(go env GOROOT)/src/cmd/internal" "$(go env GOROOT)/src/cmd/objfile"
```

- GOROOT 只读时（如 `/usr/lib/go`），复制一份可写 GOROOT，或者用自己解压的 Go。
- 每个新 Go 版本都要重新补一次。
- 这一步只需要在**构建 host** 时做，插件编译不需要。

### 4.2 host 模板（可直接编译）

`go.mod`：

```go
module myhost

go 1.21

require github.com/cxykevin/goloader v0.0.0

replace github.com/cxykevin/goloader => /path/to/goloader
```

`main.go`：

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"github.com/cxykevin/goloader"
)

// funcValue 是 Go 函数值指向的结构；调用插件函数时必须先构造它。
type funcValue struct{ code uintptr }

// mustFunc 把 CodeModule.Syms 里的代码地址变成可调用的 Go 函数。
// 签名必须与插件里的真实签名完全一致，否则会崩溃而不是返回错误。
func mustFunc[F any](m *goloader.CodeModule, name string) F {
	addr := m.Syms[name]
	if addr == 0 {
		panic("goloader: missing symbol " + name)
	}
	fv := new(funcValue)
	fv.code = addr
	return *(*F)(unsafe.Pointer(&fv))
}

type objSpecs []string

func (o *objSpecs) String() string { return "file:pkgpath" }
func (o *objSpecs) Set(v string) error {
	*o = append(*o, v)
	return nil
}

// splitObjSpec 解析 file:pkgpath。
// Windows 盘符（C:\dir\plugin.o）里的冒号不是分隔符。
func splitObjSpec(spec string) (file, pkgPath string) {
	n := strings.LastIndex(spec, ":")
	if n < 0 {
		return spec, "main" // goloader 对空 pkgpath 的默认值是 main
	}
	if n == 1 && len(spec) > 2 && (spec[2] == '\\' || spec[2] == '/') {
		return spec, "main"
	}
	return spec[:n], spec[n+1:]
}

func main() {
	var specs objSpecs
	flag.Var(&specs, "o", "plugin object, file:pkgpath")
	flag.Parse()

	if len(specs) == 0 {
		flag.Usage()
		return
	}

	var files, pkgs []string
	for _, s := range specs {
		f, p := splitObjSpec(s)
		files = append(files, f)
		pkgs = append(pkgs, p)
	}

	symPtr := make(map[string]uintptr)
	if err := goloader.RegSymbol(symPtr); err != nil {
		fmt.Fprintln(os.Stderr, "RegSymbol:", err)
		os.Exit(1)
	}

	// 插件会用到、但 host 里可能没有被引用的类型/接口，必须在这里登记。
	// 传 nil 接口会 panic：一定要给真实实例。
	goloader.RegTypes(symPtr,
		fmt.Println, fmt.Sprintf, fmt.Errorf,
		&strings.Builder{}, context.Background(),
	)

	linker, err := goloader.ReadObjs(files, pkgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ReadObjs:", err)
		os.Exit(1)
	}
	// 诊断用途；最终能否加载以 Load 的返回值为准。
	if missing := goloader.UnresolvedSymbols(linker, symPtr); len(missing) > 0 {
		fmt.Fprintln(os.Stderr, "warning, unresolved:", strings.Join(missing, ", "))
	}

	mod, err := goloader.Load(linker, symPtr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Load:", err)
		os.Exit(1)
	}
	defer mod.Unload()

	run := mustFunc[func()](mod, "main.main")
	run()
}
```

要点：

- `ReadObjs(files, pkgPaths)` 要求两个切片**等长**；第 i 个 pkgpath 对应第 i 个文件。
- `RegSymbol` 扫描的是 `os.Executable()`，所以 host 必须是磁盘上的可执行文件；`go run`/被 `-s -w` 裁剪过的二进制不可用。
- `RegTypes` 可重复调用；登记过的类型会递归展开（指针/函数签名/结构体字段/map key+value）。

### 4.3 编译插件

```bash
cd /path/to/plugin            # 插件源码所在包目录
go list -export -deps \
  -f '{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}' . > importcfg
go tool compile -p example.com/app/plugin -importcfg importcfg -o plugin.o plugin.go
```

- **必须显式 `-p`**：不传时编译器把符号归到 `<unlinkable>`，加载后找不到 `pkg.Func`。
- 「main 包」可以写成 `-p main`，并加载 `:main`（或省略 `:pkgpath`）。
- `importcfg` 生成命令要在**包目录**里执行（`.` 是当前包）。
- Windows PowerShell 里 `>` 会写 UTF-16，`go tool compile` 读不了；用：
  ```powershell
  (& go list -export -deps -f '{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}' .) |
      Set-Content -Encoding ascii importcfg
  ```
  或改用 `cmd /c "go list ... > importcfg"`。

### 4.4 运行

```bash
./myhost -o ./plugin.o:example.com/app/plugin
```

- `-o` 可重复，用于多包。
- 省略 `:pkgpath` 等价于 `:main`。
- `-s -w`、`go run`、`go test` 会让符号表缺失，一律不要用在 host 上（README 的 Warning）。

---

## 5. 从 `CodeModule.Syms` 调用函数（最容易错的地方）

### 5.1 事实

- `CodeModule.Syms[name]` 里存的是**函数代码地址**（`sym.Offset + codeBase`），可用 `runtime.FuncForPC(addr)` 验证会返回对应函数名。
- Go 的函数值不是代码地址，而是指向 `funcval{fn uintptr, ...}` 的指针。
- 因此下面这种「直接转换」是错误的，调用时会在 `CALL [AX]` 处读到机器码并崩溃：

```go
// 反例：不要这样写
addr := mod.Syms["demo/sig.Add"]
f := *(*func(int, int) int)(unsafe.Pointer(&addr)) // addr 是代码地址，不是 funcval 指针
f(1, 2)                                             // SIGSEGV
```

### 5.2 正确写法（已实测）

```go
type funcValue struct{ code uintptr }

func mustFunc[F any](m *goloader.CodeModule, name string) F {
	addr := m.Syms[name]
	if addr == 0 {
		panic("goloader: missing symbol " + name)
	}
	fv := new(funcValue)          // 堆上分配，保证调用期间有效
	fv.code = addr
	return *(*F)(unsafe.Pointer(&fv))
}
```

调用示例：

```go
noArgs := mustFunc[func()](mod, "main.main")
noArgs()

add := mustFunc[func(int, int) int](mod, "demo/sig.Add")
sum := add(2, 3) // 5

greet := mustFunc[func(string) (string, error)](mod, "demo/sig.Greet")
msg, err := greet("bob")
```

规则：

- 泛型参数 `F` 必须是**和插件完全一致**的函数类型；参数/返回值不一致不会有编译期检查，直接崩溃。
- 方法符号（如 `demo/foo.(*T).M`）在对象里就是「receiver 作为第一个参数」的函数，按 `func(*T, ...)` 声明 `F` 即可；方法值（`x.M`，带闭包上下文）不在此列。
- `funcValue` 的 `code` 是 `uintptr`（不含指针），GC 不会追踪；函数值本身持有对它的指针，调用期间不会被回收。
- 仓库自带的 `examples/loader` 用了一个等效但更绕的写法（把栈上 `uintptr` 变量的地址当 funcval），可以工作，但不要改写成「把地址值直接当函数值」。

---

## 6. 插件侧约定与 ABI 设计

### 6.1 符号命名

| 插件代码 | 符号名 |
|---|---|
| `package foo` + `func Run()`，编译 `-p demo/foo` | `demo/foo.Run` |
| `func main()`，编译 `-p main` | `main.main` |
| `func (t *T) M()` | `demo/foo.(*T).M` |

- **首字母大写**便于约定「导出」；goloader 本身按符号名查找，不检查是否导出。
- 包路径建议使用稳定、唯一的前缀（如 `example.com/app/plugin`），避免与 host 里的包冲突。

### 6.2 推荐 ABI

| 方式 | 适用 | 注意 |
|---|---|---|
| 插件导出 `func Run([]byte) ([]byte, error)` | 最稳，只依赖内建类型 | host 用 `mustFunc` 调用；错误用 `error` 接口 |
| 插件导出多个小函数（`Add`、`Greet`） | 简单 RPC 风格 | 每个函数单独 `mustFunc` |
| 插件通过 `//go:linkname` 调 host 服务 | 插件需要回调 host（反向调用） | 已在 `.github/wintest/deep/deep.go` 使用；host 侧函数必须在二进制里可达 |
| 插件 import 一个 host 已链接的公共包 | 共享类型/接口 | 该包必须存在于 host 二进制，且插件用同一份导出数据编译 |

不建议：把插件自定义的 struct 按值传给 host 再让 host 命名它——host 编译期根本不知道这个类型。可以传 `interface{}` / `[]byte` / 基础类型，或让 host 只通过接口（需 `RegTypes` 注册该接口）操作。

### 6.3 init 与全局状态

- 插件包的 `init` 在 `Load` 时执行；依赖包的 init 也执行。
- 每次 `Load` 都是新的模块实例：全局变量、init 副作用会重新来一遍。
- 不要在 init 里启动无法停止的常驻 goroutine；`Unload` 不会替你回收它们。

---

## 7. 符号与类型注册规则（R1–R10）

### R1：host 与插件必须同一工具链

Go 的对象格式、ABI、runtime 结构体随版本变化。host 与插件必须由**同一个 `go` 二进制**编译（同 minor/patch、同 GOOS/GOARCH、同 GOEXPERIMENT）。否则表现为：加载失败、GC 崩溃、`runtime: bad pointer in frame`。

### R2：`-p` 与 `:pkgpath` 必须一致

- 编译：`go tool compile -p demo/sig ...`
- 加载：`-o plugin.o:demo/sig`
- 省略 `:pkgpath` 时 goloader 用 `main`，只与 `-p main` 匹配。

### R3：插件引用的符号必须在 host 里

host 二进制只保留「可达」代码。插件用到而 host 未引用的 stdlib 函数会被链接器裁掉，`Load` 报：

```text
unresolve external:<symbol>
```

已实测需要额外保留的例子：

| 缺失符号 | 触发写法 | host 侧保留办法 |
|---|---|---|
| `runtime.chanrecv2` | `v, ok := <-ch` | host 里出现一次 comma-ok receive |
| `runtime.deferproc` | 循环里的 `defer` | host 里放一个循环 defer 的函数 |
| `fmt.Sprint` | `fmt.Sprint(x)` | 放进全局 `[]any{...}` 或 `RegTypes` |
| `type:context.Context` | 参数/字段用了 `context.Context` | `RegTypes(symPtr, context.Background())` |

通用保留技巧：

```go
// 1) 全局 slice：链接器不会裁剪被全局变量引用的函数值
var keep = []any{fmt.Sprint, strings.ToUpper, json.Marshal}

// 2) RegTypes：同时登记类型与函数签名
goloader.RegTypes(symPtr, fmt.Println, &sync.WaitGroup{}, context.Background())

// 3) 真实调用一次
```

### R4：类型/接口要注册

- 插件对象自带的类型在模块内可用；但 host 侧接口、`type:` 符号需要 host 注册。
- `RegTypes` 的参数必须是**非 nil 的具体值**；传 nil 接口会 panic（`unexpected invalid kind during register type!`）。
- 注册是递归的：`*T`、`func(A) B`、`struct{F T}`、`map[K]V` 都会展开。

### R5：host 不能 strip

不要用 `-s -w` 构建 host，也不要用 `go run` / `go test` 的临时二进制（默认 strip）。`RegSymbol` 依赖可执行文件的符号表。

### R6：`Unload` 之后一切失效

`Unload` 会释放模块的代码段/数据段并注销 moduledata。之后：

- 任何 `Syms` 地址都不可调用；
- 模块全局变量、类型指针、闭包都不再有效；
- 仍在运行的模块 goroutine 会访问已释放内存。

调用方必须在卸载前排空该模块的调用和 goroutine（引用计数 / `sync.RWMutex` / `WaitGroup`）。

### R7：init 在 `Load` 时执行

`Load` 内部调用 `doInitialize`，执行各包 init。`-times N` 会执行 N 次 init。

### R8：多包依赖要一起 `ReadObjs`

```go
linker, err := goloader.ReadObjs(
	[]string{"util.o", "main.o"},
	[]string{"demo/util", "main"},
)
```

- `ReadObjs` 先读完所有对象再统一 `resolveSymbols`，所以文件顺序不要求依赖在前。
- 编译 `main.o` 时，`demo/util` 的导出数据可以来自 build cache（`go list -export -deps` 自动包含），也可以手动追加：
  ```text
  packagefile demo/util=/abs/path/util.o
  ```

### R9：没有沙箱

加载的代码与 host 同权限：可以读写文件、发网络请求、`os.Exit`。插件系统必须自己做来源校验/签名/白名单。

### R10：`Load`/`Unload` 不是并发安全的

对同一个 `Linker` 的 `Load`/`Unload`、以及卸载与调用之间，都要由宿主加锁串行化。

---

## 8. 多包插件完整命令

目录：

```text
plugin/
  main.go          // package main; import "demo/util"
  util/util.go     // package util
  go.mod           // module demo
```

```bash
cd plugin/util
go list -export -deps -f '{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}' . > ../importcfg.util
go tool compile -p demo/util -importcfg ../importcfg.util -o ../util.o util.go

cd ..
go list -export -deps -f '{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}' . > importcfg.main
go tool compile -p main -importcfg importcfg.main -o main.o main.go

./myhost -o ./util.o:demo/util -o ./main.o:main
```

- 编译 `main.go` 时 `demo/util` 的导出数据来自主机 build cache，与 `util.o` 内容同源即可；加载时以 `util.o` 提供符号。
- 用不同编译 flags 生成的对象，只要导出数据与符号集合一致，编译可以通过；但仍建议 host 与插件使用同一套命令，避免内联/符号差异带来的运行时问题。

---

## 9. 版本与平台约束

| 项目 | 约束 |
|---|---|
| Go 版本 | host 与插件一致；本仓库支持 1.8–1.27（见 README） |
| GOOS/GOARCH | 一致；`read obj error: Arch X != Arch Y` 表示混入了别的架构对象 |
| 依赖内部包 | 构建 host 前必须准备 `cmd/objfile` |
| cgo | 有 `RegSymbolWithPath`（builder 可达性检查）与 `CgoImportMap`；插件侧 cgo 仍属高级/受限用法，优先避免 |
| Windows | 对象路径含盘符，`-o` 解析要跳过 `C:\` 的冒号（见 `splitObjSpec`） |
| 符号表 | host 不能 `-s -w`；对象不能被 strip |
| 平台支持 | linux/darwin/windows x64/x86；linux/android arm、arm64；darwin arm64（README 矩阵） |

---

## 10. 打包、缓存与热更新

### 10.1 预打包 Linker

`goloader.Serialize(linker, w)` / `goloader.UnSerialize(r)` 可以把解析结果序列化，避免每次启动重新解析 `.o`。仓库 README 提到的 `builder`/`runner` 属于外部工具（goloaderbuilder），不在本仓库内。

### 10.2 动态补依赖

`ReadDependPackages(linker, files, pkgPaths, symbolNames, symPtr)` 可在**首次 `Load` 之前**追加依赖对象并补符号。`Load` 之后调用会返回：

```text
already adapted symbol offset, don't add new symbols
```

### 10.3 热更新协议（推荐）

```text
1. 编译新 plugin.o → ReadObjs → Load 新 CodeModule
2. 原子切换「当前模块」指针（atomic.Pointer / RWMutex）
3. 等待旧模块在途调用结束（引用计数或 WaitGroup）
4. 旧模块 Unload
```

- 切换前必须保证新旧 ABI 兼容（同一签名、同一语义）。
- 失败时保留旧模块，不要先 Unload。

---

## 11. 排错表

| 现象 | 原因 | 处理 |
|---|---|---|
| `unresolve external:X` | X 没被链接进 host，或被裁剪 | 用 R3 的技巧保留；或把 X 所在包真正 import 进 host |
| `unresolve external:type:X` | 类型未注册 | `RegTypes(symPtr, 具体实例)` |
| `read error: ...` / 解析失败 | 不是 Go object、版本不符、对象损坏 | 用同一工具链重编；确认传的是 `.o` |
| `read obj error: Arch a != Arch b` | 混入不同 GOARCH 的对象 | 清掉旧对象，全部重编 |
| `Load error! not find function: main.main`（示例 loader 文案） | 符号名/包路径不对 | 检查 `-p` 与 `:pkgpath`；打印 `mod.Syms` 的 key |
| `already adapted symbol offset, ...` | `Load` 之后又调 `ReadDependPackages` | 移到首次 `Load` 前 |
| `unexpected invalid kind during register type!` | `RegTypes` 传了 nil 接口 | 传非 nil 具体值 |
| 调用时 SIGSEGV / `unexpected fault address 0xffff...` | 直接把 `Syms` 代码地址当 func 值 | 用 §5 的 `mustFunc` |
| 调用时参数/返回值错乱 | `mustFunc[F]` 的 `F` 与插件签名不一致 | 对齐签名（含 receiver） |
| GC 期间崩溃 / `bad pointer in frame` | host 与插件工具链不一致，或 host 被 strip | R1、R5 |
| Windows 下 pkgpath 变成 `\x.o` | `-o` 冒号解析把盘符当分隔符 | 用 §4.2 的 `splitObjSpec` |
| PowerShell 生成的 importcfg 编译报错 | `>` 写成 UTF-16 | `Set-Content -Encoding ascii` 或 `cmd /c` |
| `Load` 成功但行为异常 | 插件依赖的 host 全局状态/注册表未初始化 | 在 `Load` 前初始化 host 服务 |

---

## 12. 验证清单（AI 可直接执行）

按顺序做，全部通过才算适配完成：

1. **native 对照**：同一份插件源码 `go build` 成原生程序运行；与通过 goloader 加载的输出逐字节比较（`Get-FileHash` / `sha256sum`），必须相同。
2. **重复加载**：同一 `Linker` 连续 `Load`/`Unload` 3 次，输出与单次一致，且 init 每次都执行。
3. **GC 压力**：`GOGC=1`，`GODEBUG=clobberfree=1,gccheckmark=1,invalidptr=1` 下再跑一次。
4. **跨包**：两个对象（`util.o` + `main.o`）一次 `ReadObjs`，跨包符号与 init 正常。
5. **并发/生命周期**：模块 goroutine 全部退出后再 `Unload`，卸载后不再触碰旧地址。
6. **复杂覆盖**（建议）：goroutine/channel/select、defer/recover、reflect、泛型、JSON、regexp、big.Int、finalizer、深递归（栈增长）、大 slice/map。

参考实现：`.github/wintest/run-deep.ps1` 覆盖了 host↔module 边界、GC、pprof、finalizer、多模块；`examples/loader` 是最小闭环。

---

## 13. 参考索引

| 主题 | 位置 |
|---|---|
| 最小 host 流程 | `examples/loader/loader.go`（`main` 中的 RegSymbol/RegTypes/ReadObjs/Load/Unload） |
| 多包与跨包类型 | `examples/inter/inter.go`、`examples/basecontext/basecontext.go` |
| host 服务回调（linkname） | `.github/wintest/deep/deep.go`、`.github/wintest/host/hostkit/hostkit.go` |
| 符号扫描 | `link/register.go` 的 `RegSymbol` |
| 类型注册 | `link/type.go` 的 `RegTypes`/`registerType` |
| 对象读取 | `link/readobj.go` 的 `ReadObjs`、`ReadDependPackages` |
| 加载/卸载/未解析诊断 | `link/ld.go` 的 `Load`、`Unload`、`UnresolvedSymbols` |
| Linker 序列化 | `link/serialize.go` |
| 版本兼容基线 | `.github/workflows/go.yml` 的 1.8–1.27 矩阵 |

---

## 14. 给 AI 的任务模板

接到「给这个项目做插件适配」任务时，按以下顺序执行，不要跳步：

1. **确认工具链**：`go version`、`go env GOOS GOARCH`；确认 host 与插件将用同一个 `go`。
2. **准备 GOROOT**：确认 `$(go env GOROOT)/src/cmd/objfile` 存在，否则复制 `cmd/internal`。
3. **实现 host**：照 §4.2 写 `RegSymbol` → `RegTypes` → `ReadObjs` → `Load` → `mustFunc` → `Unload`；加 `-o file:pkgpath` 解析。
4. **实现插件**：导出稳定签名的函数（优先 `[]byte`/`error`），包路径固定。
5. **编译**：`go list -export -deps` 生成 importcfg，`go tool compile -p <pkgpath>` 生成 `.o`。
6. **先跑 native 对照**，再跑加载版；不一致时按 §11 表定位。
7. **加验证**：重复加载、GC 压力、多包、卸载后不访问。
8. **记录约束**：把缺失符号、注册类型、ABI 版本写进项目文档/记忆。
