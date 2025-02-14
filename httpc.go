package httpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// 错误定义
var (
	ErrRequestTimeout     = errors.New("request timeout")
	ErrMaxRetriesExceeded = errors.New("max retries exceeded")
	ErrBufferPoolEmpty    = errors.New("buffer pool exhausted")
)

// 缓冲池配置
const (
	bufferSize    = 32 << 10 // 32KB
	maxBufferPool = 100
)

var bufferPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, bufferSize))
	},
}

// Client 主客户端结构
type Client struct {
	client     *http.Client
	transport  *http.Transport
	retryOpts  RetryOptions
	bufferPool BufferPool
}

// RetryOptions 重试配置
type RetryOptions struct {
	MaxAttempts   int
	BaseDelay     time.Duration
	MaxDelay      time.Duration
	RetryStatuses []int
}

// BufferPool 缓冲池接口
type BufferPool interface {
	Get() *bytes.Buffer
	Put(*bytes.Buffer)
}

// 默认缓冲池实现
type defaultPool struct{}

func (p *defaultPool) Get() *bytes.Buffer {
	buf := bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

func (p *defaultPool) Put(buf *bytes.Buffer) {
	if buf.Cap() > bufferSize*2 {
		return // 防止内存泄漏
	}
	bufferPool.Put(buf)
}

// New 创建客户端实例
func New(opts ...Option) *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   runtime.GOMAXPROCS(0) * 2,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	c := &Client{
		client: &http.Client{
			Transport: transport,
		},
		transport: transport,
		retryOpts: RetryOptions{
			MaxAttempts:   2,
			BaseDelay:     100 * time.Millisecond,
			MaxDelay:      1 * time.Second,
			RetryStatuses: []int{429, 500, 502, 503, 504},
		},
		bufferPool: &defaultPool{},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Option 配置选项类型
type Option func(*Client)

// WithTransport 自定义Transport
func WithTransport(t *http.Transport) Option {
	return func(c *Client) {
		c.transport = t
		c.client.Transport = t
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

// 实现标准库兼容接口
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	return c.doWithRetry(req)
}

// 高性能BufferCopy实现
func (c *Client) bufferCopy(dst io.Writer, src io.Reader) (written int64, err error) {
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	for {
		nr, er := src.Read(buf.Bytes()[:bufferSize])
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

// 带指数退避的重试逻辑
func (c *Client) doWithRetry(req *http.Request) (*http.Response, error) {
	var (
		resp *http.Response
		err  error
	)

	for attempt := 0; attempt <= c.retryOpts.MaxAttempts; attempt++ {
		resp, err = c.client.Do(req)
		if c.shouldRetry(resp, err) {
			if attempt < c.retryOpts.MaxAttempts {
				delay := c.calculateDelay(attempt)
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

// 错误包装
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

// 重试条件判断
func (c *Client) shouldRetry(resp *http.Response, err error) bool {
	if err != nil {
		return isNetworkError(err)
	}

	for _, status := range c.retryOpts.RetryStatuses {
		if resp.StatusCode == status {
			return true
		}
	}
	return false
}

// 指数退避计算
func (c *Client) calculateDelay(attempt int) time.Duration {
	delay := c.retryOpts.BaseDelay * time.Duration(1<<uint(attempt))
	if delay > c.retryOpts.MaxDelay {
		return c.retryOpts.MaxDelay
	}
	return delay
}

// JSON响应处理（使用缓冲池）
func (c *Client) DecodeJSON(resp *http.Response, v interface{}) error {
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		buf := c.bufferPool.Get()
		defer c.bufferPool.Put(buf)

		_, err := c.bufferCopy(buf, resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read error body: %w", err)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, buf.String())
	}

	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	_, err := c.bufferCopy(buf, resp.Body)
	if err != nil {
		return fmt.Errorf("%v", err)
	}

	return json.Unmarshal(buf.Bytes(), v)
}

// 辅助函数
func isNetworkError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr)
}

// 标准库兼容方法
func (c *Client) Get(url string) (*http.Response, error) {
	return c.GetContext(context.Background(), url)
}

func (c *Client) GetContext(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// 高级POST方法
func (c *Client) PostJSON(ctx context.Context, url string, body interface{}) (*http.Response, error) {
	buf := c.bufferPool.Get()
	defer c.bufferPool.Put(buf)

	if err := json.NewEncoder(buf).Encode(body); err != nil {
		return nil, fmt.Errorf("encode error: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	return c.Do(req)
}
