
# touka-httpc - 功能丰富的 Go HTTP 客户端库

[![Go Report Card](https://goreportcard.com/badge/github.com/satomitouka/touka-httpc)](https://goreportcard.com/report/github.com/satomitouka/touka-httpc)
[![GoDoc](https://godoc.org/github.com/satomitouka/touka-httpc?status.svg)](https://godoc.org/github.com/satomitouka/touka-httpc)

`httpc` 是一个基于 Go 标准库 `net/http` 构建的、灵活且功能丰富的 HTTP 客户端库。它旨在提供更便捷的 API、增强的配置选项以及常用的附加功能，如自动重试、请求构建、响应解码、中间件支持和详细日志记录。

## ✨ 主要特性

*   **流式请求构建器 (Fluent Request Builder)**：通过链式调用轻松构建 HTTP 请求（设置方法、URL、头、查询参数、请求体）。
*   **自动重试机制**：内置指数退避（Exponential Backoff）和可选的 Jitter 抖动策略，可配置重试次数、延迟和触发重试的 HTTP 状态码。
*   **便捷的响应解码**：直接将 HTTP 响应体解码为 JSON、XML、GOB、字符串或字节切片。
*   **中间件支持**：轻松集成自定义逻辑（如认证、日志记录、度量）到请求/响应处理流程中。
*   **高度可配置**：
    *   可定制底层的 `http.Transport` 和 `net.Dialer` (超时、KeepAlive、TLS 握手、代理等)。
    *   可配置客户端级别的默认超时。
    *   可配置 User-Agent。
    *   可配置 HTTP 协议版本 (HTTP/1.1, HTTP/2, H2C)。
*   **缓冲池 (`sync.Pool`)**：高效复用 `bytes.Buffer`，减少内存分配和 GC 压力。
*   **日志记录**：可选的详细请求/响应日志，支持自定义日志输出函数。
*   **上下文传播 (`context.Context`)**：完全支持 Go 的上下文，用于控制超时和取消请求。
*   **标准库兼容接口**：提供与标准库 `http.Client` 类似的 `Do`, `Get`, `Post` 等方法。
*   **结构化 HTTP 错误**：当遇到 >= 400 的状态码时，返回包含状态码、头信息和部分响应体预览的 `HTTPError`。

## 📦 安装

```bash
go get github.com/satomitouka/touka-httpc
```

## 🚀 快速开始

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	httpc "github.com/satomitouka/touka-httpc" 
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func main() {
	// 创建一个带有默认配置的客户端
	// 启用默认日志记录 (打印到控制台)
	client := httpc.New(httpc.WithDumpLog())

	// 设置客户端级别的超时 (可选)
	client.SetTimeout(15 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // 请求级别的超时
	defer cancel()

	var user User
	// 使用请求构建器发送 GET 请求并解码 JSON 响应
	err := client.GET("https://api.example.com/users/1").
		WithContext(ctx).              // 设置请求上下文
		SetHeader("Accept", "application/json"). // 设置请求头
		AddQueryParam("details", "true"). // 添加查询参数
		DecodeJSON(&user)              // 执行请求并解码

	if err != nil {
		// 处理错误，可能是网络错误、超时、解码错误或 HTTPError
		if httpErr, ok := err.(*httpc.HTTPError); ok {
			log.Printf("HTTP Error: Status=%d, Body Preview: %s", httpErr.StatusCode, string(httpErr.Body))
		} else {
			log.Fatalf("Request failed: %v", err)
		}
		return
	}

	fmt.Printf("Fetched User: %+v\n", user)

	// 发送 POST 请求 (JSON)
	newUser := User{Name: "New User"}
	resp, err := client.POST("https://api.example.com/users").
		WithContext(ctx).
		SetJSONBody(newUser) // 自动设置 Content-Type: application/json

	if err != nil {
		// ... 错误处理 ...
		log.Fatalf("POST request failed: %v", err)
		return
	}
	defer resp.Body.Close() // 如果只需要状态码或头，手动获取 Response

	fmt.Printf("POST request successful: Status=%s\n", resp.Status)
}
```

## ⚙️ 配置选项

`httpc` 客户端可以通过 `New()` 函数的可变参数 `Option` 进行配置。

```go
// 创建客户端时应用配置
client := httpc.New(
	httpc.WithTimeout(20*time.Second), // 设置默认请求超时
	httpc.WithUserAgent("MyCustomApp/1.0"), // 设置 User-Agent
	httpc.WithRetryOptions(httpc.RetryOptions{ // 配置重试策略
		MaxAttempts:   3,
		BaseDelay:     200 * time.Millisecond,
		MaxDelay:      2 * time.Second,
		RetryStatuses: []int{429, 500, 503}, // 只在这些状态码时重试
		Jitter:        true, // 启用 Jitter 抖动
	}),
	httpc.WithMaxIdleConns(200), // 设置最大空闲连接数
	httpc.WithIdleConnTimeout(120*time.Second), // 设置空闲连接超时
	httpc.WithDialTimeout(5*time.Second), // 设置 TCP 连接超时
	httpc.WithTLSHandshakeTimeout(10*time.Second), // 设置 TLS 握手超时
	httpc.WithDumpLogFunc(func(ctx context.Context, log string) { // 自定义日志函数
		// 将日志写入文件或发送到日志系统
		fmt.Println("[Custom Logger]", log)
	}),
	// ... 其他配置选项 ...
)
```

**可用的配置选项 (`Option` 函数):**

*   `WithTransport(*http.Transport)`: 提供一个自定义的 `http.Transport`。`httpc` 会将非零字段合并到默认的 Transport 配置中。
*   `WithMaxIdleConns(int)`: 设置整个客户端的最大空闲连接数。
*   `WithIdleConnTimeout(time.Duration)`: 设置空闲连接在关闭前保持打开状态的最长时间。
*   `WithDialTimeout(time.Duration)`: 设置建立 TCP 连接的超时时间。
*   `WithKeepAliveTimeout(time.Duration)`: 设置 TCP Keep-Alive 的间隔时间。
*   `WithTLSHandshakeTimeout(time.Duration)`: 设置 TLS 握手的超时时间。
*   `WithExpectContinueTimeout(time.Duration)`: 设置等待服务器第一个响应头的超时时间 (在使用 "Expect: 100-continue" 时)。
*   `WithBufferSize(int)`: 设置内部缓冲池中每个 `bytes.Buffer` 的初始容量。
*   `WithMaxBufferPoolSize(int)`: (注意: 代码中 `maxBufferPool` 字段似乎未直接限制 `sync.Pool` 大小，而是用于 `defaultPool.Put` 的容量检查)。
*   `WithTimeout(time.Duration)`: 设置客户端级别的默认请求超时时间。如果请求的 `Context` 带有更短的截止时间，则以 `Context` 为准。
*   `WithBufferPool(BufferPool)`: 提供自定义的缓冲池实现。
*   `WithRetryOptions(RetryOptions)`: 设置自定义的重试策略。
*   `WithUserAgent(string)`: 设置 HTTP 请求的 `User-Agent` 头。
*   `WithDumpLog()`: 启用默认的日志记录，输出到标准输出 (`fmt.Println`)。
*   `WithDumpLogFunc(DumpLogFunc)`: 提供自定义的日志记录函数。
*   `WithMiddleware(...MiddlewareFunc)`: 添加一个或多个中间件到请求处理链。
*   `WithProtocols(ProtocolsConfig)`: 配置客户端支持的 HTTP 协议版本（HTTP/1.1, HTTP/2, H2C）。

**动态配置:**

*   `client.SetRetryOptions(RetryOptions)`: 在客户端创建后动态修改重试选项。
*   `client.SetDumpLogFunc(DumpLogFunc)`: 动态设置或更改日志记录函数。
*   `client.SetTimeout(time.Duration)`: 动态设置客户端级别的默认超时。

## 🛠️ 请求构建器 (`RequestBuilder`)

`RequestBuilder` 提供了一种流式接口来构建和配置 HTTP 请求。

```go
client := httpc.New()

// 获取 RequestBuilder
rb := client.POST("https://api.example.com/data")

// 配置请求
req, err := rb.
	WithContext(ctx).                  // 关联 Context
	SetHeader("Authorization", "Bearer <token>"). // 设置单个 Header (覆盖)
	AddHeader("X-Request-ID", "uuid-123").    // 添加 Header (可重复)
	SetHeaders(map[string]string{           // 批量设置 Headers
		"X-Client-Version": "1.1",
		"Accept-Encoding": "gzip",
	}).
	SetQueryParam("filter", "active").        // 设置查询参数 (覆盖)
	AddQueryParam("sort", "name").           // 添加查询参数 (可重复)
	SetQueryParams(map[string]string{       // 批量设置查询参数
		"page": "1",
		"limit": "20",
	}).
	// SetBody(strings.NewReader("raw body data")). // 设置原始 io.Reader Body
	// SetJSONBody(map[string]interface{}{"key": "value"}). // 设置 JSON Body (自动编码和设置 Content-Type)
	// SetXMLBody(MyXMLStruct{...}). // 设置 XML Body (自动编码和设置 Content-Type)
	SetGOBBody(MyGobStruct{...}). // 设置 GOB Body (自动编码和设置 Content-Type)
	Build() // 构建 *http.Request (如果需要手动操作 Request)

if err != nil {
	log.Fatalf("Failed to build request: %v", err)
}

// 可以选择直接执行并处理响应
var result MyResultType
err = rb.DecodeJSON(&result) // Execute + Decode
if err != nil {
	// ... 错误处理 ...
}

// 或者先 Build，然后使用 client.Do (不推荐，会绕过部分RequestBuilder特性)
// resp, err := client.Do(req)

// 或者直接 Execute 获取 *http.Response
resp, err := rb.Execute()
if err != nil {
    // ... 错误处理 ...
}
defer resp.Body.Close()
// ... 处理 resp ...
```

**RequestBuilder 方法:**

*   `GET(url)`, `POST(url)`, `PUT(url)`, `DELETE(url)`, `PATCH(url)`, `HEAD(url)`, `OPTIONS(url)`: 创建对应 HTTP 方法的 `RequestBuilder`。
*   `WithContext(context.Context)`: 设置请求的 `Context`。
*   `SetHeader(key, value)`: 设置单个请求头，如果已存在则覆盖。
*   `AddHeader(key, value)`: 添加请求头，允许同名键存在多个值。
*   `SetHeaders(map[string]string)`: 批量设置请求头。
*   `SetQueryParam(key, value)`: 设置单个 URL 查询参数。
*   `AddQueryParam(key, value)`: 添加 URL 查询参数。
*   `SetQueryParams(map[string]string)`: 批量设置 URL 查询参数。
*   `SetBody(io.Reader)`: 设置请求体为任意 `io.Reader`。需要手动设置 `Content-Type` 头。
*   `SetJSONBody(interface{}) (*RequestBuilder, error)`: 将 Go 对象编码为 JSON 作为请求体，并自动设置 `Content-Type: application/json`。
*   `SetXMLBody(interface{}) (*RequestBuilder, error)`: 将 Go 对象编码为 XML 作为请求体，并自动设置 `Content-Type: application/xml`。
*   `SetGOBBody(interface{}) (*RequestBuilder, error)`: 将 Go 对象编码为 GOB 作为请求体，并自动设置 `Content-Type: application/octet-stream`。
*   `Build() (*http.Request, error)`: 根据当前配置构建一个 `*http.Request` 对象。
*   `Execute() (*http.Response, error)`: 执行构建好的请求，并返回原始的 `*http.Response`。**注意：** 调用者需要负责关闭 `resp.Body`。
*   `DecodeJSON(v interface{}) error`: 执行请求并将 JSON 响应解码到 `v` 中。
*   `DecodeXML(v interface{}) error`: 执行请求并将 XML 响应解码到 `v` 中。
*   `DecodeGOB(v interface{}) error`: 执行请求并将 GOB 响应解码到 `v` 中。
*   `Text() (string, error)`: 执行请求并以字符串形式返回响应体。
*   `Bytes() ([]byte, error)`: 执行请求并以字节切片形式返回响应体。

## 🔄 响应处理

`RequestBuilder` 提供了一系列便捷方法来处理响应：

```go
// 解码 JSON
var data map[string]interface{}
err := client.GET(url).DecodeJSON(&data)

// 解码 XML
var item MyXMLStruct
err = client.GET(url).DecodeXML(&item)

// 解码 GOB
var config MyGobConfig
err = client.GET(url).DecodeGOB(&config)

// 获取纯文本
text, err := client.GET(url).Text()

// 获取字节流
bytes, err := client.GET(url).Bytes()

// 获取原始 Response (需要手动关闭 Body)
resp, err := client.GET(url).Execute()
if err == nil {
    defer resp.Body.Close()
    // 读取 resp.Body 或检查 resp.StatusCode, resp.Header
    if resp.StatusCode == http.StatusOK {
        // ...
    }
}
```

**错误处理 (`HTTPError`):**

当服务器返回的状态码 `>= 400` 时，`DecodeXxx`, `Text`, `Bytes` 和 `Execute` (如果内部发生错误) 方法会返回一个 `*httpc.HTTPError` 类型的错误。这个错误包含了更多上下文信息：

```go
err := client.GET("https://api.example.com/not-found").Text()
if err != nil {
    var httpErr *httpc.HTTPError
    if errors.As(err, &httpErr) {
        fmt.Printf("HTTP Error Detected:\n")
        fmt.Printf("  Status Code: %d\n", httpErr.StatusCode)
        fmt.Printf("  Status Text: %s\n", httpErr.Status)
        fmt.Printf("  Headers:\n")
        for k, v := range httpErr.Header {
            fmt.Printf("    %s: %s\n", k, strings.Join(v, ", "))
        }
        fmt.Printf("  Body Preview: %s\n", string(httpErr.Body)) // 只包含部分 Body
    } else {
        // 其他类型的错误 (网络、超时、解码等)
        fmt.Printf("Non-HTTP error: %v\n", err)
    }
}
```

## ⏳ 自动重试

客户端默认启用基本的重试策略。可以通过 `WithRetryOptions` 或 `SetRetryOptions` 进行配置。

**`RetryOptions` 结构:**

*   `MaxAttempts (int)`: 最大重试次数（0 表示不重试，默认 2 次，总共执行 1 + MaxAttempts 次）。
*   `BaseDelay (time.Duration)`: 初始重试延迟（默认 100ms）。
*   `MaxDelay (time.Duration)`: 最大重试延迟（默认 1s）。
*   `RetryStatuses ([]int)`: 哪些 HTTP 状态码会触发重试（默认 `[429, 500, 502, 503, 504]`）。
*   `Jitter (bool)`: 是否在计算退避延迟时加入随机抖动（有助于防止 "惊群效应"，默认 `false`）。

**重试逻辑:**

1.  当请求返回网络错误 (如 `net.Error`) 或 `http.Response` 的状态码在 `RetryStatuses` 列表中时，会触发重试。
2.  重试延迟使用指数退避算法计算：`delay = BaseDelay * 2^attempt`，但不会超过 `MaxDelay`。
3.  如果 `Jitter` 为 `true`，实际延迟会在计算出的 `delay` 附近随机波动。
4.  如果响应状态码是 `429 Too Many Requests` 并且包含 `Retry-After` 头，客户端会优先使用该头指定的时间作为延迟。
5.  如果达到 `MaxAttempts` 仍然失败，将返回 `ErrMaxRetriesExceeded` 错误（或最后一次请求的错误）。

## 🔗 中间件

中间件允许你在请求发送前或响应返回后注入自定义逻辑。中间件的类型是 `func(next http.Handler) http.Handler`。

```go
// 示例：添加认证 Token 的中间件
func AuthMiddleware(token string) httpc.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 在请求发送前添加 Header
			r.Header.Set("Authorization", "Bearer "+token)
			fmt.Println("AuthMiddleware: Added token")

			// 调用下一个中间件或最终的 http.Handler (即 client.Do)
			next.ServeHTTP(w, r)

			// 可以在这里处理响应，但 httpc 的中间件主要作用于请求发出前
			// 响应处理通常在 Execute/DecodeXxx 之后进行
			fmt.Println("AuthMiddleware: Request finished")
		})
	}
}

// 示例：记录请求耗时的中间件
func TimingMiddleware() httpc.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			fmt.Printf("TimingMiddleware: Request started for %s\n", r.URL.Path)

			next.ServeHTTP(w, r) // 执行请求

			duration := time.Since(start)
			fmt.Printf("TimingMiddleware: Request for %s took %v\n", r.URL.Path, duration)
			// 注意：这里的 w (ResponseWriter) 是内部包装的，用于捕获响应，
			// 不能直接用来修改最终返回给调用者的 *http.Response
		})
	}
}


