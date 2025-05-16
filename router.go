// Copyright 2013 Julien Schmidt. All rights reserved.
// 使用本源代码受 BSD 风格许可协议的约束，该协议可在 LICENSE.BASE 文件中找到。
// Copyright 2025 WJQSERVER, WJQSERVER-STUDIO. All rights reserved.
// 使用本源代码受 Apache 2.0许可协议的约束，该协议可在 LICENSE 文件中找到。
// Package httprouter 是一个基于 trie 树的高性能 HTTP 请求路由器。
//
// 一个简单的例子是：
//
//	package main
//
//	import (
//	    "context"
//	    "fmt"
//	    "github.com/julienschmidt/httprouter"
//	    "log"
//	    "net/http"
//	    "time"
//	)
//
//	func Index(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
//	    fmt.Fprint(w, "Welcome!\n")
//	}
//
//	func Hello(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
//	    // 通过 r.Context() 获取上下文
//	    ctx := r.Context()
//	    select {
//	    case <-ctx.Done():
//	        // 客户端断开连接或请求被取消
//	        log.Println("Client disconnected for /hello/:name")
//	        return
//	    default:
//	        // 继续正常处理
//	        fmt.Fprintf(w, "hello, %s! (ctx available)\n", ps.ByName("name"))
//	    }
//	}
//
//	func LongProcess(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
//	    ctx := r.Context() // 获取请求上下文
//	    log.Printf("Starting long process for %s...\n", ps.ByName("task"))
//
//	    select {
//	    case <-time.After(10 * time.Second): // 模拟耗时操作
//	        fmt.Fprintf(w, "Task %s completed!\n", ps.ByName("task"))
//	        log.Printf("Task %s completed normally.\n", ps.ByName("task"))
//	    case <-ctx.Done(): // 监听客户端断开或请求取消
//	        // 如果 ctx.Done() 被关闭，意味着客户端断开连接或服务器取消了请求 (例如超时)
//	        // 这里的 err 会给出原因，例如 context.Canceled 或 context.DeadlineExceeded
//	        err := ctx.Err()
//	        log.Printf("Long process for %s cancelled/client disconnected: %v\n", ps.ByName("task"), err)
//	        // 可以在这里进行一些清理工作
//	        // 注意：此时可能无法再向 ResponseWriter 写入数据，因为连接可能已经关闭
//	    }
//	}
//
//	func AnyHandler(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
//	    fmt.Fprintf(w, "Handled by ANY for path %s, method %s\n", r.URL.Path, r.Method)
//	}
//
//	func main() {
//	    router := httprouter.New()
//	    router.GET("/", Index)
//	    router.GET("/hello/:name", Hello)
//	    router.GET("/long/:task", LongProcess)
//
//	    // 使用 Any 方法
//	    router.ANY("/anypath", AnyHandler)
//	    router.ANY("/anypath/:param", AnyHandler)
//
//	    // 路由组示例
//	    adminGroup := router.Group("/admin")
//	    adminGroup.GET("/dashboard", Index)
//	    adminGroup.ANY("/settings", AnyHandler)
//
//	    log.Println("Server starting on :8080...")
//	    log.Fatal(http.ListenAndServe(":8080", router))
//	}
//
// ... (其余注释保持不变) ...
package httprouter

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// Handle 是一个可以注册到路由以处理 HTTP 请求的函数。
// 类似于 http.HandlerFunc，但有第三个参数用于通配符（路径变量）的值。
// **重要提示**: 可以通过 `r.Context()` 在此函数内部获取 `context.Context`。
// 该上下文可用于感知客户端断开连接 (`<-r.Context().Done()`) 或传递请求范围的值。
type Handle func(http.ResponseWriter, *http.Request, Params)

// Param 是一个单独的 URL 参数，由一个键和一个值组成。
type Param struct {
	Key   string
	Value string
}

// Params 是一个 Param 切片，由路由器返回。
// 切片是有序的，第一个 URL 参数也是第一个切片值。
// 因此通过索引读取值是安全的。
type Params []Param

// ByName 返回键匹配给定名称的第一个 Param 的值。
// 如果没有找到匹配的 Param，则返回空字符串。
func (ps Params) ByName(name string) string {
	for _, p := range ps {
		if p.Key == name {
			return p.Value
		}
	}
	return ""
}

type paramsKey struct{}

// ParamsKey 是 URL 参数存储在请求上下文中的键。
// 用户可以使用 `req.Context().Value(httprouter.ParamsKey)` 来获取参数，
// 但通常直接使用 Handle 函数签名中的 `Params` 参数更方便。
var ParamsKey = paramsKey{}

// ParamsFromContext 从请求上下文中提取 URL 参数，
// 如果不存在则返回 nil。
func ParamsFromContext(ctx context.Context) Params {
	p, _ := ctx.Value(ParamsKey).(Params)
	return p
}

// MatchedRoutePathParam 是存储匹配路由路径的 Param 名称，
// 如果设置了 Router.SaveMatchedRoutePath。
var MatchedRoutePathParam = "$matchedRoutePath"

// MatchedRoutePath 检索匹配路由的路径。
// 必须在添加相应的处理程序时启用 Router.SaveMatchedRoutePath，
// 否则此函数始终返回空字符串。
func (ps Params) MatchedRoutePath() string {
	return ps.ByName(MatchedRoutePathParam)
}

// Middleware 是一个可以封装 http.Handler 的函数。
// 典型的用法是在调用链中执行预处理或后处理操作。
type Middleware func(http.Handler) http.Handler

// RecoveryHandlerFunc 定义了处理从 panic 中恢复的函数的签名。
type RecoveryHandlerFunc func(http.ResponseWriter, *http.Request, interface{})

