package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloadSourceSelector_CheckNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	selector := &DownloadSourceSelector{
		log:           slog.Default(),
		networkURL:    server.URL,
		networkClient: server.Client(),
	}
	if err := selector.CheckNetwork(context.Background()); err != nil {
		t.Fatalf("CheckNetwork() unexpected error: %v", err)
	}

	selector.networkURL = server.URL + "/unavailable"
	selector.networkClient = &http.Client{}
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if err := selector.CheckNetwork(context.Background()); !errors.Is(err, ErrNetworkUnavailable) {
		t.Fatalf("CheckNetwork() error = %v, want ErrNetworkUnavailable", err)
	}
}

func TestDownloadSourceSelector_Select(t *testing.T) {
	// 创建临时目录用于缓存
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建日志记录器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// 创建选择器
	selector := NewDownloadSourceSelector(tempDir, log)

	// 测试选择下载源
	ctx := context.Background()
	source := selector.Select(ctx)

	// 验证结果
	if source == "" {
		t.Error("下载源不能为空")
	}

	t.Logf("选择的下载源: %s", source)

	// 再次调用，应该返回缓存结果
	source2 := selector.Select(ctx)
	if source != source2 {
		t.Errorf("第二次调用应该返回缓存结果，期望: %s, 实际: %s", source, source2)
	}
}

func TestDownloadSourceSelector_detectCountryByIPChina(t *testing.T) {
	// 创建测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "country.is") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(IPInfo{
				IP:      "114.253.245.172",
				Country: CountryCN,
			})
		} else if strings.Contains(r.URL.Path, "ip9.com.cn") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ret": 200,
				"data": map[string]interface{}{
					"ip":           "114.253.245.172",
					"country_code": "cn",
				},
			})
		}
	}))
	defer server.Close()

	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 测试国内接口检测
	ctx := context.Background()
	country, ip, err := selector.detectCountryByIPChina(ctx)
	if err != nil {
		t.Errorf("检测国家失败: %v", err)
	} else {
		t.Logf("检测到国家: %s, IP: %s", country, ip)
		if country != CountryCN {
			t.Errorf("期望国家: %s, 实际: %s", CountryCN, country)
		}
	}
}

func TestDownloadSourceSelector_detectCountryByIPForeign(t *testing.T) {
	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 测试国外接口检测
	ctx := context.Background()
	country, ip, err := selector.detectCountryByIPForeign(ctx)
	if err != nil {
		t.Logf("检测国家失败（这是正常的，因为国外 API 可能不可用）: %v", err)
	} else {
		t.Logf("检测到国家: %s, IP: %s", country, ip)
	}
}

func TestDownloadSourceSelector_detectCountryByOS(t *testing.T) {
	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 测试操作系统检测
	country, err := selector.detectCountryByOS()
	if err != nil {
		t.Logf("操作系统检测失败（这是正常的，因为 PowerShell 可能不可用）: %v", err)
	} else {
		t.Logf("检测到国家: %s", country)
	}
}

func TestDownloadSourceSelector_checkURL(t *testing.T) {
	// 创建测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 测试 URL 连接检测
	ctx := context.Background()
	result := selector.checkURL(ctx, server.URL)
	if !result {
		t.Error("URL 连接检测失败")
	}

	// 测试无效 URL
	result = selector.checkURL(ctx, "http://invalid-url-12345.com")
	if result {
		t.Error("无效 URL 应该返回 false")
	}
}

func TestDownloadSourceSelector_cacheResult(t *testing.T) {
	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 测试缓存结果
	result := &DetectionResult{
		Source:  DownloadSourceModelScope,
		Country: CountryCN,
		IP:      "114.253.245.172",
	}

	err = selector.cacheResult(result)
	if err != nil {
		t.Errorf("缓存结果失败: %v", err)
	}

	// 测试加载缓存
	cachedResult, err := selector.loadCachedResult()
	if err != nil {
		t.Errorf("加载缓存失败: %v", err)
	} else {
		if cachedResult.Source != DownloadSourceModelScope {
			t.Errorf("期望源: %s, 实际: %s", DownloadSourceModelScope, cachedResult.Source)
		}
		if cachedResult.Country != CountryCN {
			t.Errorf("期望国家: %s, 实际: %s", CountryCN, cachedResult.Country)
		}
		if cachedResult.IP != "114.253.245.172" {
			t.Errorf("期望 IP: 114.253.245.172, 实际: %s", cachedResult.IP)
		}
	}
}

