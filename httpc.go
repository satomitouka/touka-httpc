package httpc

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/WJQSERVER-STUDIO/go-utils/copyb"
)

// 错误定义
var (
	ErrRequestTimeout     = errors.New("httpc: request timeout")
	ErrMaxRetriesExceeded = errors.New("httpc: max retries exceeded")
	ErrDecodeResponse     = errors.New("httpc: failed to decode response body")
	ErrInvalidURL         = errors.New("httpc: invalid URL")
)

// 默认配置常量
const (
	defaultBufferSize            = 32 << 10 // 32KB
	defaultMaxBufferPool         = 100
	defaultUserAgent             = "Touka HTTP Client"
	defaultMaxIdleConns          = 128
	defaultIdleConnTimeout       = 90 * time.Second
	defaultDialTimeout           = 10 * time.Second
	defaultKeepAliveTimeout      = 30 * time.Second
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultExpectContinueTimeout = 1 * time.Second
)

var enableH2C = false // 是否启用 HTTP/2 Cleartext 连接

var bufferPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, defaultBufferSize))
	},
}

// DumpLogFunc 定义日志记录函数
type DumpLogFunc func(ctx context.Context, log string)

// Client 主客户端结构
type Client struct {
	client        *http.Client
	transport     *http.Transport
	retryOpts     RetryOptions
	bufferPool    BufferPool
	userAgent     string
	dumpLog       DumpLogFunc      // 日志记录函数
	maxIdleConns  int              // 最大空闲连接数
	bufferSize    int              // 缓冲池 buffer 大小
	maxBufferPool int              // 最大缓冲池数量
	timeout       time.Duration    // 默认请求超时时间 (可选)
	middlewares   []MiddlewareFunc // 中间件链
	dialer        *net.Dialer      // dialer实例
}

// RetryOptions 重试配置
type RetryOptions struct {
	MaxAttempts   int
	BaseDelay     time.Duration
	MaxDelay      time.Duration
	RetryStatuses []int
	Jitter        bool // 是否启用 Jitter 抖动
}

// BufferPool 缓冲池接口
type BufferPool interface {
	Get() *bytes.Buffer
	Put(*bytes.Buffer)
}

// 默认缓冲池实现
type defaultPool struct {
	bufferSize int
}

func newDefaultPool(bufferSize int) *defaultPool {
	return &defaultPool{bufferSize: bufferSize}
}

func (p *defaultPool) Get() *bytes.Buffer {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

func (p *defaultPool) Put(buf *bytes.Buffer) {
	if buf.Cap() > p.bufferSize*2 { // 防止内存泄漏，基于配置的 bufferSize
		return
	}
	bufferPool.Put(buf)
}

// Option 配置选项类型
type Option func(*Client)

// WithTransport 自定义 Transport，将非零字段合并到默认 Transport 中
func WithTransport(t *http.Transport) Option {
	return func(c *Client) {
		defaultTransport := c.transport
		mergeTransport(defaultTransport, t)
		c.transport = defaultTransport
		c.client.Transport = defaultTransport
	}
}

// WithMaxIdleConns 设置最大空闲连接数
func WithMaxIdleConns(maxIdleConns int) Option {
	return func(c *Client) {
		c.maxIdleConns = maxIdleConns
	}
}

// WithIdleConnTimeout 设置空闲连接超时时间
func WithIdleConnTimeout(idleConnTimeout time.Duration) Option {
	return func(c *Client) {
		c.transport.IdleConnTimeout = idleConnTimeout
	}
}

// WithDialTimeout 设置 DialContext 的超时时间
func WithDialTimeout(dialTimeout time.Duration) Option {
	return func(c *Client) {
		// 直接修改 c.dialer.Timeout
		c.dialer.Timeout = dialTimeout
		// 重新将 dialer.DialContext 赋值给 transport.DialContext
		c.transport.DialContext = c.dialer.DialContext
	}
}

// WithKeepAliveTimeout 设置 KeepAlive 超时时间
func WithKeepAliveTimeout(keepAliveTimeout time.Duration) Option {
	return func(c *Client) {
		// 直接修改 c.dialer.KeepAlive
		c.dialer.KeepAlive = keepAliveTimeout
		// 重新将 dialer.DialContext 赋值给 transport.DialContext
		c.transport.DialContext = c.dialer.DialContext
	}
}

// WithTLSHandshakeTimeout 设置 TLS 握手超时时间
func WithTLSHandshakeTimeout(tlsHandshakeTimeout time.Duration) Option {
	return func(c *Client) {
		c.transport.TLSHandshakeTimeout = tlsHandshakeTimeout
	}
}

// WithExpectContinueTimeout 设置 ExpectContinue 超时时间
func WithExpectContinueTimeout(expectContinueTimeout time.Duration) Option {
	return func(c *Client) {
		c.transport.ExpectContinueTimeout = expectContinueTimeout
	}
}

// WithBufferSize 自定义缓冲池 Buffer 大小
func WithBufferSize(bufferSize int) Option {
	return func(c *Client) {
		c.bufferSize = bufferSize
	}
}

// WithMaxBufferPoolSize 自定义最大缓冲池数量
func WithMaxBufferPoolSize(maxBufferPool int) Option {
	return func(c *Client) {
		c.maxBufferPool = maxBufferPool
	}
}

// WithTimeout 设置默认请求超时时间
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		c.timeout = timeout
	}
}