// ErrorHandlerFunc 定义了处理HTTP错误的函数的签名。
// 它接收状态码以及标准的ResponseWriter和Request。
type ErrorHandlerFunc func(w http.ResponseWriter, r *http.Request, statusCode int)

// defaultErrorHandler 是一个默认的 ErrorHandlerFunc 实现。
// 它简单地使用 http.Error 来发送带有状态码和相应文本的响应。
func defaultErrorHandler(w http.ResponseWriter, r *http.Request, statusCode int) {
	// 在写入响应之前检查客户端是否已断开连接
	select {
	case <-r.Context().Done():
		// 如果客户端已断开连接，则不执行任何操作（或仅记录日志）
		// log.Printf("client disconnected before error %d could be sent for %s", statusCode, r.URL.Path)
		return
	default:
		// http.Error 会设置 Content-Type 和状态码，并写入消息体。
		http.Error(w, http.StatusText(statusCode), statusCode)
	}
}

// Router 是一个 http.Handler，可用于通过可配置的路由将请求分派到不同的处理程序函数。
type Router struct {
	trees map[string]*node

	paramsPool sync.Pool
	maxParams  uint16

	// Middlewares 是应用于所有请求的全局中间件列表。
	// 中间件按照在 Use 方法中添加的顺序执行。
	Middlewares []Middleware

	// 如果启用，在调用处理程序之前将匹配的路由路径添加到 http.Request 上下文。
	// 匹配的路由路径只添加到启用此选项时注册的路由处理程序。
	SaveMatchedRoutePath bool

	// 如果当前路由无法匹配，但存在带（或不带）尾部斜杠的路径处理程序，则启用自动重定向。
	// 例如，如果请求 /foo/ 但只存在 /foo 的路由，则客户端将被重定向到 /foo，
	// 对于 GET 请求使用 http 状态码 301，对于所有其他请求方法使用 308。
	RedirectTrailingSlash bool

	// 如果启用，路由器会尝试修复当前请求路径，如果没有为其注册处理程序。
	// 首先，会移除诸如 ../ 或 // 等多余的路径元素。
	// 然后，路由器会对清理后的路径进行不区分大小写的查找。
	// 如果找到了该路由的处理程序，路由器会以状态码 301（GET 请求）和 308（所有其他请求方法）
	// 重定向到修正后的路径。
	// 例如，/FOO 和 /..//Foo 可以被重定向到 /foo。
	// RedirectTrailingSlash 与此选项无关。
	RedirectFixedPath bool

	// 如果启用，当当前请求无法路由时，路由器会检查是否允许使用其他方法。
	// 如果是这种情况，请求会以“不允许使用的方法”和 HTTP 状态码 405 进行响应。
	// 如果没有允许的其他方法，则将请求委托给 NotFound 处理程序。
	HandleMethodNotAllowed bool

	// 如果启用，路由器会自动回复 OPTIONS 请求。
	// 自定义 OPTIONS 处理程序优先于自动回复。
	HandleOPTIONS bool

	// 一个可选的 http.Handler，在自动 OPTIONS 请求时调用。
	// 只有当 HandleOPTIONS 为 true 且未设置特定路径的 OPTIONS 处理程序时，才会调用此处理程序。
	// 在调用处理程序之前会设置 "Allowed" 头部。
	GlobalOPTIONS http.Handler

	// 全局 (*) 允许方法的缓存值
	globalAllowed string

	// 可配置的 http.Handler，当找不到匹配的路由时调用。
	// 如果未设置，则使用 http.NotFound。
	NotFound http.Handler

	// 可配置的 http.Handler，当请求无法路由且 HandleMethodNotAllowed 为 true 时调用。
	// 如果未设置，则使用 http.Error 和 http.StatusMethodNotAllowed。
	// 在调用处理程序之前会设置包含允许请求方法的 "Allow" 头部。
	MethodNotAllowed http.Handler

	// RecoveryHandler 是处理从 http 处理程序（包括中间件和路由处理程序）恢复的 panic 的函数。
	// 如果设置，当 panic 发生并被恢复时，会调用此函数。
	// 它接收原始的 ResponseWriter、Request 和 panic 的值 (interface{})。
	// 如果未设置，则 panic 会继续传播（回到 net/http 的 ServeHTTP，可能导致连接关闭）。
	RecoveryHandler RecoveryHandlerFunc

	// FileSystemForUnmatched 用于在没有匹配到预定义路由时服务静态文件。
	// 如果设置且 ServeUnmatchedAsStatic 为 true，则未匹配路由将尝试在此文件系统中查找文件。
	FileSystemForUnmatched http.FileSystem

	// ServeUnmatchedAsStatic 如果启用，则将所有未匹配的路由尝试作为静态文件处理，
	// 使用 FileSystemForUnmatched 指定的文件系统。
	ServeUnmatchedAsStatic bool

	// ErrorHandler 是一个统一的错误处理函数。
	// 当 NotFound 或 MethodNotAllowed 为 nil 时，或者在 panic 恢复且 RecoveryHandler 为 nil 时，
	// 此函数将被调用来处理错误。
	// 默认为 defaultErrorHandler，它使用 http.Error。
	errorHandler              ErrorHandlerFunc
	isDefaultErrorHandlerUsed bool
}

// 确保 Router 符合 http.Handler 接口
var _ http.Handler = New()

// New 返回一个新的初始化 Router。
// 路径自动更正，包括尾部斜杠，默认启用。
func New() *Router {
	r := &Router{
		RedirectTrailingSlash:  true,
		RedirectFixedPath:      true,
		HandleMethodNotAllowed: true,
		HandleOPTIONS:          true,
		Middlewares:            make([]Middleware, 0),
	}
	r.setDefaultErrorHandler()
	return r
}