func TestDownloadSourceSelector_Reset(t *testing.T) {
	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 设置检测结果
	selector.mu.Lock()
	selector.detectionResult = &DetectionResult{
		Source:  DownloadSourceModelScope,
		Country: CountryCN,
		IP:      "114.253.245.172",
	}
	selector.mu.Unlock()

	// 重置
	selector.Reset()

	// 验证重置结果
	result := selector.GetDetectionResult()
	if result != nil {
		t.Error("重置后检测结果应该为 nil")
	}
}

func TestDownloadSourceSelector_GetDetectionResult(t *testing.T) {
	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 设置检测结果
	selector.mu.Lock()
	selector.detectionResult = &DetectionResult{
		Source:  DownloadSourceModelScope,
		Country: CountryCN,
		IP:      "114.253.245.172",
	}
	selector.mu.Unlock()

	// 获取检测结果
	result := selector.GetDetectionResult()
	if result == nil {
		t.Error("检测结果不能为 nil")
	} else {
		if result.Source != DownloadSourceModelScope {
			t.Errorf("期望源: %s, 实际: %s", DownloadSourceModelScope, result.Source)
		}
		if result.Country != CountryCN {
			t.Errorf("期望国家: %s, 实际: %s", CountryCN, result.Country)
		}
		if result.IP != "114.253.245.172" {
			t.Errorf("期望 IP: 114.253.245.172, 实际: %s", result.IP)
		}
	}
}

func TestDownloadSourceSelector_ContextTimeout(t *testing.T) {
	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 创建超时上下文
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// 测试超时情况
	country, ip, err := selector.detectCountryByIP(ctx)
	if err == nil {
		t.Error("超时应该返回错误")
	} else {
		t.Logf("超时检测返回错误（这是预期的）: %v", err)
	}

	if country != "" {
		t.Errorf("超时后国家应该为空，实际: %s", country)
	}

	if ip != "" {
		t.Errorf("超时后 IP 应该为空，实际: %s", ip)
	}
}

func TestDownloadSourceSelector_CacheFile(t *testing.T) {
	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 缓存结果
	result := &DetectionResult{
		Source:  DownloadSourceHuggingFace,
		Country: CountryUS,
		IP:      "66.11.117.89",
	}

	err = selector.cacheResult(result)
	if err != nil {
		t.Errorf("缓存结果失败: %v", err)
	}

	// 验证缓存文件是否存在
	cacheFile := filepath.Join(tempDir, "download_source_cache.json")
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Error("缓存文件不存在")
	}

	// 读取缓存文件内容
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Errorf("读取缓存文件失败: %v", err)
	}

	// 验证缓存文件内容
	var cachedResult CacheResult
	if err := json.Unmarshal(data, &cachedResult); err != nil {
		t.Errorf("解析缓存文件失败: %v", err)
	}

	if cachedResult.Source != DownloadSourceHuggingFace {
		t.Errorf("期望源: %s, 实际: %s", DownloadSourceHuggingFace, cachedResult.Source)
	}

	if cachedResult.Country != CountryUS {
		t.Errorf("期望国家: %s, 实际: %s", CountryUS, cachedResult.Country)
	}

	if cachedResult.IP != "66.11.117.89" {
		t.Errorf("期望 IP: 66.11.117.89, 实际: %s", cachedResult.IP)
	}

	// 验证时间戳
	if cachedResult.LastChecked.IsZero() {
		t.Error("缓存时间戳不能为零")
	}
}

func TestDownloadSourceSelector_LoadNonExistentCache(t *testing.T) {
	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 尝试加载不存在的缓存
	_, err = selector.loadCachedResult()
	if err == nil {
		t.Error("加载不存在的缓存应该返回错误")
	} else {
		t.Logf("加载不存在的缓存返回错误（这是预期的）: %v", err)
	}
}

func TestDownloadSourceSelector_ConcurrentAccess(t *testing.T) {
	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "download_source_test")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// 创建选择器
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	selector := NewDownloadSourceSelector(tempDir, log)

	// 并发访问
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			ctx := context.Background()
			source := selector.Select(ctx)
			t.Logf("并发访问选择的下载源: %s", source)
			done <- true
		}()
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 10; i++ {
		<-done
	}

	// 验证结果
	result := selector.GetDetectionResult()
	if result == nil {
		t.Error("检测结果不能为 nil")
	}
}