// mergeTransport 将 src 的非零字段合并到 dst 中 (保持原函数不变)
func mergeTransport(dst, src *http.Transport) {
	dstVal := reflect.ValueOf(dst).Elem()
	srcVal := reflect.ValueOf(src).Elem()

	for i := 0; i < srcVal.NumField(); i++ {
		srcField := srcVal.Field(i)
		srcType := srcVal.Type().Field(i)
		if srcType.PkgPath != "" {
			continue
		}
		dstField := dstVal.FieldByName(srcType.Name)
		if !dstField.IsValid() || !dstField.CanSet() {
			continue
		}
		if !isZero(srcField) {
			dstField.Set(srcField)
		}
	}
}

// isZero 检查反射值是否为对应类型的零值 (保持原函数不变)
func isZero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		z := reflect.Zero(v.Type())
		return v.Interface() == z.Interface()
	}
}

// WithBufferPool 自定义缓冲池
func WithBufferPool(pool BufferPool) Option {
	return func(c *Client) {
		c.bufferPool = pool
	}
}

// WithRetryOptions 自定义重试策略
func WithRetryOptions(opts RetryOptions) Option {
	return func(c *Client) {
		c.retryOpts = opts
	}
}

// WithUserAgent 设置自定义 User-Agent
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		c.userAgent = ua
	}
}

// WithDumpLog 启用默认日志记录功能
func WithDumpLog() Option {
	return func(c *Client) {
		c.dumpLog = func(ctx context.Context, log string) {
			fmt.Println(log)
		}
	}
}

// WithDumpLogFunc 自定义日志记录功能
func WithDumpLogFunc(dumpLog DumpLogFunc) Option {
	return func(c *Client) {
		c.dumpLog = dumpLog
	}
}

// WithMiddleware 添加中间件
func WithMiddleware(middleware ...MiddlewareFunc) Option {
	return func(c *Client) {
		c.middlewares = append(c.middlewares, middleware...)
	}
}

// WithH2C 启用自定义 H2C (HTTP/2 Cleartext) 支持
func WithH2C() Option {
	return func(c *Client) {
		enableH2C = true // 设置全局变量启用 H2C
	}
}

