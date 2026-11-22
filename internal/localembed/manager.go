// Copyright (c) 2025-now SuInk.
// Licensed under the Limited Redistribution License in the repository root.

package localembed

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// 模型权重不打进程序：五个平台的包都会多 95MB，而大多数人不开语义检索。
// 第一次需要时从下面的地址下载到数据目录，按固定的 SHA-256 校验，之后离线可用。
// config.json 和 vocab.txt 很小，直接编进来，下载只拿一个 safetensors。
//
// 两个来源内容相同：Hugging Face 钉在固定提交上，ModelScope 是国内能直连的镜像。
// 哪个先通用哪个；校验和不对就换下一个，不会把坏文件留在磁盘上。

// ModelName 是存进向量表的模型名。换模型或换权重都要改它，旧向量才会自然失效。
const ModelName = "builtin:bge-small-zh-v1.5"

const (
	weightsFileName = "bge-small-zh-v1.5.safetensors"
	weightsSHA256   = "354763b9b1357bc9c44f62c6be2276321081ed2567773608c0d0785b61d5a026"
	weightsSize     = 95827648
	// 下载中途超过这么久一个字节都没收到就换下一个来源。被墙的地址往往不是立刻
	// 失败，而是连上后一直不给数据。
	downloadStallTimeout = 30 * time.Second
	retryAfterFailure    = 10 * time.Minute
)

var weightSources = []string{
	"https://huggingface.co/BAAI/bge-small-zh-v1.5/resolve/7999e1d3359715c523056ef9478215996d62a620/model.safetensors",
	"https://www.modelscope.cn/models/BAAI/bge-small-zh-v1.5/resolve/master/model.safetensors",
}

//go:embed assets/config.json
var configJSON []byte

//go:embed assets/vocab.txt
var vocabText []byte

// State 是内置模型当前所处的阶段。
type State string

const (
	StateIdle        State = "idle"
	StateDownloading State = "downloading"
	StateLoading     State = "loading"
	StateReady       State = "ready"
	StateFailed      State = "failed"
)

// Status 给界面展示下载进度和失败原因。
type Status struct {
	State      State  `json:"state"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
}

// Manager 负责按需下载、校验、加载内置模型。零值不可用，用 NewManager。
type Manager struct {
	dir     string
	client  *http.Client
	sources []string
	sha256  string
	size    int64
	config  []byte
	vocab   []byte

	mu         sync.Mutex
	state      State
	err        error
	failedAt   time.Time
	model      *Model
	downloaded atomic.Int64
}

// NewManager 把模型放在 dir 下。client 为 nil 时用带代理环境变量的默认客户端。
func NewManager(dir string, client *http.Client) *Manager {
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	}
	return &Manager{
		dir: dir, client: client, sources: weightSources, sha256: weightsSHA256, size: weightsSize,
		config: configJSON, vocab: vocabText, state: StateIdle,
	}
}

// Ready 报告模型能不能马上用。没准备好时顺手在后台开始准备，调用方不必等。
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.state {
	case StateReady:
		return true
	case StateDownloading, StateLoading:
		return false
	case StateFailed:
		if time.Since(m.failedAt) < retryAfterFailure {
			return false
		}
	}
	m.state = StateLoading
	m.err = nil
	go m.prepare()
	return false
}

// Status 返回当前状态的快照。
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := Status{State: m.state, Downloaded: m.downloaded.Load(), Total: m.size}
	if m.err != nil {
		status.Error = m.err.Error()
	}
	return status
}

// Embed 用内置模型算向量。模型还没就绪时返回错误，不会阻塞等下载。
func (m *Manager) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	m.mu.Lock()
	model := m.model
	m.mu.Unlock()
	if model == nil {
		return nil, errors.New("localembed: built-in model is not ready")
	}
	return model.Embed(ctx, texts)
}

func (m *Manager) prepare() {
	model, err := m.loadOrDownload()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		log.Printf("built-in embedding model unavailable: %v", err)
		m.state = StateFailed
		m.err = err
		m.failedAt = time.Now()
		return
	}
	m.model = model
	m.state = StateReady
}

func (m *Manager) loadOrDownload() (*Model, error) {
	path := filepath.Join(m.dir, weightsFileName)
	if err := verifyFile(path, m.sha256); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(path)
		}
		m.setState(StateDownloading)
		if err := m.download(path); err != nil {
			return nil, err
		}
		m.setState(StateLoading)
	}
	return load(m.config, m.vocab, path)
}

func (m *Manager) setState(state State) {
	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
}

func (m *Manager) download(path string) error {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	var errs []error
	for _, source := range m.sources {
		err := m.downloadFrom(source, path)
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", source, err))
	}
	return fmt.Errorf("download built-in embedding model: %w", errors.Join(errs...))
}

func (m *Manager) downloadFrom(source, path string) error {
	m.downloaded.Store(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	stall := time.AfterFunc(downloadStallTimeout, cancel)
	defer stall.Stop()
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(m.dir, weightsFileName+".*.part")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	hash := sha256.New()
	buffer := make([]byte, 256<<10)
	var written int64
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			stall.Reset(downloadStallTimeout)
			written += int64(n)
			if m.size > 0 && written > m.size {
				_ = tmp.Close()
				return errors.New("response larger than expected")
			}
			hash.Write(buffer[:n])
			if _, err := tmp.Write(buffer[:n]); err != nil {
				_ = tmp.Close()
				return err
			}
			m.downloaded.Store(written)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = tmp.Close()
			return readErr
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != m.sha256 {
		return fmt.Errorf("sha256 mismatch: got %s", got)
	}
	return os.Rename(tmpPath, path)
}

func verifyFile(path, want string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("sha256 mismatch: got %s", got)
	}
	return nil
}