client := httpc.New(
	httpc.WithMiddleware(
		AuthMiddleware("my-secret-token"),
		TimingMiddleware(),
		// ... 其他中间件
	),
)

// 发送请求时，中间件会按照添加的顺序反向执行（最后一个添加的最先执行请求修改）
client.GET("https://api.example.com/secure/resource").Execute()
```

中间件会按照添加顺序形成一个链条。当执行请求时，请求会依次通过每个中间件（按添加顺序的反向），最后到达实际的 `client.Do` 调用。

## 📜 日志记录

通过 `WithDumpLog()` 或 `WithDumpLogFunc()` 启用请求日志。日志会包含请求时间、方法、URL、协议、头信息以及 Transport 的关键配置。

```go
// 使用默认日志记录 (打印到 stdout)
client := httpc.New(httpc.WithDumpLog())

// 使用自定义日志记录器 (例如，集成 zap logger)
// import "go.uber.org/zap"
// logger, _ := zap.NewProduction()
// client := httpc.New(httpc.WithDumpLogFunc(func(ctx context.Context, log string) {
// 	logger.Info("HTTPC Request", zap.String("details", log))
// }))

client.GET("https://example.com").Execute()
```

**日志输出示例:**

```
[HTTP Request Log]
-------------------------------
Time       : 2023-10-27 10:30:00
Method     : GET
URL        : https://example.com
Host       : example.com
Protocol   : HTTP/2.0
Transport  :
  Type                 : *http.Transport
  MaxIdleConns         : 128
  MaxIdleConnsPerHost  : 64
  MaxConnsPerHost      : 0
  IdleConnTimeout      : 90s
  TLSHandshakeTimeout  : 10s
  DisableKeepAlives    : false
  WriteBufferSize      : 32768
  ReadBufferSize       : 32768
  Protocol             : ...
  H2C                  : false