// New 创建客户端实例
func New(opts ...Option) *Client {
	// 智能MaxIdleConns 设置 (保持不变)
	var maxIdleConns = defaultMaxIdleConns
	if runtime.GOMAXPROCS(0) > 4 {
		maxIdleConns = 128
	} else if runtime.GOMAXPROCS(0) != 1 {
		maxIdleConns = runtime.GOMAXPROCS(0) * 24
	} else {
		maxIdleConns = 32
	}

	// 初始化 net.Dialer 实例并存储到 Client 结构体中
	dialer := &net.Dialer{
		Timeout:   defaultDialTimeout,
		KeepAlive: defaultKeepAliveTimeout,
	}

	var proTolcols = new(http.Protocols)
	proTolcols.SetHTTP1(true)
	proTolcols.SetHTTP2(true)
	if enableH2C {
		proTolcols.SetUnencryptedHTTP2(true)
	}

	// 默认 Transport 配置
	transport := &http.Transport{
		Proxy:                  http.ProxyFromEnvironment,
		DialContext:            dialer.DialContext,
		MaxIdleConns:           maxIdleConns,
		MaxIdleConnsPerHost:    maxIdleConns / 2,
		MaxConnsPerHost:        0, // 默认为 0，表示无限制
		IdleConnTimeout:        defaultIdleConnTimeout,
		TLSHandshakeTimeout:    defaultTLSHandshakeTimeout,
		ExpectContinueTimeout:  defaultExpectContinueTimeout,
		WriteBufferSize:        32 * 1024, // 默认为 32KB
		ReadBufferSize:         32 * 1024, // 默认为 32KB
		DisableKeepAlives:      false,
		DisableCompression:     false,
		MaxResponseHeaderBytes: 0, // 默认为 0，表示无限制
		ForceAttemptHTTP2:      false,
		Protocols:              proTolcols,
	}

	c := &Client{
		client: &http.Client{
			Transport: transport,
			Timeout:   0, // 默认 Client Timeout 为 0，表示不超时，由 Request Context 控制
		},
		transport:     transport,
		retryOpts:     defaultRetryOptions(),
		bufferPool:    newDefaultPool(defaultBufferSize),
		userAgent:     defaultUserAgent,
		dumpLog:       nil, // 默认不启用日志
		maxIdleConns:  defaultMaxIdleConns,
		bufferSize:    defaultBufferSize,
		maxBufferPool: defaultMaxBufferPool,
		timeout:       0, // 默认不设置全局超时
		middlewares:   []MiddlewareFunc{},
	}

	for _, opt := range opts {
		opt(c)
		// 应用 Option 后，需要重新设置 Transport 到 Client，确保配置生效
		c.client.Transport = c.transport
		if c.timeout != 0 { // 如果设置了全局超时，则更新 Client 的 Timeout
			c.client.Timeout = c.timeout
		}
	}

	return c
}

// defaultRetryOptions 返回默认的重试策略
func defaultRetryOptions() RetryOptions {
	return RetryOptions{
		MaxAttempts:   2,
		BaseDelay:     100 * time.Millisecond,
		MaxDelay:      1 * time.Second,
		RetryStatuses: []int{429, 500, 502, 503, 504},
		Jitter:        false, // 默认不启用 Jitter
	}
}

// SetRetryOptions 动态设置重试选项
func (c *Client) SetRetryOptions(opts RetryOptions) {
	c.retryOpts = opts
}

// SetDumpLogFunc 动态设置日志记录函数
func (c *Client) SetDumpLogFunc(dumpLog DumpLogFunc) {
	c.dumpLog = dumpLog
}

// SetTimeout 动态设置客户端超时
func (c *Client) SetTimeout(timeout time.Duration) {
	c.timeout = timeout
	c.client.Timeout = timeout // 同时更新 http.Client 的 Timeout
}

// RequestBuilder 用于构建请求的结构体
type RequestBuilder struct {
	client  *Client
	method  string
	url     string
	header  http.Header
	query   url.Values
	body    io.Reader
	context context.Context
}

// NewRequestBuilder 创建 RequestBuilder 实例
func (c *Client) NewRequestBuilder(method, urlStr string) *RequestBuilder {
	return &RequestBuilder{
		client:  c,
		method:  method,
		url:     urlStr,
		header:  make(http.Header),
		query:   make(url.Values),
		context: context.Background(), // 默认使用 Background Context
	}
}

// GET, POST, PUT, DELETE 等快捷方法
func (c *Client) GET(urlStr string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodGet, urlStr)
}

func (c *Client) POST(urlStr string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodPost, urlStr)
}

func (c *Client) PUT(urlStr string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodPut, urlStr)
}

func (c *Client) DELETE(urlStr string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodDelete, urlStr)
}

func (c *Client) PATCH(urlStr string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodPatch, urlStr)
}

func (c *Client) HEAD(urlStr string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodHead, urlStr)
}

func (c *Client) OPTIONS(urlStr string) *RequestBuilder {
	return c.NewRequestBuilder(http.MethodOptions, urlStr)
}

// WithContext 设置 Context
func (rb *RequestBuilder) WithContext(ctx context.Context) *RequestBuilder {
	rb.context = ctx
	return rb
}

