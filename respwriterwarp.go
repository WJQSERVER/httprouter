package httprouter // 或者你项目的包名

import (
	"net/http"
)

// errorCapturingResponseWriter 用于在 FileServer 处理时捕获错误状态码，
// 并在用户设置了自定义 ErrorHandler 时，用该 ErrorHandler 处理此错误。
type errorCapturingResponseWriter struct {
	w                   http.ResponseWriter // 原始的 ResponseWriter
	r                   *http.Request       // 当前请求，用于传递给 ErrorHandlerFunc
	errorHandlerFunc    ErrorHandlerFunc    // 实际要调用的错误处理函数
	statusCode          int                 // FileServer 尝试设置的状态码
	headerSnapshot      http.Header         // FileServer 在调用 WriteHeader 前可能设置的头部快照
	capturedErrorSignal bool                // 标记 FileServer 是否意图发送一个错误状态码 (>=400)
	responseStarted     bool                // 标记包装器是否已经向原始 w 发送过任何数据 (通过 WriteHeader 或 Write)
}

// newErrorCapturingResponseWriter 创建一个新的 errorCapturingResponseWriter 实例。
// 它接收原始的 ResponseWriter、当前的 Request 和将用于处理捕获错误的 ErrorHandlerFunc。
func newErrorCapturingResponseWriter(w http.ResponseWriter, r *http.Request, eh ErrorHandlerFunc) *errorCapturingResponseWriter {
	return &errorCapturingResponseWriter{
		w:                w,
		r:                r,
		errorHandlerFunc: eh, // 存储传入的错误处理函数
		headerSnapshot:   make(http.Header),
	}
}

// Header 返回一个 http.Header。
// 如果错误信号已激活 (capturedErrorSignal is true)，则操作内部的快照头部，
// 因为这些头部可能不会被发送，或者会被 ErrorHandlerFunc 覆盖。
// 否则，代理到原始 ResponseWriter 的 Header()。
func (ecw *errorCapturingResponseWriter) Header() http.Header {
	if ecw.capturedErrorSignal {
		return ecw.headerSnapshot
	}
	// 如果响应已经开始但不是错误信号（例如，成功路径且FileServer先设置header再WriteHeader），
	// 也应该允许修改实际的头部。
	// 但通常，在WriteHeader之后修改头部是无效的。
	// 为了简单，这里只基于 capturedErrorSignal。
	// 另一种做法是，如果 responseStarted 为 true，则总是返回 ecw.w.Header()。
	// 但考虑到 FileServer 的行为，它通常在 WriteHeader 之前设置头部。
	return ecw.w.Header()
}

// WriteHeader 记录状态码。
// 如果状态码表示错误 (>=400)，则激活 capturedErrorSignal 并不将状态码传递给原始 ResponseWriter。
// 如果状态码表示成功，则将快照中的头部（如果有）复制到原始 w，然后调用原始 w.WriteHeader。
func (ecw *errorCapturingResponseWriter) WriteHeader(statusCode int) {
	// 如果响应已经通过此包装器开始（无论是成功还是错误路径已被处理），
	// 则忽略后续的 WriteHeader 调用。
	if ecw.responseStarted {
		// log.Printf("httprouter: warning: WriteHeader called on ecw after response already started (status %d, new %d ignored)", ecw.statusCode, statusCode)
		return
	}

	ecw.statusCode = statusCode // 总是记录 FileServer 意图的状态码

	if statusCode >= http.StatusBadRequest {
		// 是一个错误状态码。激活错误信号。
		// 不会将这个 WriteHeader 传递给原始的 w，等待 processAfterFileServer 处理。
		ecw.capturedErrorSignal = true
		// FileServer 在调用 WriteHeader(error) 后可能还会调用 Header().Set()，
		// 这些操作会作用于 ecw.headerSnapshot。
	} else {
		// 是成功状态码。
		// 将 ecw.headerSnapshot 中（由 FileServer 在此之前通过 ecw.Header() 设置的）任何头部复制到原始的 w.Header()。
		// 确保这在调用 w.WriteHeader() 之前完成。
		for k, v := range ecw.headerSnapshot {
			// 在将快照头部复制到实际头部之前，清除实际头部中可能已存在的同名键，以避免重复。
			// 或者，如果原始的 ecw.w.Header() 在此之前不应该被修改，那么直接 Add 就可以了。
			// 假设原始 w.Header() 是干净的，或者 FileServer 的行为是先 Header().Set() 再 WriteHeader()。
			// 为安全起见，如果原始 w.Header() 可能已被其他地方修改，应该先 Get/Del 或直接 Set。
			// 这里我们简单地 Add，如果 FileServer 总是先清空再设置，或者我们是唯一的写入者，这是可以的。
			// 通常，标准的 http.ResponseWriter 实现允许多次 Add 同一个 key。
			for _, vv := range v {
				ecw.w.Header().Add(k, vv) // 或者 ecw.w.Header().Set(k, strings.Join(v, ",")) 如果需要替换
			}
		}
		ecw.w.WriteHeader(statusCode)
		ecw.responseStarted = true // 标记成功响应已开始
	}
}