// setDefaultErrorHandler 将路由器的错误处理器设置为默认实现。
func (r *Router) setDefaultErrorHandler() {
	r.errorHandler = defaultErrorHandler
	r.isDefaultErrorHandlerUsed = true
}

// SetErrorHandler 允许用户设置自定义的错误处理函数。
// 如果传入 nil，则会恢复为默认的错误处理函数。
func (r *Router) SetErrorHandler(handler ErrorHandlerFunc) {
	if handler == nil {
		r.setDefaultErrorHandler()
	} else {
		r.errorHandler = handler
		r.isDefaultErrorHandlerUsed = false
	}
}

// GetErrorHandler 返回当前配置的错误处理函数。
// 注意：直接比较返回的函数与 defaultErrorHandler 可能不可靠。
// 请使用 Router.IsUsingDefaultErrorHandler() 来检查是否正在使用默认处理器。
func (r *Router) GetErrorHandler() ErrorHandlerFunc {
	return r.errorHandler
}

// 返回默认errhandle
func (r *Router) GetDefaultErrHandler() ErrorHandlerFunc {
	return defaultErrorHandler
}

// IsUsingDefaultErrorHandler 返回 true 如果当前路由器正在使用默认的错误处理器。
func (r *Router) IsUsingDefaultErrorHandler() bool {
	return r.isDefaultErrorHandlerUsed
}

// Group 代表一个路由组，具有一个路径前缀。
type Group struct {
	router      *Router      // 指向主 Router
	prefix      string       // 该组的路径前缀
	middlewares []Middleware // group级中间件
}

// Group 创建一个新的路由组，所有通过该组注册的路由都将带有给定的路径前缀。
func (r *Router) Group(prefix string) *Group {
	// 1. 组前缀必须以 '/' 开头
	if len(prefix) == 0 || prefix[0] != '/' {
		panic("group prefix must begin with '/' in prefix '" + prefix + "'")
	}

	// 2. 移除尾部斜杠，除非前缀本身就是 "/"
	// 例如: "/admin/" -> "/admin", "/users" -> "/users", "/" -> "/"
	// strings.TrimSuffix 是更简洁的方式
	cleanedPrefix := strings.TrimSuffix(prefix, "/")

	// 3. 如果移除尾部斜杠后变为空字符串，说明原前缀是 "/" 或多个 "/" (e.g., "//", "///")
	// 在这种情况下，合法的组前缀应该是 "/"
	if cleanedPrefix == "" {
		cleanedPrefix = "/"
	}

	// 此时 cleanedPrefix 保证以 "/" 开头，除了 "/" 本身外没有尾部斜杠，并且不会是空字符串

	return &Group{
		router: r,
		prefix: cleanedPrefix, // 使用处理后的前缀
	}
}

func (r *Router) getParams() *Params {
	ps, _ := r.paramsPool.Get().(*Params)
	*ps = (*ps)[0:0] // 重置切片
	return ps
}

func (r *Router) putParams(ps *Params) {
	if ps != nil {
		r.paramsPool.Put(ps)
	}
}

func (r *Router) saveMatchedRoutePath(path string, handle Handle) Handle {
	return func(w http.ResponseWriter, req *http.Request, ps Params) {
		// 确保即使 ps 为 nil（例如，没有路径参数的路由但启用了 SaveMatchedRoutePath），
		// 我们也能正确处理。
		var paramsToUse Params
		var psp *Params // 用于回收

		if ps == nil {
			psp = r.getParams()       // 从池中获取一个新的 *Params
			*psp = (*psp)[:cap(*psp)] // 扩展到底层数组的容量，确保有空间
			if cap(*psp) == 0 {       // 如果池返回的是一个零容量的切片
				temp := make(Params, 1)
				*psp = temp
			} else {
				*psp = (*psp)[:1] // 设置长度为1
			}
			(*psp)[0] = Param{Key: MatchedRoutePathParam, Value: path}
			paramsToUse = *psp
		} else {
			// 如果 ps 不是 nil，我们追加到它。
			// 注意：这可能会修改调用者（例如 root.getValue）拥有的 ps。
			// 通常，ps 是从池中获取的，并在之后放回，所以这是安全的。
			// 为了更安全，可以考虑复制 ps，但这会增加分配。
			// 鉴于 julienschmidt/httprouter 的性能重点，现有行为（可能修改）是可接受的。
			// 但如果 SaveMatchedRoutePath 与不从池中获取 ps 的情况结合，则需小心。
			// 实际上，ps 总是从 getParams 获取的，所以这里是安全的。
			paramsToUse = append(ps, Param{Key: MatchedRoutePathParam, Value: path})
		}

		handle(w, req, paramsToUse)

		if psp != nil { // 如果我们从池中分配了新的 *Params，则将其放回
			r.putParams(psp)
		}
		// 注意：原始的 ps (如果非 nil) 会由 ServeHTTP 中的 defer r.putParams(ps) 回收
	}
}

// Use 将一个或多个全局中间件添加到路由器。
// 中间件按照添加的顺序在处理链中从外向内执行。
// 例如，Use(A, B) 将导致请求按 A -> B -> 最终处理程序 的顺序执行。
func (r *Router) Use(middleware ...Middleware) {
	r.Middlewares = append(r.Middlewares, middleware...)
}