// SetHeader 设置 Header
func (rb *RequestBuilder) SetHeader(key, value string) *RequestBuilder {
	rb.header.Set(key, value)
	return rb
}

// AddHeader 添加 Header
func (rb *RequestBuilder) AddHeader(key, value string) *RequestBuilder {
	rb.header.Add(key, value)
	return rb
}

// SetHeaders 批量设置 Headers
func (rb *RequestBuilder) SetHeaders(headers map[string]string) *RequestBuilder {
	for key, value := range headers {
		rb.header.Set(key, value)
	}
	return rb
}

// SetQueryParam 设置 Query 参数
func (rb *RequestBuilder) SetQueryParam(key, value string) *RequestBuilder {
	rb.query.Set(key, value)
	return rb
}

// AddQueryParam 添加 Query 参数
func (rb *RequestBuilder) AddQueryParam(key, value string) *RequestBuilder {
	rb.query.Add(key, value)
	return rb
}

// SetQueryParams 批量设置 Query 参数
func (rb *RequestBuilder) SetQueryParams(params map[string]string) *RequestBuilder {
	for key, value := range params {
		rb.query.Set(key, value)
	}
	return rb
}

// SetBody 设置 Body (io.Reader)
func (rb *RequestBuilder) SetBody(body io.Reader) *RequestBuilder {
	rb.body = body
	return rb
}

// SetJSONBody 设置 JSON Body
func (rb *RequestBuilder) SetJSONBody(body interface{}) (*RequestBuilder, error) {
	buf := rb.client.bufferPool.Get()
	defer rb.client.bufferPool.Put(buf)

	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return nil, fmt.Errorf("encode json body error: %w", err)
	}
	rb.body = buf
	rb.header.Set("Content-Type", "application/json")
	return rb, nil
}

// SetXMLBody 设置 XML Body
func (rb *RequestBuilder) SetXMLBody(body interface{}) (*RequestBuilder, error) {
	buf := rb.client.bufferPool.Get()
	defer rb.client.bufferPool.Put(buf)

	if err := xml.NewEncoder(buf).Encode(body); err != nil {
		return nil, fmt.Errorf("encode xml body error: %w", err)
	}
	rb.body = buf
	rb.header.Set("Content-Type", "application/xml")
	return rb, nil
}

// SetGOBBody 设置GOB Body
func (rb *RequestBuilder) SetGOBBody(body interface{}) (*RequestBuilder, error) {
	buf := rb.client.bufferPool.Get()
	defer rb.client.bufferPool.Put(buf)

	// 使用 gob 编码
	if err := gob.NewEncoder(buf).Encode(body); err != nil {
		return nil, fmt.Errorf("encode gob body error: %w", err)
	}
	rb.body = buf
	rb.header.Set("Content-Type", "application/octet-stream") // 设置合适的 Content-Type
	return rb, nil
}

// Build 构建 http.Request
func (rb *RequestBuilder) Build() (*http.Request, error) {
	// 构建带 Query 参数的 URL
	reqURL, err := url.Parse(rb.url)
	if err != nil {
		return nil, fmt.Errorf("%w: %s, error: %v", ErrInvalidURL, rb.url, err)
	}
	if len(rb.query) > 0 {
		query := reqURL.Query()
		for k, v := range rb.query {
			for _, val := range v {
				query.Add(k, val)
			}
		}
		reqURL.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(rb.context, rb.method, reqURL.String(), rb.body)
	if err != nil {
		return nil, err
	}

	// 合并 Header，RequestBuilder 中的 Header 优先级更高
	req.Header = rb.header
	req.Header.Set("User-Agent", rb.client.userAgent) // 确保 User-Agent 被设置

	return req, nil
}

// Execute 执行请求并返回 http.Response
func (rb *RequestBuilder) Execute() (*http.Response, error) {
	req, err := rb.Build()
	if err != nil {
		return nil, err
	}

	// 应用中间件
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := rb.client.Do(r) // 调用 Client.Do 执行请求
		rb.responseWrapper(w, resp, err)
	})

	// 构建中间件链
	middlewareHandler := applyMiddlewares(handler, rb.client.middlewares...)

	// 创建 ResponseWriter 和 Request，并调用中间件链
	rw := newResponseWriter()
	middlewareHandler.ServeHTTP(rw, req)

	return rw.getResponse(), rw.getError()
}