Headers    :
  User-Agent: Touka HTTP Client
  Accept-Encoding: gzip
-------------------------------
```

## 🌐 协议配置

可以精细控制客户端使用的 HTTP 协议版本。

```go
// 配置只使用 HTTP/1.1
client := httpc.New(
    httpc.WithProtocols(httpc.ProtocolsConfig{
        Http1: true,
        Http2: false,
        Http2_Cleartext: false,
        ForceH2C: false,
    }),
)

// 配置优先使用 HTTP/2 (默认行为)
client = httpc.New(
    httpc.WithProtocols(httpc.ProtocolsConfig{
        Http1: true, // 允许回退到 HTTP/1.1
        Http2: true, // 启用 HTTP/2 (TLS)
        Http2_Cleartext: false,
        ForceH2C: false,
    }),
)

// 配置强制使用 H2C (非加密 HTTP/2)
client = httpc.New(
    httpc.WithProtocols(httpc.ProtocolsConfig{
        ForceH2C: true, // 强制 H2C，会忽略其他设置
    }),
)

// 也可以使用全局函数预设，但不推荐覆盖实例配置
// httpc.SetProtolcols(httpc.ProtocolsConfig{Http1: true, Http2: false})
// c := httpc.New() // 会使用预设值，除非被 Option 覆盖
```

## 🚨 错误处理

除了上面提到的 `HTTPError`，`httpc` 还定义了一些特定的错误变量：

*   `ErrRequestTimeout`: 当请求因超时（来自 `Context` 或 `Dialer/Transport` 配置）失败时返回（通常包装了原始的 `context.DeadlineExceeded` 或 `net.Error` 超时）。
*   `ErrMaxRetriesExceeded`: 当达到最大重试次数后请求仍然失败时返回。
*   `ErrDecodeResponse`: 当解码响应体（JSON, XML, GOB 等）失败时返回（通常包装了原始的解码库错误）。
*   `ErrInvalidURL`: 当提供的 URL 字符串无法解析时返回。

建议使用 `errors.Is` 或 `errors.As` 来检查特定类型的错误。

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！请确保遵循良好的编码风格并添加适当的测试。

## 📄 许可证

本项目使用 WJQserver Studio License 2.0