// HTTP method shortcuts
func (r *Router) GET(path string, handle Handle)     { r.Handle(http.MethodGet, path, handle) }
func (r *Router) HEAD(path string, handle Handle)    { r.Handle(http.MethodHead, path, handle) }
func (r *Router) OPTIONS(path string, handle Handle) { r.Handle(http.MethodOptions, path, handle) }
func (r *Router) POST(path string, handle Handle)    { r.Handle(http.MethodPost, path, handle) }
func (r *Router) PUT(path string, handle Handle)     { r.Handle(http.MethodPut, path, handle) }
func (r *Router) PATCH(path string, handle Handle)   { r.Handle(http.MethodPatch, path, handle) }
func (r *Router) DELETE(path string, handle Handle)  { r.Handle(http.MethodDelete, path, handle) }
func (r *Router) Get(path string, handle Handle)     { r.Handle(http.MethodGet, path, handle) }
func (r *Router) Head(path string, handle Handle)    { r.Handle(http.MethodHead, path, handle) }
func (r *Router) Options(path string, handle Handle) { r.Handle(http.MethodOptions, path, handle) }
func (r *Router) Post(path string, handle Handle)    { r.Handle(http.MethodPost, path, handle) }
func (r *Router) Put(path string, handle Handle)     { r.Handle(http.MethodPut, path, handle) }
func (r *Router) Patch(path string, handle Handle)   { r.Handle(http.MethodPatch, path, handle) }
func (r *Router) Delete(path string, handle Handle)  { r.Handle(http.MethodDelete, path, handle) }

/*
// GET 是 router.Handle(http.MethodGet, path, handle) 的快捷方式
func (r *Router) GET(path string, handle Handle) {
	r.Handle(http.MethodGet, path, handle)
}

// HEAD 是 router.Handle(http.MethodHead, path, handle) 的快捷方式
func (r *Router) HEAD(path string, handle Handle) {
	r.Handle(http.MethodHead, path, handle)
}

// OPTIONS 是 router.Handle(http.MethodOptions, path, handle) 的快捷方式
func (r *Router) OPTIONS(path string, handle Handle) {
	r.Handle(http.MethodOptions, path, handle)
}

// POST 是 router.Handle(http.MethodPost, path, handle) 的快捷方式
func (r *Router) POST(path string, handle Handle) {
	r.Handle(http.MethodPost, path, handle)
}

// PUT 是 router.Handle(http.MethodPut, path, handle) 的快捷方式
func (r *Router) PUT(path string, handle Handle) {
	r.Handle(http.MethodPut, path, handle)
}

// PATCH 是 router.Handle(http.MethodPatch, path, handle) 的快捷方式
func (r *Router) PATCH(path string, handle Handle) {
	r.Handle(http.MethodPatch, path, handle)
}

// DELETE 是 router.Handle(http.MethodDelete, path, handle) 的快捷方式
func (r *Router) DELETE(path string, handle Handle) {
	r.Handle(http.MethodDelete, path, handle)
}

// 支持对应Get Put等书写方法作为GET PUT的同名入口
// Get 是 router.Handle(http.MethodGet, path, handle) 的快捷方式
func (r *Router) Get(path string, handle Handle) {
	r.Handle(http.MethodGet, path, handle)
}

// Head 是 router.Handle(http.MethodHead, path, handle) 的快捷方式
func (r *Router) Head(path string, handle Handle) {
	r.Handle(http.MethodHead, path, handle)
}

// Options 是 router.Handle(http.MethodOptions, path, handle) 的快捷方式
func (r *Router) Options(path string, handle Handle) {
	r.Handle(http.MethodOptions, path, handle)
}

// Post 是 router.Handle(http.MethodPost, path, handle) 的快捷方式
func (r *Router) Post(path string, handle Handle) {
	r.Handle(http.MethodPost, path, handle)
}

// Put 是 router.Handle(http.MethodPut, path, handle) 的快捷方式
func (r *Router) Put(path string, handle Handle) {
	r.Handle(http.MethodPut, path, handle)
}

// Patch 是 router.Handle(http.MethodPatch, path, handle) 的快捷方式
func (r *Router) Patch(path string, handle Handle) {
	r.Handle(http.MethodPatch, path, handle)
}

// Delete 是 router.Handle(http.MethodDelete, path, handle) 的快捷方式
func (r *Router) Delete(path string, handle Handle) {
	r.Handle(http.MethodDelete, path, handle)
}
*/

// DefaultMethodsForAny 定义了 ANY 方法将注册的 HTTP 方法列表
var DefaultMethodsForAny = []string{
	http.MethodGet,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodHead,
	http.MethodOptions,
}

// ANY 为所有 DefaultMethodsForAny 中定义的方法注册相同的处理函数。
// 这对于捕获所有类型的请求到单个端点非常有用。
func (r *Router) ANY(path string, handle Handle) {
	for _, method := range DefaultMethodsForAny {
		r.Handle(method, path, handle)
	}
}

// --- 定义group方式 ---