// responseWrapper 用于包装 Client.Do 的响应和错误
func (rb *RequestBuilder) responseWrapper(w http.ResponseWriter, resp *http.Response, err error) {
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError) // 或者其他适当的错误状态码
		w.Write([]byte(err.Error()))                  // 将错误信息写入 ResponseWriter
		return
	}
	// 将 Client.Do 返回的响应复制到 ResponseWriter
	copyResponse(w, resp)
}

// copyResponse 将 http.Response 复制到 http.ResponseWriter
func copyResponse(w http.ResponseWriter, resp *http.Response) {
	if resp == nil {
		return
	}
	defer resp.Body.Close()

	// 复制 Header
	header := w.Header()
	for key, values := range resp.Header {
		for _, value := range values {
			header.Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// 复制 Body
	if _, err := copyb.CopyBuffer(w, resp.Body, nil); err != nil {
		// 复制 Body 失败，记录日志或处理错误
		fmt.Printf("Error copying response body: %v\n", err) // 示例错误处理
	}
}

// ResponseWriter 包装 http.ResponseWriter 和错误信息
type ResponseWriter struct {
	http.ResponseWriter
	response *http.Response
	err      error
}

// newResponseWriter 创建 ResponseWriter 实例
func newResponseWriter() *ResponseWriter {
	return &ResponseWriter{
		ResponseWriter: &noopResponseWriter{}, // 使用 noopResponseWriter 作为默认值
	}
}

// Header 实现 http.ResponseWriter 接口的 Header 方法
func (rw *ResponseWriter) Header() http.Header {
	if rw.response != nil {
		return rw.response.Header
	}
	return make(http.Header) // 如果 response 为 nil，返回一个新的 Header
}

// WriteHeader 实现 http.ResponseWriter 接口的 WriteHeader 方法
func (rw *ResponseWriter) WriteHeader(statusCode int) {
	if rw.response == nil {
		rw.response = &http.Response{
			Header:     make(http.Header),
			StatusCode: statusCode,
		}
	} else {
		rw.response.StatusCode = statusCode
	}
}

// Write 实现 http.ResponseWriter 接口的 Write 方法
func (rw *ResponseWriter) Write(p []byte) (int, error) {
	if rw.response == nil {
		rw.response = &http.Response{
			Header: make(http.Header),
			Body:   io.NopCloser(bytes.NewReader(p)), // 使用 bytes.NewReader 和 NopCloser
		}
	} else if rw.response.Body == nil {
		rw.response.Body = io.NopCloser(bytes.NewReader(p))
	} else {
		// 如果 Body 已经存在，则追加内容 (需要更高效的实现，这里仅为示例)
		currentBody, _ := io.ReadAll(rw.response.Body)
		newBody := append(currentBody, p...)
		rw.response.Body = io.NopCloser(bytes.NewReader(newBody))
	}
	return len(p), nil
}

// noopResponseWriter 实现 http.ResponseWriter 接口，但不执行任何操作
type noopResponseWriter struct {
	header http.Header
}

func (w *noopResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *noopResponseWriter) WriteHeader(statusCode int) {}

func (w *noopResponseWriter) Write(p []byte) (int, error) {
	return len(p), nil
}

// getResponse 获取 http.Response
func (rw *ResponseWriter) getResponse() *http.Response {
	return rw.response
}

// getError 获取错误信息
func (rw *ResponseWriter) getError() error {
	return rw.err
}

// MiddlewareFunc 定义中间件函数类型
type MiddlewareFunc func(next http.Handler) http.Handler

// ApplyMiddlewares 应用中间件链
func applyMiddlewares(handler http.Handler, middlewares ...MiddlewareFunc) http.Handler {
	for i := range middlewares {
		handler = middlewares[len(middlewares)-1-i](handler) // 逆序应用中间件
	}
	return handler
}

// 实现标准库兼容接口 (保持原函数不变，但使用 RequestBuilder 重构)
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.ProtoMajor == 2 {
		if req.Header.Get("Connection") == "Upgrade" && req.Header.Get("Upgrade") != "" {
			req.Header.Del("Connection")
			req.Header.Del("Upgrade")
		}
	}

	// 记录日志
	c.logRequest(req)

	// 执行中间件链和重试逻辑
	return c.doWithRetry(req)
}

// 记录请求日志 (保持原函数不变)
func (c *Client) logRequest(req *http.Request) {
	if c.dumpLog == nil {
		return
	}

	transportDetails := getTransportDetails(c.transport)

	logContent := fmt.Sprintf(`
[HTTP Request Log]
-------------------------------
Time       : %s
Method     : %s
URL        : %s
Host       : %s
Protocol   : %s
Transport  :
%v
Headers    :
%v
-------------------------------
`,
		time.Now().Format("2006-01-02 15:04:05"),
		req.Method,
		req.URL.String(),
		req.URL.Host,
		req.Proto,
		transportDetails,
		formatHeaders(req.Header),
	)

	c.dumpLog(req.Context(), logContent)
}

// 获取 Transport 的详细信息 (保持原函数不变)
func getTransportDetails(transport http.RoundTripper) string {
	if t, ok := transport.(*http.Transport); ok {
		return fmt.Sprintf(`  Type                 : *http.Transport
  MaxIdleConns         : %d
  MaxIdleConnsPerHost  : %d
  MaxConnsPerHost      : %d
  IdleConnTimeout      : %s
  TLSHandshakeTimeout  : %s
  DisableKeepAlives    : %v
  WriteBufferSize      : %d
  ReadBufferSize       : %d
`,
			t.MaxIdleConns,
			t.MaxIdleConnsPerHost,
			t.MaxConnsPerHost,
			t.IdleConnTimeout,
			t.TLSHandshakeTimeout,
			t.DisableKeepAlives,
			t.WriteBufferSize,
			t.ReadBufferSize,
		)
	}

	if transport != nil {
		return fmt.Sprintf("  Type                 : %T", transport)
	}

	return "  Type                 : nil"
}

// 格式化请求头为多行字符串
func formatHeaders(headers http.Header) string {
	var builder strings.Builder
	for key, values := range headers {
		builder.WriteString(fmt.Sprintf("  %s: %s\n", key, strings.Join(values, ", ")))
	}
	return builder.String()
}

/*
// 高性能 BufferCopy 实现
func (c *Client) bufferCopy(dst io.Writer, src io.Reader) (written int64, err error) {
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	for {
		nr, er := src.Read(buf.Bytes()[:c.bufferSize]) // 使用配置的 bufferSize
		if nr > 0 {
			nw, ew := dst.Write(buf.Bytes()[0:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return
}
*/
// 高性能 BufferCopy 实现
func (c *Client) bufferCopy(dst io.Writer, src io.Reader) (written int64, err error) {
	written, err = copyb.CopyBuffer(dst, src, nil)
	return
}

func (c *Client) doWithRetry(req *http.Request) (*http.Response, error) {
	var (
		resp *http.Response
		err  error
	)

	for attempt := 0; attempt <= c.retryOpts.MaxAttempts; attempt++ {
		resp, err = c.client.Do(req) // 注意这里调用的是 http.Client.Do

		if c.shouldRetry(resp, err) {
			if attempt < c.retryOpts.MaxAttempts {
				var delay time.Duration
				if resp != nil && resp.StatusCode == 429 {
					delay = c.calculateRetryAfter(resp)
				} else {
					delay = c.calculateExponentialBackoff(attempt, c.retryOpts.Jitter) // 传递 Jitter 参数
				}
				time.Sleep(delay)
				continue
			}
			return nil, ErrMaxRetriesExceeded
		}
		break
	}

	if err != nil {
		return nil, c.wrapError(err)
	}

	return resp, nil
}

// 解析 Retry-After 头部，仅在状态码为 429 时调用 (保持原函数不变)
func (c *Client) calculateRetryAfter(resp *http.Response) time.Duration {
	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter != "" {
		if delay, err := parseRetryAfter(retryAfter); err == nil {
			return delay
		}
	}
	return c.retryOpts.BaseDelay
}

// 解析 Retry-After 的具体实现 (保持原函数不变)
func parseRetryAfter(retryAfter string) (time.Duration, error) {
	if seconds, err := time.ParseDuration(retryAfter + "s"); err == nil {
		return seconds, nil
	}

	if retryTime, err := http.ParseTime(retryAfter); err == nil {
		delay := time.Until(retryTime)
		if delay > 0 {
			return delay, nil
		}
	}

	return 0, errors.New("invalid Retry-After value")
}

// 指数退避计算 (修改为支持 Jitter)
func (c *Client) calculateExponentialBackoff(attempt int, jitter bool) time.Duration {
	delay := c.retryOpts.BaseDelay * time.Duration(1<<uint(attempt))
	if delay > c.retryOpts.MaxDelay {
		delay = c.retryOpts.MaxDelay
	}

	if jitter {
		// 添加 Jitter 抖动，防止 thundering herd 问题
		randomFactor := 0.8 + 0.4*float64(attempt) // 随着重试次数增加，抖动范围略微扩大
		delay = time.Duration(float64(delay) * randomFactor)
	}
	return delay
}

// 错误包装 (保持原函数不变)
func (c *Client) wrapError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %v", ErrRequestTimeout, err)
	default:
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return fmt.Errorf("%w: %v", ErrRequestTimeout, err)
		}
		return err
	}
}

