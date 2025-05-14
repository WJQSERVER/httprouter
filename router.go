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
//	    "fmt"
//	    "github.com/julienschmidt/httprouter"
//	    "net/http"
//	    "log"
//	)
//
//	func Index(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
//	    fmt.Fprint(w, "Welcome!\n")
//	}
//
//	func Hello(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
//	    fmt.Fprintf(w, "hello, %s!\n", ps.ByName("name"))
//	}
//
//	func main() {
//	    router := httprouter.New()
//	    router.GET("/", Index)
//	    router.GET("/hello/:name", Hello)
//
//	    log.Fatal(http.ListenAndServe(":8080", router))
//	}
//
// 路由器根据请求方法和路径匹配传入的请求。
// 如果为该路径和方法注册了处理程序，路由器会将请求委托给该函数。
// 对于 GET, POST, PUT, PATCH, DELETE 和 OPTIONS 方法，存在快捷函数来
// 注册处理程序，对于所有其他方法可以使用 router.Handle。
//
// 注册的路径，路由器将根据它来匹配传入的请求，可以
// 包含两种类型的参数：
//
//	语法    类型
//	:name     命名参数
//	*name     捕获所有参数
//
// 命名参数是动态路径段。它们匹配直到下一个 '/' 或路径结束的任何内容：
//
//	路径: /blog/:category/:post
//
//	请求:
//	 /blog/go/request-routers            匹配: category="go", post="request-routers"
//	 /blog/go/request-routers/           不匹配，但路由器会重定向
//	 /blog/go/                           不匹配
//	 /blog/go/request-routers/comments   不匹配
//
// 捕获所有参数匹配直到路径结束的任何内容，包括
// 目录索引（捕获所有参数前面的 '/'）。因为它们匹配
// 直到末尾的任何内容，捕获所有参数必须始终是最终路径元素。
//
//	路径: /files/*filepath
//
//	请求:
//	 /files/                             匹配: filepath="/"
//	 /files/LICENSE                      匹配: filepath="/LICENSE"
//	 /files/templates/article.html       匹配: filepath="/templates/article.html"
//	 /files                              不匹配，但路由器会重定向
//
// 参数的值作为 Param 结构体的切片保存，每个结构体包含
// 一个键和一个值。该切片作为第三个参数传递给 Handle 函数。
// 有两种方法检索参数的值：
//
//	// 按参数名称
//	user := ps.ByName("user") // 由 :user 或 *user 定义
//
//	// 按参数索引。这种方法你也可以获取名称 (key)
//	thirdKey   := ps[2].Key   // 第 3 个参数的名称
//	thirdValue := ps[2].Value // 第 3 个参数的值
package httprouter

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// Handle 是一个可以注册到路由以处理 HTTP 请求的函数。
// 类似于 http.HandlerFunc，但有第三个参数用于通配符（路径变量）的值。
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
}

// 确保 Router 符合 http.Handler 接口
var _ http.Handler = New()

// New 返回一个新的初始化 Router。
// 路径自动更正，包括尾部斜杠，默认启用。
func New() *Router {
	return &Router{
		RedirectTrailingSlash:  true,
		RedirectFixedPath:      true,
		HandleMethodNotAllowed: true,
		HandleOPTIONS:          true,
		Middlewares:            make([]Middleware, 0),
	}
}

// Group 代表一个路由组，具有一个路径前缀。
type Group struct {
	router *Router // 指向主 Router
	prefix string  // 该组的路径前缀
}

// Group 创建一个新的路由组，所有通过该组注册的路由都将带有给定的路径前缀。
func (r *Router) Group(prefix string) *Group {
	if len(prefix) == 0 || prefix[0] != '/' {
		// 组前缀必须以 '/' 开头
		panic("group prefix must begin with '/' in prefix '" + prefix + "'")
	}
	// 确保前缀没有尾部斜杠，除非前缀本身就是 "/"
	if len(prefix) > 1 && prefix[len(prefix)-1] == '/' {
		panic("group prefix must not end with a trailing slash in prefix '" + prefix + "'")
	}

	return &Group{
		router: r,
		prefix: prefix,
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
		if ps == nil {
			psp := r.getParams()
			ps = (*psp)[0:1]
			ps[0] = Param{Key: MatchedRoutePathParam, Value: path}
			handle(w, req, ps)
			r.putParams(psp)
		} else {
			ps = append(ps, Param{Key: MatchedRoutePathParam, Value: path})
			handle(w, req, ps)
		}
	}
}

// Use 将一个或多个全局中间件添加到路由器。
// 中间件按照添加的顺序在处理链中从外向内执行。
// 例如，Use(A, B) 将导致请求按 A -> B -> 最终处理程序 的顺序执行。
func (r *Router) Use(middleware ...Middleware) {
	r.Middlewares = append(r.Middlewares, middleware...)
}

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