// --- applyGroupMiddlewares 辅助函数 ---
// applyGroupMiddlewares 将组的中间件应用于一个 httprouter.Handle。
// 它返回一个新的 httprouter.Handle，该 Handle 在执行时会首先运行组中间件链。
func applyGroupMiddlewares(middlewares []Middleware, targetHandle Handle) Handle {
	if len(middlewares) == 0 {
		return targetHandle // 没有组中间件，直接返回原 Handle
	}

	// 1. 将 httprouter.Handle 适配为 http.Handler，以便标准的中间件可以应用。
	//    这个适配的 http.Handler 在被调用时，需要能够执行原始的 targetHandle，
	//    并且能够传递 httprouter.Params。由于 Params 是在最外层 ServeHTTP 中解析的，
	//    并且通过第三个参数传递给 httprouter.Handle，我们需要一种方式将这些 Params
	//    传递给适配后的 targetHandle。
	//    我们将通过闭包来捕获 Params。
	adaptedHandlerFunc := func(ps Params) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			targetHandle(w, r, ps)
		}
	}

	// 2. 构建最终的 httprouter.Handle，它在被调用时会执行中间件链。
	return func(w http.ResponseWriter, r *http.Request, ps Params) {
		// 获取为当前参数 ps 适配的 http.Handler
		handlerToWrap := adaptedHandlerFunc(ps)

		// 应用组中间件 (从后往前构建调用链，类似 Router.applyMiddleware)
		var current http.Handler = handlerToWrap
		for i := len(middlewares) - 1; i >= 0; i-- {
			current = middlewares[i](current)
		}

		// 执行被组中间件包裹的处理器
		current.ServeHTTP(w, r)
	}
}

// internal helper to join group prefix with relative path
func joinGroupPath(prefix, relativePath string) string {
	if prefix == "/" {
		if relativePath == "" {
			return "/"
		}
		if relativePath[0] == '/' {
			return relativePath

		}
		return "/" + relativePath
	}
	if relativePath == "" {
		return prefix
	}
	if relativePath == "/" {
		return prefix + "/"
	}
	if relativePath[0] == '/' {
		return prefix + relativePath
	}
	return prefix + "/" + relativePath
}

// Handle 是 Group 的 router.Handle 的快捷方式
func (g *Group) Handle(method, relativePath string, handle Handle) {

	// 调用主 Router 的 Handle 方法
	finalHandle := applyGroupMiddlewares(g.middlewares, handle)
	g.router.Handle(method, joinGroupPath(g.prefix, relativePath), finalHandle)
}

// Handler 是 Group 的 router.Handler 的快捷方式
func (g *Group) Handler(method, relativePath string, handler http.Handler) {
	// 1. 创建一个 httprouter.Handle 来包装原始的 http.Handler
	//    这个 Handle 的作用是将 router 解析的 Params 放入上下文中。
	intermediateHandle := func(w http.ResponseWriter, r *http.Request, p Params) {
		if len(p) > 0 {
			ctx := r.Context()
			ctx = context.WithValue(ctx, ParamsKey, p)
			r = r.WithContext(ctx)
		}
		handler.ServeHTTP(w, r)
	}

	// 2. 应用组中间件到这个 intermediateHandle 上
	finalHandle := applyGroupMiddlewares(g.middlewares, intermediateHandle)

	// 3. 注册最终的、被组中间件包裹的 Handle
	g.router.Handle(method, joinGroupPath(g.prefix, relativePath), finalHandle)
}

// HandlerFunc 是 Group 的 router.HandlerFunc 的快捷方式
func (g *Group) HandlerFunc(method, path string, handler http.HandlerFunc) {
	fullPath := g.prefix
	if path != "" && path != "/" {
		if path[0] == '/' {
			if g.prefix == "/" {
				fullPath = path
			} else {
				fullPath += path
			}
		} else {
			if g.prefix == "/" {
				fullPath += path
			} else {
				fullPath += "/" + path
			}
		}
	} else if path == "/" && g.prefix != "/" {
		if g.prefix != "/" {
			fullPath += "/"
		}
	} else if path == "" && g.prefix == "/" {
		fullPath = "/"
	}
	g.router.HandlerFunc(method, fullPath, handler)
}

// ServeFiles 是 Group 的 router.ServeFiles 的快捷方式
func (g *Group) ServeFiles(relativePath string, root http.FileSystem) {
	if len(relativePath) < 10 || relativePath[len(relativePath)-10:] != "/*filepath" {
		panic("path for ServeFiles must end with /*filepath in path '" + relativePath + "'")
	}

	fileServer := http.FileServer(root)

	// 创建一个 httprouter.Handle 来服务文件
	fileServeHandle := func(w http.ResponseWriter, req *http.Request, ps Params) {
		originalPath := req.URL.Path
		req.URL.Path = ps.ByName("filepath") // 从 Params 中获取实际文件路径
		if len(ps) > 0 {                     // 将 Params 放入上下文，以保持一致性
			ctx := req.Context()
			ctx = context.WithValue(ctx, ParamsKey, ps)
			req = req.WithContext(ctx)
		}
		fileServer.ServeHTTP(w, req)
		req.URL.Path = originalPath // 恢复原始路径
	}

	// 应用组中间件到这个 fileServeHandle
	finalFileServeHandle := applyGroupMiddlewares(g.middlewares, fileServeHandle)

	// 注册这个被包裹的 Handle
	g.router.Handle(http.MethodGet, joinGroupPath(g.prefix, relativePath), finalFileServeHandle)
}