// 重试条件判断 (保持原函数不变)
func (c *Client) shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return isNetworkError(err)
	}

	for _, status := range c.retryOpts.RetryStatuses {
		if resp != nil && resp.StatusCode == status { // 增加 resp != nil 判断
			return true
		}
	}
	return false
}

// 辅助函数 (保持原函数不变)
func isNetworkError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr)
}

// --- 响应处理方法 (使用 RequestBuilder 重构) ---

// DecodeJSON 解析 JSON 响应
func (rb *RequestBuilder) DecodeJSON(v interface{}) error {
	resp, err := rb.Execute()
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return rb.client.decodeJSONResponse(resp, v)
}

// DecodeXML 解析 XML 响应
func (rb *RequestBuilder) DecodeXML(v interface{}) error {
	resp, err := rb.Execute()
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return rb.client.decodeXMLResponse(resp, v)
}

// DecodeGOB 解析 GOB 响应
func (rb *RequestBuilder) DecodeGOB(v interface{}) error {
	resp, err := rb.Execute()
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return rb.client.decodeGOBResponse(resp, v)
}

// Text 获取 Text 响应
func (rb *RequestBuilder) Text() (string, error) {
	resp, err := rb.Execute()
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return rb.client.decodeTextResponse(resp)
}

// Bytes 获取 Bytes 响应
func (rb *RequestBuilder) Bytes() ([]byte, error) {
	resp, err := rb.Execute()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return rb.client.decodeBytesResponse(resp)
}