// --- 定义group方式 ---

// Handle 是 Group 的 router.Handle 的快捷方式
func (g *Group) Handle(method, path string, handle Handle) {
	// 将组前缀添加到路径
	fullPath := g.prefix + path
	// 调用主 Router 的 Handle 方法
	g.router.Handle(method, fullPath, handle)
}

// Handler 是 Group 的 router.Handler 的快捷方式
func (g *Group) Handler(method, path string, handler http.Handler) {
	// 将组前缀添加到路径
	fullPath := g.prefix + path
	// 调用主 Router 的 Handler 方法
	g.router.Handler(method, fullPath, handler)
}

// HandlerFunc 是 Group 的 router.HandlerFunc 的快捷方式
func (g *Group) HandlerFunc(method, path string, handler http.HandlerFunc) {
	// 将组前缀添加到路径
	fullPath := g.prefix + path
	// 调用主 Router 的 HandlerFunc 方法
	g.router.HandlerFunc(method, fullPath, handler)
}

// ServeFiles 是 Group 的 router.ServeFiles 的快捷方式
func (g *Group) ServeFiles(path string, root http.FileSystem) {
	// 验证路径
	if len(path) < 10 || path[len(path)-10:] != "/*filepath" {
		panic("path must end with /*filepath in path '" + path + "'")
	}
	// 将组前缀添加到路径
	fullPath := g.prefix + path
	// 调用主 Router 的 ServeFiles 方法
	g.router.ServeFiles(fullPath, root)
}

// Use 是 Group 的 router.Use 的快捷方式
func (g *Group) Use(middleware ...Middleware) {
	g.router.Use(middleware...)
}

// GET 是 Group 的 router.GET 的快捷方式
func (g *Group) GET(path string, handle Handle) {
	g.Handle(http.MethodGet, path, handle)
}

// HEAD 是 Group 的 router.HEAD 的快捷方式
func (g *Group) HEAD(path string, handle Handle) {
	g.Handle(http.MethodHead, path, handle)
}

// OPTIONS 是 Group 的 router.OPTIONS 的快捷方式
func (g *Group) OPTIONS(path string, handle Handle) {
	g.Handle(http.MethodOptions, path, handle)
}

// POST 是 Group 的 router.POST 的快捷方式
func (g *Group) POST(path string, handle Handle) {
	g.Handle(http.MethodPost, path, handle)
}

// PUT 是 Group 的 router.PUT 的快捷方式
func (g *Group) PUT(path string, handle Handle) {
	g.Handle(http.MethodPut, path, handle)
}

// PATCH 是 Group 的 router.PATCH 的快捷方式
func (g *Group) PATCH(path string, handle Handle) {
	g.Handle(http.MethodPatch, path, handle)
}

// DELETE 是 Group 的 router.DELETE 的快捷方式
func (g *Group) DELETE(path string, handle Handle) {
	g.Handle(http.MethodDelete, path, handle)
}

// 支持对应Get Put等书写方法作为GET PUT的同名入口
// Get 是 Group 的 router.Get 的快捷方式
func (g *Group) Get(path string, handle Handle) {
	g.Handle(http.MethodGet, path, handle)
}

// Head 是 Group 的 router.Head 的快捷方式
func (g *Group) Head(path string, handle Handle) {
	g.Handle(http.MethodHead, path, handle)
}

// Options 是 Group 的 router.Options 的快捷方式
func (g *Group) Options(path string, handle Handle) {
	g.Handle(http.MethodOptions, path, handle)
}

// Post 是 Group 的 router.Post 的快捷方式
func (g *Group) Post(path string, handle Handle) {
	g.Handle(http.MethodPost, path, handle)
}

// Put 是 Group 的 router.Put 的快捷方式
func (g *Group) Put(path string, handle Handle) {
	g.Handle(http.MethodPut, path, handle)
}

// Patch 是 Group 的 router.Patch 的快捷方式
func (g *Group) Patch(path string, handle Handle) {
	g.Handle(http.MethodPatch, path, handle)
}

// Delete 是 Group 的 router.Delete 的快捷方式
func (g *Group) Delete(path string, handle Handle) {
	g.Handle(http.MethodDelete, path, handle)
}

// Handle 使用给定的路径和方法注册新的请求处理程序。
//
// 对于 GET, POST, PUT, PATCH 和 DELETE 请求，可以使用相应的快捷函数。
//
// 此函数旨在用于批量加载并允许使用较不常用、非标准化或自定义的方法
// （例如用于与代理进行内部通信）。
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

		r.globalAllowed = r.allowed("*", "")
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
func (r *Router) Handler(method, path string, handler http.Handler) {
	r.Handle(method, path,
		func(w http.ResponseWriter, req *http.Request, p Params) {
			if len(p) > 0 {
				ctx := req.Context()
				ctx = context.WithValue(ctx, ParamsKey, p)
				req = req.WithContext(ctx)
			}
			handler.ServeHTTP(w, req)
		},
	)
}