func (g *Group) Use(middleware ...Middleware) {
	g.middlewares = append(g.middlewares, middleware...)
}
func (g *Group) GET(relativePath string, handle Handle) {
	g.Handle(http.MethodGet, relativePath, handle)
}
func (g *Group) HEAD(relativePath string, handle Handle) {
	g.Handle(http.MethodHead, relativePath, handle)
}
func (g *Group) OPTIONS(relativePath string, handle Handle) {
	g.Handle(http.MethodOptions, relativePath, handle)
}
func (g *Group) POST(relativePath string, handle Handle) {
	g.Handle(http.MethodPost, relativePath, handle)
}
func (g *Group) PUT(relativePath string, handle Handle) {
	g.Handle(http.MethodPut, relativePath, handle)
}
func (g *Group) PATCH(relativePath string, handle Handle) {
	g.Handle(http.MethodPatch, relativePath, handle)
}
func (g *Group) DELETE(relativePath string, handle Handle) {
	g.Handle(http.MethodDelete, relativePath, handle)
}
func (g *Group) Get(relativePath string, handle Handle) {
	g.Handle(http.MethodGet, relativePath, handle)
}
func (g *Group) Head(relativePath string, handle Handle) {
	g.Handle(http.MethodHead, relativePath, handle)
}
func (g *Group) Options(relativePath string, handle Handle) {
	g.Handle(http.MethodOptions, relativePath, handle)
}
func (g *Group) Post(relativePath string, handle Handle) {
	g.Handle(http.MethodPost, relativePath, handle)
}
func (g *Group) Put(relativePath string, handle Handle) {
	g.Handle(http.MethodPut, relativePath, handle)
}
func (g *Group) Patch(relativePath string, handle Handle) {
	g.Handle(http.MethodPatch, relativePath, handle)
}
func (g *Group) Delete(relativePath string, handle Handle) {
	g.Handle(http.MethodDelete, relativePath, handle)
}

// ANY 为组内路径注册一个处理所有 DefaultMethodsForAny 中定义的方法的 Handler。
func (g *Group) ANY(path string, handle Handle) {
	fullPath := g.prefix
	if path != "" && path != "/" {
		if path[0] == '/' {
			if g.prefix == "/" {
				fullPath = path
			} else {
				fullPath += path
			}
		} else {
			if g.prefix == "/" {
				fullPath += path
			} else {
				fullPath += "/" + path
			}
		}
	} else if path == "/" && g.prefix != "/" {
		if g.prefix != "/" {
			fullPath += "/"
		}
	} else if path == "" && g.prefix == "/" {
		fullPath = "/"
	}
	g.router.ANY(fullPath, handle) // 委托给 Router 的 ANY 方法
}

// Handle 使用给定的路径和方法注册新的请求处理程序。
// ... (方法内部逻辑保持不变)
func (r *Router) Handle(method, path string, handle Handle) {
	varsCount := uint16(0)

	if method == "" {
		panic("method must not be empty")
	}
	if len(path) < 1 || path[0] != '/' {
		panic("path must begin with '/' in path '" + path + "'")
	}
	if handle == nil {
		panic("handle must not be nil")
	}

	if r.SaveMatchedRoutePath {
		varsCount++
		handle = r.saveMatchedRoutePath(path, handle)
	}

	if r.trees == nil {
		r.trees = make(map[string]*node)
	}

	root := r.trees[method]
	if root == nil {
		root = new(node)
		r.trees[method] = root

		r.globalAllowed = r.allowed("*", "") // 更新全局允许的方法
	}

	root.addRoute(path, handle)

	// 更新 maxParams
	if paramsCount := countParams(path); paramsCount+varsCount > r.maxParams {
		r.maxParams = paramsCount + varsCount
	}

	// 延迟初始化 paramsPool 分配函数
	if r.paramsPool.New == nil && r.maxParams > 0 {
		r.paramsPool.New = func() interface{} {
			ps := make(Params, 0, r.maxParams)
			return &ps
		}
	}
}

// Handler 是一个适配器，允许将 http.Handler 用作请求处理程序。
// Params 在请求上下文中可以通过 ParamsKey 获取。
// **重要**: req.Context() 会被用于传递 Params。
func (r *Router) Handler(method, path string, handler http.Handler) {
	r.Handle(method, path,
		func(w http.ResponseWriter, req *http.Request, p Params) {
			// 确保即使 p 为空 (例如没有路径参数的路由)，我们也不会尝试将 nil 存入 context
			// 虽然 context.WithValue(ctx, key, nil) 是合法的，但 ParamsFromContext 会返回 nil Params。
			// 只有当 p 实际有值时（或者 SaveMatchedRoutePath 导致 p 被创建），才将其放入 context。
			if len(p) > 0 { // 检查 len(p) 而不是 p != nil，因为 p 可能是空的非 nil 切片
				ctx := req.Context()
				ctx = context.WithValue(ctx, ParamsKey, p)
				req = req.WithContext(ctx) // 使用新的 context，其中包含 Params
			}
			handler.ServeHTTP(w, req) // req 现在携带了更新后的 context
		},
	)
}

// HandlerFunc 是一个适配器，允许将 http.HandlerFunc 用作请求处理程序。
func (r *Router) HandlerFunc(method, path string, handler http.HandlerFunc) {
	r.Handler(method, path, handler)
}

// ServeFiles 从给定的文件系统根目录提供文件。
// ... (方法内部逻辑基本保持不变, 但注意 context 的传递)
func (r *Router) ServeFiles(path string, root http.FileSystem) {
	if len(path) < 10 || path[len(path)-10:] != "/*filepath" {
		panic("path must end with /*filepath in path '" + path + "'")
	}

	fileServer := http.FileServer(root)

	r.GET(path, func(w http.ResponseWriter, req *http.Request, ps Params) {
		originalPath := req.URL.Path // 保存原始路径，如果需要的话
		req.URL.Path = ps.ByName("filepath")

		// 将 Params 存储到请求的 context 中，供后续可能需要的中间件或 handler 使用
		// 虽然 fileServer.ServeHTTP 可能不直接使用它，但保持一致性是好的
		if len(ps) > 0 {
			ctx := req.Context()
			ctx = context.WithValue(ctx, ParamsKey, ps) // Store the VALUE in context
			req = req.WithContext(ctx)                  // Use the new context
		}

		// 检查客户端是否已断开连接
		// 注意: fileServer.ServeHTTP 内部可能也会处理 context，但这提供了一个额外的检查点
		// 不过，通常对于静态文件服务，这种检查可能不是必须的，除非文件非常大或传输慢
		// select {
		// case <-req.Context().Done():
		// 	 // 客户端断开，可以记录日志或提前返回，但通常 ServeHTTP 会处理
		// 	 return
		// default:
		// }

		fileServer.ServeHTTP(w, req)
		req.URL.Path = originalPath // 恢复原始路径，以防请求对象被重用或检查
	})
}