// Write 将数据写入响应。
// 如果 capturedErrorSignal 为 true，则丢弃数据，因为 ErrorHandlerFunc 将负责响应体。
// 如果是成功路径，则在必要时先发送隐式的 200 OK 头部，然后将数据写入原始 ResponseWriter。
func (ecw *errorCapturingResponseWriter) Write(data []byte) (int, error) {
	if ecw.capturedErrorSignal {
		// 错误信号已激活，不写入 FileServer 尝试发送的 body。
		// ErrorHandlerFunc 将负责生成响应体。
		return len(data), nil // 假装写入成功
	}

	// 如果响应尚未开始（即 WriteHeader 未被调用，或以成功状态码调用但尚未写入body）
	// 并且这是第一次 Write 调用，则意味着 http 库将隐式发送 200 OK。
	if !ecw.responseStarted {
		// 如果 statusCode 仍为0 (WriteHeader从未被调用), 则 FileServer 意图是 200 OK。
		if ecw.statusCode == 0 {
			ecw.statusCode = http.StatusOK
		}
		// 此时 ecw.statusCode 应该是成功的状态码 (2xx)。
		// 如果 headerSnapshot 有内容，并且 WriteHeader 还没来得及应用它们，这里应用。
		// (这种情况是 FileServer 直接 Write 而没有先调用 WriteHeader)。
		for k, v := range ecw.headerSnapshot {
			for _, vv := range v {
				ecw.w.Header().Add(k, vv)
			}
		}
		ecw.w.WriteHeader(ecw.statusCode) // 发送实际的状态码 (可能是200或之前设置的2xx)
		ecw.responseStarted = true
	}
	return ecw.w.Write(data)
}

// Flush 尝试刷新缓冲的数据到客户端。
// 仅当未捕获错误且原始 ResponseWriter 支持 http.Flusher 时才执行。
func (ecw *errorCapturingResponseWriter) Flush() {
	if flusher, ok := ecw.w.(http.Flusher); ok {
		if !ecw.capturedErrorSignal && ecw.responseStarted {
			flusher.Flush()
		}
		// 如果 capturedErrorSignal 为 true，我们不希望刷新任何 FileServer 可能已缓冲的错误内容（理论上不应有）。
		// 如果 responseStarted 为 false (且非错误)，则刷新也无意义。
	}
}

// processAfterFileServer 在 http.FileServer.ServeHTTP 调用完成后执行。
// 如果之前捕获了错误信号 (capturedErrorSignal is true) 并且响应尚未开始，
// 它将调用配置的 ErrorHandlerFunc 来处理错误。
func (ecw *errorCapturingResponseWriter) processAfterFileServer() {
	if ecw.capturedErrorSignal && !ecw.responseStarted {
		// FileServer 意图发送一个错误 (ecw.statusCode 已被记录为 >=400)，
		// 并且我们的包装器还没有代表成功路径向客户端发送任何响应头部或主体。
		// 现在调用用户自定义的 ErrorHandlerFunc。
		// ecw.w (原始 ResponseWriter) 此时是“干净”的（除了可能通过 ecw.Header() -> ecw.w.Header() 设置的非错误情况下的头部），
		// ErrorHandlerFunc 可以完全控制响应。
		if ecw.errorHandlerFunc != nil {
			ecw.errorHandlerFunc(ecw.w, ecw.r, ecw.statusCode)
			// ecw.responseStarted = true // 标记响应已由 ErrorHandler 处理
		} else {
			// 理论上不应发生，因为 errorHandlerFunc 应该总是被提供。
			// 作为后备，可以调用一个非常基础的默认错误处理。
			http.Error(ecw.w, http.StatusText(ecw.statusCode), ecw.statusCode)
		}
	}
	// 如果 !ecw.capturedErrorSignal，则成功路径已通过代理写入 ecw.w，无需额外操作。
	// 如果 ecw.capturedErrorSignal && ecw.responseStarted，这意味着在捕获错误信号之前，
	// 成功路径的响应已经开始（例如，FileServer 发送了 206 Partial Content，然后发生了错误）。
	// 这种混合情况非常复杂，此时覆盖已发送的部分响应通常是不可能的或不安全的。
	// 当前逻辑假设一旦 responseStarted (for success)，我们就不能再用 ErrorHandler 回退。
}