// HandlerFunc 是一个适配器，允许将 http.HandlerFunc 用作请求处理程序。
func (r *Router) HandlerFunc(method, path string, handler http.HandlerFunc) {
	r.Handler(method, path, handler)
}

// ServeFiles 从给定的文件系统根目录提供文件。
// 路径必须以 "/*filepath" 结尾，然后从本地路径 /defined/root/dir/*filepath 提供文件。
// 例如，如果 root 是 "/etc" 且 *filepath 是 "passwd"，则会提供本地文件 "/etc/passwd"。
// 内部使用 http.FileServer，因此使用 http.NotFound 而不是 Router 的 NotFound 处理程序。
// 要使用操作系统的文件系统实现，
// 使用 http.Dir：
//
//	router.ServeFiles("/src/*filepath", http.Dir("/var/www"))
func (r *Router) ServeFiles(path string, root http.FileSystem) {
	if len(path) < 10 || path[len(path)-10:] != "/*filepath" {
		panic("path must end with /*filepath in path '" + path + "'")
	}

	fileServer := http.FileServer(root)

	r.GET(path, func(w http.ResponseWriter, req *http.Request, ps Params) {
		req.URL.Path = ps.ByName("filepath")

		// 新增部分
		ctx := req.Context()
		if ps != nil { // ps should not be nil here due to /*filepath
			ctx = context.WithValue(ctx, ParamsKey, ps) // Store the VALUE in context
			req = req.WithContext(ctx)                  // Use the new context
		}

		fileServer.ServeHTTP(w, req)
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
		if r.RecoveryHandler != nil {
			r.RecoveryHandler(w, req, rcv)
		} else {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}

	}
}

// Lookup 允许手动查找方法 + 路径组合。
// 例如，这对于围绕此路由器构建框架很有用。
// 如果找到了路径，它返回处理程序函数和路径参数值。
// 否则，第三个返回值指示是否应重定向到不带尾部斜杠的相同路径。
func (r *Router) Lookup(method, path string) (Handle, Params, bool) {
	if root := r.trees[method]; root != nil {
		handle, ps, tsr := root.getValue(path, r.getParams)
		if handle == nil {
			r.putParams(ps)
			return nil, nil, tsr
		}
		if ps == nil {
			return handle, nil, tsr
		}
		return handle, *ps, tsr
	}
	return nil, nil, false
}

func (r *Router) allowed(path, reqMethod string) (allow string) {
	allowed := make([]string, 0, 9)

	if path == "*" { // 服务器范围
		// 空方法用于内部调用以刷新缓存
		if reqMethod == "" {
			for method := range r.trees {
				if method == http.MethodOptions {
					continue
				}
				// 将请求方法添加到允许方法列表
				allowed = append(allowed, method)
			}
		} else {
			return r.globalAllowed
		}
	} else { // 特定路径
		for method := range r.trees {
			// 跳过请求的方法 - 我们已经尝试过这个方法
			if method == reqMethod || method == http.MethodOptions {
				continue
			}

			handle, _, _ := r.trees[method].getValue(path, nil)
			if handle != nil {
				// 将请求方法添加到允许方法列表
				allowed = append(allowed, method)
			}
		}
	}

	if len(allowed) > 0 {
		// 将请求方法添加到允许方法列表
		if r.HandleOPTIONS {
			allowed = append(allowed, http.MethodOptions)
		}

		// 排序允许的方法。
		// sort.Strings(allowed) 不幸的是会导致不必要的分配，
		// 因为 allowed 被移动到堆中并进行接口转换。
		for i, l := 1, len(allowed); i < l; i++ {
			for j := i; j > 0 && allowed[j] < allowed[j-1]; j-- {
				allowed[j], allowed[j-1] = allowed[j-1], allowed[j]
			}
		}

		// 作为逗号分隔列表返回
		return strings.Join(allowed, ", ")
	}

	return allow
}

// applyMiddleware 是一个辅助函数，用于将全局中间件应用于给定的 http.Handler。
// 它按照在 Router.Use 中添加的顺序的逆序（从后往前）构建调用链，
// 以实现洋葱模型：Middleware1(Middleware2(FinalHandler)).ServeHTTP(...)
func (r *Router) applyMiddleware(handler http.Handler) http.Handler {
	var current http.Handler = handler
	// Apply middlewares in reverse order to build the chain
	for i := len(r.Middlewares) - 1; i >= 0; i-- {
		current = r.Middlewares[i](current)
	}
	return current
}

// ServeHTTP 使路由器实现 http.Handler 接口。
// 它应用全局中间件，然后执行核心路由匹配、处理和错误处理逻辑。
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 在最外层设置 panic 恢复。
	// 如果设置了 PanicHandler，它将捕获整个 ServeHTTP 执行过程中的 panic。
	//if r.PanicHandler != nil {
	//	defer r.recv(w, req)
	//	}

	defer func() {
		r.recv(w, req)
	}()

	// coreRoutingAndHandling 封装了主要的路由查找和处理逻辑。
	// 它是中间件链中的“最内层”处理程序。
	coreRoutingAndHandling := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path // 获取请求路径

		// 查找与请求方法对应的路由树
		if root := r.trees[req.Method]; root != nil {
			// 在 Trie 树中查找匹配的路由和参数
			// getValue 如果需要，会从 pool 获取 Params 切片
			handle, ps, tsr := root.getValue(path, r.getParams)

			// 延迟将 Params 切片放回 pool。
			// 确保即使处理程序 panic，Params 也能被回收。
			if ps != nil {
				defer r.putParams(ps)
			}

			if handle != nil {
				// 找到匹配的路由处理程序！

				// 将 Params (切片的值) 存储到请求的 context 中，供后续中间件或 handler 使用。
				if ps != nil { // 仅当找到参数或 SaveMatchedRoutePath 启用时才放
					ctx := req.Context()
					ctx = context.WithValue(ctx, ParamsKey, *ps) // 存储切片的值
					req = req.WithContext(ctx)                   // 使用新 context
				}

				// 调用路由处理程序。
				// 这里传递 *ps (Params 切片的值)，无论是 httprouter.Handle 还是 http.Handler 适配器都能正确处理。
				if ps == nil {
					handle(w, req, nil)
				} else {
					handle(w, req, *ps)
				}

				// 请求处理完成
				return
			} else if req.Method != http.MethodConnect && path != "/" {
				// 没有找到精确匹配，检查是否需要重定向（尾部斜杠或固定路径）

				// 确定重定向状态码
				code := http.StatusMovedPermanently
				if req.Method != http.MethodGet {
					code = http.StatusPermanentRedirect
				}

				// 处理尾部斜杠重定向
				if tsr && r.RedirectTrailingSlash {
					if len(path) > 1 && path[len(path)-1] == '/' {
						req.URL.Path = path[:len(path)-1]
					} else {
						req.URL.Path = path + "/"
					}
					http.Redirect(w, req, req.URL.String(), code)
					return
				}

				// 尝试修复请求路径（如大小写、冗余斜杠）
				if r.RedirectFixedPath {
					fixedPath, found := root.findCaseInsensitivePath(
						CleanPath(path),
						r.RedirectTrailingSlash,
					)
					if found {
						req.URL.Path = fixedPath
						http.Redirect(w, req, req.URL.String(), code)
						return
					}
				}
				// 如果没有重定向，继续处理 405/404
			}
		}

		// 如果没有匹配的路由或重定向，处理 fallback 情况：OPTIONS, 405, 404

		// 处理 OPTIONS 请求
		if req.Method == http.MethodOptions && r.HandleOPTIONS {
			if allow := r.allowed(path, http.MethodOptions); allow != "" {
				w.Header().Set("Allow", allow)
				if r.GlobalOPTIONS != nil {
					r.GlobalOPTIONS.ServeHTTP(w, req)
				} else {
					w.WriteHeader(http.StatusOK)
				}
				return
			}
		} else if r.HandleMethodNotAllowed { // 处理 405 Method Not Allowed
			if allow := r.allowed(path, req.Method); allow != "" {
				w.Header().Set("Allow", allow)
				if r.MethodNotAllowed != nil {
					r.MethodNotAllowed.ServeHTTP(w, req)
				} else {
					http.Error(w,
						http.StatusText(http.StatusMethodNotAllowed),
						http.StatusMethodNotAllowed,
					)
				}
				return
			}
		}

		// 处理不定路由
		if r.ServeUnmatchedAsStatic && r.FileSystemForUnmatched != nil {
			fileServer := http.FileServer(r.FileSystemForUnmatched)
			fileServer.ServeHTTP(w, req)
			return
		}

		// 处理 404 Not Found
		if r.NotFound != nil {
			r.NotFound.ServeHTTP(w, req)
		} else {
			http.NotFound(w, req)
		}
	}) // coreRoutingAndHandling http.HandlerFunc 结束

	// 应用全局中间件到核心路由处理逻辑。
	// 中间件按照 Use() 添加的顺序从外向内执行。
	finalHandler := r.applyMiddleware(coreRoutingAndHandling)

	// 执行完整的处理链（中间件 + 核心逻辑）
	finalHandler.ServeHTTP(w, req)
}