// ServeUnmatched 配置路由器将所有未匹配的路由尝试作为静态文件处理。
// fs 指定了静态文件的根目录。
func (r *Router) ServeUnmatched(fs http.FileSystem) {
	r.FileSystemForUnmatched = fs
	r.ServeUnmatchedAsStatic = true
}

func (r *Router) recv(w http.ResponseWriter, req *http.Request) {
	if rcv := recover(); rcv != nil {
		// 在调用 RecoveryHandler 之前，检查请求上下文是否已取消（客户端断开连接）
		// 这有助于避免在客户端已经离开时尝试写入响应。
		select {
		case <-req.Context().Done():
			// 客户端已断开连接，记录错误，但可能无法安全地写入响应。
			// RecoveryHandler 仍然可以被调用用于记录。
			// log.Printf("Client disconnected during panic recovery: %v", req.Context().Err())
			if r.RecoveryHandler != nil {
				// 传递一个标记或者修改 ResponseWriter 以阻止写入
				// 或者让 RecoveryHandler 自行检查 ctx.Done()
				r.RecoveryHandler(w, req, rcv)
			}
			return // 避免在已关闭的连接上写入
		default:
		}

		if r.RecoveryHandler != nil {
			r.RecoveryHandler(w, req, rcv)
		} else if r.errorHandler != nil { // 使用统一的错误处理器处理 panic
			r.errorHandler(w, req, http.StatusInternalServerError)
		} else {
			defaultErrorHandler(w, req, http.StatusInternalServerError)
		}
	}
}

// Lookup 允许手动查找方法 + 路径组合。
// ... (方法内部逻辑保持不变)
func (r *Router) Lookup(method, path string) (Handle, Params, bool) {
	if root := r.trees[method]; root != nil {
		handle, ps, tsr := root.getValue(path, r.getParams)
		if handle == nil {
			r.putParams(ps) // 确保即使未找到处理程序，获取的 params 也能被放回
			return nil, nil, tsr
		}
		// if ps == nil { // ps 可能是空的非 nil 切片，这是有效的
		// 	return handle, nil, tsr
		// }
		// getValue 返回的 ps 是 *Params，所以需要解引用
		if ps != nil {
			return handle, *ps, tsr
		}
		return handle, nil, tsr // 如果 ps 是 nil (例如，没有参数且未启用 SaveMatchedRoutePath)
	}
	return nil, nil, false
}

func (r *Router) allowed(path, reqMethod string) (allow string) {
	allowedMethods := make([]string, 0, 9) // 预分配容量

	if path == "*" { // 服务器范围
		if reqMethod == "" { // 内部调用以刷新缓存 (r.globalAllowed)
			for method := range r.trees {
				if method == http.MethodOptions {
					continue
				}
				allowedMethods = append(allowedMethods, method)
			}
		} else {
			return r.globalAllowed // 直接返回缓存的全局允许方法
		}
	} else { // 特定路径
		for method := range r.trees {
			if method == reqMethod || method == http.MethodOptions {
				continue
			}
			handle, _, _ := r.trees[method].getValue(path, nil) // getValue 不需要 params 池进行检查
			if handle != nil {
				allowedMethods = append(allowedMethods, method)
			}
		}
	}

	if len(allowedMethods) > 0 {
		if r.HandleOPTIONS { // 如果处理 OPTIONS，则将其加入允许列表
			allowedMethods = append(allowedMethods, http.MethodOptions)
		}

		// 排序 (之前的手动排序是有效的)
		for i, l := 1, len(allowedMethods); i < l; i++ {
			for j := i; j > 0 && allowedMethods[j] < allowedMethods[j-1]; j-- {
				allowedMethods[j], allowedMethods[j-1] = allowedMethods[j-1], allowedMethods[j]
			}
		}
		return strings.Join(allowedMethods, ", ")
	}

	return "" // 如果没有允许的方法，则返回空字符串
}

// applyMiddleware 是一个辅助函数，用于将全局中间件应用于给定的 http.Handler。
// ... (方法内部逻辑保持不变)
func (r *Router) applyMiddleware(handler http.Handler) http.Handler {
	current := handler
	for i := len(r.Middlewares) - 1; i >= 0; i-- {
		current = r.Middlewares[i](current)
	}
	return current
}