// decodeJSONResponse 内部 JSON 响应解码
func (c *Client) decodeJSONResponse(resp *http.Response, v interface{}) error {
	if resp.StatusCode >= 400 {
		return c.errorResponse(resp)
	}

	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	_, err := c.bufferCopy(buf, resp.Body)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDecodeResponse, err)
	}

	if err := json.Unmarshal(buf.Bytes(), v); err != nil {
		return fmt.Errorf("%w: %v, raw body: %s", ErrDecodeResponse, err, buf.String()) // 包含原始 body
	}
	return nil
}

// decodeXMLResponse 内部 XML 响应解码
func (c *Client) decodeXMLResponse(resp *http.Response, v interface{}) error {
	if resp.StatusCode >= 400 {
		return c.errorResponse(resp)
	}

	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	_, err := c.bufferCopy(buf, resp.Body)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDecodeResponse, err)
	}

	if err := xml.Unmarshal(buf.Bytes(), v); err != nil {
		return fmt.Errorf("%w: %v, raw body: %s", ErrDecodeResponse, err, buf.String()) // 包含原始 body
	}
	return nil
}

// decodeGOBResponse 内部 GOB 响应解码
func (c *Client) decodeGOBResponse(resp *http.Response, v interface{}) error {
	if resp.StatusCode >= 400 {
		return c.errorResponse(resp)
	}

	// 使用 gob 解码
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	_, err := c.bufferCopy(buf, resp.Body)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDecodeResponse, err)
	}
	if err := gob.NewDecoder(buf).Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: unexpected end of data: %v", ErrDecodeResponse, err)
		}
		return fmt.Errorf("%w: %v, raw body: %s", ErrDecodeResponse, err, buf.String()) // 包含原始 body
	}
	return nil
}