// ServeHTTP 使路由器实现 http.Handler 接口。
// 它应用全局中间件，然后执行核心路由匹配、处理和错误处理逻辑。
// **重要**: req.Context() 在这里是源头，它会被传递下去。
// 中间件和最终的路由处理函数都可以访问和使用这个上下文。
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 在最外层设置 panic 恢复。
	// defer r.recv(w, req) // 移动到匿名函数内部，以确保它在 applyMiddleware 之后执行的 handler 的 panic 也能捕获
	// 并且确保在核心逻辑执行前应用中间件

	//path := req.URL.Path // 获取请求路径, 在应用中间件前获取，中间件可能会修改它

	// coreRoutingAndHandling 封装了主要的路由查找和处理逻辑。
	// 它是中间件链中的“最内层”处理程序。
	coreRoutingAndHandling := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// 确保在核心处理逻辑中也捕获panic，这样 RecoveryHandler 能正确获取 w, req
		defer func() {
			// 使用原始的 w 和 req (ServeHTTP 的参数) 进行恢复，
			// 因为中间件可能替换了 writer 或 request
			r.recv(w, req)
		}()

		// path 现在从 request 获取，因为中间件可能修改了 request.URL.Path
		currentPath := request.URL.Path

		if root := r.trees[request.Method]; root != nil {
			handle, psPtr, tsr := root.getValue(currentPath, r.getParams) // psPtr is *Params

			// 将 Params 切片放回 pool。
			// 确保即使处理程序 panic，Params 也能被回收。
			if psPtr != nil {
				defer r.putParams(psPtr)
			}

			if handle != nil {
				var params Params
				if psPtr != nil {
					params = *psPtr
				}

				// 将 Params (切片的值) 存储到请求的 context 中
				if len(params) > 0 {
					// 使用 request.Context() 而不是 req.Context()，因为中间件可能更新了 request 的 context
					ctx := request.Context()
					ctx = context.WithValue(ctx, ParamsKey, params)
					request = request.WithContext(ctx) // 更新 request 以携带新的 context
				}

				// 调用路由处理程序
				handle(writer, request, params) // request 包含了更新后的上下文
				return
			} else if request.Method != http.MethodConnect && currentPath != "/" {
				code := http.StatusMovedPermanently // 301
				if request.Method != http.MethodGet {
					code = http.StatusPermanentRedirect // 308
				}

				if tsr && r.RedirectTrailingSlash {
					// 创建一个新的 URL 对象进行重定向，避免修改原始请求的 URL 指针
					redirectURL := *request.URL
					if len(currentPath) > 1 && currentPath[len(currentPath)-1] == '/' {
						redirectURL.Path = currentPath[:len(currentPath)-1]
					} else {
						redirectURL.Path = currentPath + "/"
					}
					http.Redirect(writer, request, redirectURL.String(), code)
					return
				}

				if r.RedirectFixedPath {
					fixedPath, found := root.findCaseInsensitivePath(
						CleanPath(currentPath),
						r.RedirectTrailingSlash,
					)
					if found {
						redirectURL := *request.URL
						redirectURL.Path = fixedPath
						http.Redirect(writer, request, redirectURL.String(), code)
						return
					}
				}
			}
		}

		if request.Method == http.MethodOptions && r.HandleOPTIONS {
			if allow := r.allowed(currentPath, http.MethodOptions); allow != "" {
				writer.Header().Set("Allow", allow)
				if r.GlobalOPTIONS != nil {
					r.GlobalOPTIONS.ServeHTTP(writer, request)
				} else {
					writer.WriteHeader(http.StatusOK)
				}
				return
			}
		} else if r.HandleMethodNotAllowed {
			if allow := r.allowed(currentPath, request.Method); allow != "" {
				writer.Header().Set("Allow", allow)
				if r.MethodNotAllowed != nil {
					r.MethodNotAllowed.ServeHTTP(writer, request)
				} else if r.errorHandler != nil {
					r.errorHandler(writer, request, http.StatusMethodNotAllowed)
				} else {
					defaultErrorHandler(writer, request, http.StatusMethodNotAllowed)
				}
				return
			}
		}

		if r.ServeUnmatchedAsStatic && r.FileSystemForUnmatched != nil {
			// 确保 req.URL.Path 是原始的，如果中间件没有修改它的话。
			// fileServer 应该基于原始请求路径查找文件。
			// 注意：如果中间件修改了 req.URL.Path，这里的行为可能需要调整。
			// 假设 fileServer 应该使用 ServeHTTP 接收到的 req.URL.Path
			// 或者，如果需要，可以传递原始的 path 变量。
			// 为简单起见，我们使用 request.URL.Path，它可能已被中间件修改。
			fileServer := http.FileServer(r.FileSystemForUnmatched)

			// 重要的上下文考虑：如果 FileSystemForUnmatched 是一个实现了 Context-aware ServeHTTP 的 http.FileSystem,
			// 那么 request.Context() 会被正确使用。
			// http.FileServer 本身不直接从 context 读取，但其内部操作 (如 os.Open) 不会受 context 取消的影响，除非文件系统实现特殊。
			// 对于客户端断开连接，如果写入时间很长，TCP/IP 层会处理，写入会失败。
			// 对 fileServer.ServeHTTP 的调用，其内部的 io.Copy 会在写入失败时中止。
			// req.Context().Done() 主要用于应用层取消长时间操作。
			//fileServer.ServeHTTP(writer, request)

			if !r.isDefaultErrorHandlerUsed { // 使用布尔标记判断
				// 用户设置了自定义错误处理器
				// 传递 r.errorHandler 给包装器
				ecw := newErrorCapturingResponseWriter(writer, request, r.errorHandler)
				fileServer.ServeHTTP(ecw, request)
				ecw.processAfterFileServer()
			} else {
				// 用户使用的是默认错误处理器
				fileServer.ServeHTTP(writer, request)
			}
			return
		}

		if r.NotFound != nil {
			r.NotFound.ServeHTTP(writer, request)
		} else if r.errorHandler != nil {
			r.errorHandler(writer, request, http.StatusNotFound)
		} else {
			defaultErrorHandler(writer, request, http.StatusNotFound)
			//http.NotFound(writer, request)
		}
	}) // coreRoutingAndHandling http.HandlerFunc 结束

	// 应用全局中间件到核心路由处理逻辑。
	finalHandler := r.applyMiddleware(coreRoutingAndHandling)

	// 执行完整的处理链（中间件 + 核心逻辑）
	finalHandler.ServeHTTP(w, req)
}