// decodeTextResponse 内部 Text 响应解码
func (c *Client) decodeTextResponse(resp *http.Response) (string, error) {
	if resp.StatusCode >= 400 {
		return "", c.errorResponse(resp)
	}
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	_, err := c.bufferCopy(buf, resp.Body)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDecodeResponse, err)
	}
	return buf.String(), nil
}

// decodeBytesResponse 内部 Bytes 响应解码
func (c *Client) decodeBytesResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode >= 400 {
		return nil, c.errorResponse(resp)
	}
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	_, err := c.bufferCopy(buf, resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecodeResponse, err)
	}
	return buf.Bytes(), nil
}

// errorResponse 处理错误响应，返回包含状态码和 Body 的错误
func (c *Client) errorResponse(resp *http.Response) error {
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	_, err := c.bufferCopy(buf, resp.Body)
	if err != nil {
		return fmt.Errorf("HTTP %d, failed to read error body: %w", resp.StatusCode, err)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, buf.String())
}

// --- 标准库兼容方法 (使用 RequestBuilder 重构) ---

// NewRequest 创建请求，支持与 http.NewRequest 兼容
func (c *Client) NewRequest(method, urlStr string, body io.Reader) (*http.Request, error) {
	builder := c.NewRequestBuilder(method, urlStr).SetBody(body)
	return builder.Build()
}

// Get 发送 GET 请求
func (c *Client) Get(url string) (*http.Response, error) {
	return c.GET(url).Execute()
}

// GetContext 发送带 Context 的 GET 请求
func (c *Client) GetContext(ctx context.Context, url string) (*http.Response, error) {
	return c.GET(url).WithContext(ctx).Execute()
}

// PostJSON 发送 JSON POST 请求
func (c *Client) PostJSON(ctx context.Context, url string, body interface{}) (*http.Response, error) {
	builder := c.POST(url)
	_, err := builder.SetJSONBody(body)
	if err != nil {
		return nil, err
	}
	return builder.WithContext(ctx).Execute()
}

// PostXML 发送 XML POST 请求
func (c *Client) PostXML(ctx context.Context, url string, body interface{}) (*http.Response, error) {
	builder := c.POST(url)
	_, err := builder.SetXMLBody(body)
	if err != nil {
		return nil, err
	}
	return builder.WithContext(ctx).Execute()
}

// PostGOB 发送 GOB POST 请求
func (c *Client) PostGOB(ctx context.Context, url string, body interface{}) (*http.Response, error) {
	builder := c.POST(url)
	_, err := builder.SetGOBBody(body)
	if err != nil {
		return nil, err
	}
	return builder.WithContext(ctx).Execute()
}

// PutJSON 发送 JSON PUT 请求
func (c *Client) PutJSON(ctx context.Context, url string, body interface{}) (*http.Response, error) {
	builder := c.PUT(url)
	_, err := builder.SetJSONBody(body)
	if err != nil {
		return nil, err
	}
	return builder.WithContext(ctx).Execute()
}

// PutXML 发送 XML PUT 请求
func (c *Client) PutXML(ctx context.Context, url string, body interface{}) (*http.Response, error) {
	builder := c.PUT(url)
	_, err := builder.SetXMLBody(body)
	if err != nil {
		return nil, err
	}
	return builder.WithContext(ctx).Execute()
}

// PutGOB 发送 GOB PUT 请求
func (c *Client) PutGOB(ctx context.Context, url string, body interface{}) (*http.Response, error) {
	builder := c.PUT(url)
	_, err := builder.SetGOBBody(body)
	if err != nil {
		return nil, err
	}
	return builder.WithContext(ctx).Execute()
}

// Post 发送 POST 请求
func (c *Client) Post(ctx context.Context, url string, body io.Reader) (*http.Response, error) {
	return c.POST(url).SetBody(body).WithContext(ctx).Execute()
}

// Put 发送 PUT 请求
func (c *Client) Put(ctx context.Context, url string, body io.Reader) (*http.Response, error) {
	return c.PUT(url).SetBody(body).WithContext(ctx).Execute()
}

// Delete 发送 DELETE 请求
func (c *Client) Delete(ctx context.Context, url string) (*http.Response, error) {
	return c.DELETE(url).WithContext(ctx).Execute()
}
