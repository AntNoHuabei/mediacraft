package downloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Xuanwo/go-locale"
)

// ErrNetworkUnavailable 表示当前无法访问网络。
var ErrNetworkUnavailable = errors.New("network unavailable")

// NetworkUnavailableError 保留网络探测失败的底层原因。
type NetworkUnavailableError struct {
	Cause error
}

func (e *NetworkUnavailableError) Error() string { return "network unavailable: " + e.Cause.Error() }
func (e *NetworkUnavailableError) Unwrap() error { return ErrNetworkUnavailable }

// CheckNetwork 检查至少一个模型下载源是否可访问，不使用下载源缓存。
func (s *DownloadSourceSelector) CheckNetwork(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	urls := []string{"https://www.modelscope.cn", "https://huggingface.co"}
	if s.networkURL != "" {
		urls = []string{s.networkURL}
	}
	for _, url := range urls {
		if s.checkURL(checkCtx, url) {
			return nil
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return &NetworkUnavailableError{Cause: errors.New("model download sources are unreachable")}
}

// DownloadSource 下载源类型
type DownloadSource string

const (
	// DownloadSourceModelScope ModelScope 下载源
	DownloadSourceModelScope DownloadSource = "modelscope"
	// DownloadSourceHuggingFace HuggingFace 下载源
	DownloadSourceHuggingFace DownloadSource = "huggingface"
	// DownloadSourceNone 无可用下载源
	DownloadSourceNone DownloadSource = "none"
)

// Country 国家代码
type Country string

const (
	// CountryCN 中国
	CountryCN Country = "CN"
	// CountryUS 美国
	CountryUS Country = "US"
)

// IPInfo IP 信息
type IPInfo struct {
	IP      string  `json:"ip"`
	Country Country `json:"country"`
}

// CacheResult 缓存结果
type CacheResult struct {
	Source      DownloadSource `json:"source"`
	Country     Country        `json:"country"`
	IP          string         `json:"ip"`
	LastChecked time.Time      `json:"last_checked"`
}

// DownloadSourceSelector 下载源选择器
type DownloadSourceSelector struct {
	log             *slog.Logger
	cacheFile       string
	cachedResult    *CacheResult
	once            sync.Once
	detectionResult *DetectionResult
	mu              sync.RWMutex
	networkURL      string
	networkClient   *http.Client
}

// DetectionResult 检测结果
type DetectionResult struct {
	Source  DownloadSource `json:"source"`
	Country Country        `json:"country"`
	IP      string         `json:"ip"`
}

// NewDownloadSourceSelector 创建下载源选择器
func NewDownloadSourceSelector(cacheDir string, log *slog.Logger) *DownloadSourceSelector {
	return &DownloadSourceSelector{
		log:       log,
		cacheFile: filepath.Join(cacheDir, "download_source_cache.json"),
	}
}

// Select 选择下载源（仅第一次调用时执行检测，后续返回缓存结果）
func (s *DownloadSourceSelector) Select(ctx context.Context) DownloadSource {
	s.once.Do(func() {
		result := s.detectDownloadSource(ctx)
		s.mu.Lock()
		s.detectionResult = result
		s.mu.Unlock()

		// 缓存结果
		if err := s.cacheResult(result); err != nil {
			s.log.Warn("缓存下载源检测结果失败", slog.Any("error", err))
		}
	})

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.detectionResult != nil {
		return s.detectionResult.Source
	}

	return DownloadSourceNone
}

// detectDownloadSource 检测下载源
func (s *DownloadSourceSelector) detectDownloadSource(ctx context.Context) *DetectionResult {
	// 1. 尝试通过 IP 检测国家
	country, ip, err := s.detectCountryByIP(ctx)
	if err == nil {
		s.log.Info("通过 IP 检测到国家", slog.String("country", string(country)), slog.String("ip", ip))

		// 2. 根据国家选择下载源
		if country == CountryCN {
			// 国内，检测 ModelScope
			if s.checkModelScope(ctx) {
				s.log.Info("ModelScope 可用，选择 ModelScope 作为下载源")
				return &DetectionResult{
					Source:  DownloadSourceModelScope,
					Country: country,
					IP:      ip,
				}
			}
		} else {
			// 国外，先检测 HuggingFace
			if s.checkHuggingFace(ctx) {
				s.log.Info("HuggingFace 可用，选择 HuggingFace 作为下载源")
				return &DetectionResult{
					Source:  DownloadSourceHuggingFace,
					Country: country,
					IP:      ip,
				}
			}
			// HuggingFace 不可用，检测 ModelScope
			if s.checkModelScope(ctx) {
				s.log.Info("HuggingFace 不可用，但 ModelScope 可用，选择 ModelScope 作为下载源")
				return &DetectionResult{
					Source:  DownloadSourceModelScope,
					Country: country,
					IP:      ip,
				}
			}
		}
	} else {
		s.log.Warn("通过 IP 检测国家失败", slog.Any("error", err))
	}

	// 3. IP 检测失败，尝试检测操作系统区域
	country, err = s.detectCountryByOS()
	if err == nil {
		s.log.Info("通过操作系统检测到国家", slog.String("country", string(country)))

		// 根据国家选择下载源
		if country == CountryCN {
			if s.checkModelScope(ctx) {
				s.log.Info("ModelScope 可用，选择 ModelScope 作为下载源")
				return &DetectionResult{
					Source:  DownloadSourceModelScope,
					Country: country,
					IP:      "",
				}
			}
		} else {
			if s.checkHuggingFace(ctx) {
				s.log.Info("HuggingFace 可用，选择 HuggingFace 作为下载源")
				return &DetectionResult{
					Source:  DownloadSourceHuggingFace,
					Country: country,
					IP:      "",
				}
			}
			if s.checkModelScope(ctx) {
				s.log.Info("HuggingFace 不可用，但 ModelScope 可用，选择 ModelScope 作为下载源")
				return &DetectionResult{
					Source:  DownloadSourceModelScope,
					Country: country,
					IP:      "",
				}
			}
		}
	} else {
		s.log.Warn("通过操作系统检测国家失败", slog.Any("error", err))
	}

	// 4. 所有检测都失败，尝试使用缓存
	cachedResult, err := s.loadCachedResult()
	if err == nil {
		s.log.Info("使用缓存的下载源检测结果", slog.String("source", string(cachedResult.Source)))
		return &DetectionResult{
			Source:  cachedResult.Source,
			Country: cachedResult.Country,
			IP:      cachedResult.IP,
		}
	} else {
		s.log.Warn("加载缓存失败", slog.Any("error", err))
	}

	// 5. 所有方法都失败，默认认为是国外
	s.log.Warn("所有检测方法都失败，默认使用 HuggingFace 作为下载源")
	return &DetectionResult{
		Source:  DownloadSourceHuggingFace,
		Country: CountryUS,
		IP:      "",
	}
}

// detectCountryByIP 通过 IP 检测国家（优先国内接口，失败后使用国外接口）
func (s *DownloadSourceSelector) detectCountryByIP(ctx context.Context) (Country, string, error) {
	// 优先使用国内接口
	country, ip, err := s.detectCountryByIPChina(ctx)
	if err == nil {
		return country, ip, nil
	}

	s.log.Warn("国内接口检测失败，尝试国外接口", slog.Any("error", err))

	// 国内接口失败，使用国外接口
	return s.detectCountryByIPForeign(ctx)
}

// detectCountryByIPChina 使用国内接口检测国家
func (s *DownloadSourceSelector) detectCountryByIPChina(ctx context.Context) (Country, string, error) {
	// 尝试 api.country.is
	country, ip, err := s.callCountryIsAPI(ctx)
	if err == nil {
		return country, ip, nil
	}

	// 尝试 ip9.com.cn
	return s.callIP9API(ctx)
}

// detectCountryByIPForeign 使用国外接口检测国家
func (s *DownloadSourceSelector) detectCountryByIPForeign(ctx context.Context) (Country, string, error) {
	// 尝试 api.ip.sb
	country, ip, err := s.callIPSbAPI(ctx)
	if err == nil {
		return country, ip, nil
	}

	// 尝试 v4.ident.me
	return s.callIdentMeAPI(ctx)
}

// callCountryIsAPI 调用 api.country.is 接口
func (s *DownloadSourceSelector) callCountryIsAPI(ctx context.Context) (Country, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.country.is", nil)
	if err != nil {
		return "", "", fmt.Errorf("创建请求失败: %v", err)
	}

	client := s.networkClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("读取响应失败: %v", err)
	}

	var info IPInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return "", "", fmt.Errorf("解析响应失败: %v", err)
	}

	return info.Country, info.IP, nil
}

// callIP9API 调用 ip9.com.cn 接口
func (s *DownloadSourceSelector) callIP9API(ctx context.Context) (Country, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://ip9.com.cn/get", nil)
	if err != nil {
		return "", "", fmt.Errorf("创建请求失败: %v", err)
	}

	client := s.networkClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("读取响应失败: %v", err)
	}

	var result struct {
		Ret  int `json:"ret"`
		Data struct {
			IP          string `json:"ip"`
			CountryCode string `json:"country_code"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", fmt.Errorf("解析响应失败: %v", err)
	}

	if result.Ret != 200 {
		return "", "", fmt.Errorf("API 返回错误: %d", result.Ret)
	}

	return Country(strings.ToUpper(result.Data.CountryCode)), result.Data.IP, nil
}

// callIPSbAPI 调用 api.ip.sb 接口
func (s *DownloadSourceSelector) callIPSbAPI(ctx context.Context) (Country, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.ip.sb/geoip", nil)
	if err != nil {
		return "", "", fmt.Errorf("创建请求失败: %v", err)
	}

	client := s.networkClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("读取响应失败: %v", err)
	}

	var info struct {
		IP          string `json:"ip"`
		CountryCode string `json:"country_code"`
	}

	if err := json.Unmarshal(body, &info); err != nil {
		return "", "", fmt.Errorf("解析响应失败: %v", err)
	}

	return Country(strings.ToUpper(info.CountryCode)), info.IP, nil
}

// callIdentMeAPI 调用 v4.ident.me 接口
func (s *DownloadSourceSelector) callIdentMeAPI(ctx context.Context) (Country, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://v4.ident.me/json", nil)
	if err != nil {
		return "", "", fmt.Errorf("创建请求失败: %v", err)
	}

	client := s.networkClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP 状态码: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("读取响应失败: %v", err)
	}

	var info struct {
		IP      string `json:"ip"`
		CC      string `json:"cc"`
		Country string `json:"country"`
	}

	if err := json.Unmarshal(body, &info); err != nil {
		return "", "", fmt.Errorf("解析响应失败: %v", err)
	}

	return Country(strings.ToUpper(info.CC)), info.IP, nil
}

// detectCountryByOS 通过操作系统检测国家
func (s *DownloadSourceSelector) detectCountryByOS() (Country, error) {
	// 优先使用第三方库
	country, err := s.detectCountryByLocale()
	if err == nil {
		return country, nil
	}

	s.log.Warn("使用第三方库检测失败，尝试 PowerShell", slog.Any("error", err))

	// 第三方库失败，使用 PowerShell
	return s.detectCountryByPowerShell()
}

// detectCountryByLocale 使用第三方库检测国家
func (s *DownloadSourceSelector) detectCountryByLocale() (Country, error) {
	// 使用 github.com/Xuanwo/go-locale 库检测
	tag, err := locale.Detect()
	if err != nil {
		return "", fmt.Errorf("使用 go-locale 检测失败: %v", err)
	}

	lang := tag.String()
	s.log.Info("使用 go-locale 检测到语言", slog.String("lang", lang))

	if strings.Contains(strings.ToLower(lang), "zh") {
		return CountryCN, nil
	}

	return "", fmt.Errorf("无法通过 go-locale 检测国家")
}

// detectCountryByPowerShell 使用 PowerShell 检测国家
func (s *DownloadSourceSelector) detectCountryByPowerShell() (Country, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "Get-Culture | select -exp Name")
	hideCommandWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("执行 PowerShell 命令失败: %v", err)
	}

	lang := strings.TrimSpace(string(output))
	s.log.Info("检测到操作系统语言", slog.String("lang", lang))

	if strings.HasPrefix(strings.ToLower(lang), "zh") {
		return CountryCN, nil
	}

	return CountryUS, nil
}

// checkModelScope 检测 ModelScope 是否可连接
func (s *DownloadSourceSelector) checkModelScope(ctx context.Context) bool {
	return s.checkURL(ctx, "https://www.modelscope.cn")
}

// checkHuggingFace 检测 HuggingFace 是否可连接
func (s *DownloadSourceSelector) checkHuggingFace(ctx context.Context) bool {
	return s.checkURL(ctx, "https://huggingface.co")
}

// checkURL 检测 URL 是否可连接
func (s *DownloadSourceSelector) checkURL(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		if s.log != nil {
			s.log.Warn("创建请求失败", slog.Any("error", err))
		}
		return false
	}

	client := s.networkClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		s.log.Warn("请求失败", slog.Any("error", err))
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// cacheResult 缓存检测结果
func (s *DownloadSourceSelector) cacheResult(result *DetectionResult) error {
	if result == nil {
		return fmt.Errorf("检测结果为空")
	}

	cacheResult := &CacheResult{
		Source:      result.Source,
		Country:     result.Country,
		IP:          result.IP,
		LastChecked: time.Now(),
	}

	// 确保目录存在
	dir := filepath.Dir(s.cacheFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建缓存目录失败: %v", err)
	}

	// 序列化
	data, err := json.MarshalIndent(cacheResult, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化缓存失败: %v", err)
	}

	// 写入文件
	if err := os.WriteFile(s.cacheFile, data, 0644); err != nil {
		return fmt.Errorf("写入缓存文件失败: %v", err)
	}

	s.log.Info("已缓存下载源检测结果", slog.String("cacheFile", s.cacheFile))
	return nil
}

// loadCachedResult 加载缓存的检测结果
func (s *DownloadSourceSelector) loadCachedResult() (*CacheResult, error) {
	// 检查文件是否存在
	if _, err := os.Stat(s.cacheFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("缓存文件不存在")
	}

	// 读取文件
	data, err := os.ReadFile(s.cacheFile)
	if err != nil {
		return nil, fmt.Errorf("读取缓存文件失败: %v", err)
	}

	// 反序列化
	var result CacheResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("解析缓存文件失败: %v", err)
	}

	return &result, nil
}

// GetDetectionResult 获取检测结果
func (s *DownloadSourceSelector) GetDetectionResult() *DetectionResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.detectionResult
}

// CachedSource returns the last detected source without performing network I/O.
// It falls back to the persisted cache when this selector has not detected a
// source in the current process.
func (s *DownloadSourceSelector) CachedSource() DownloadSource {
	if s == nil {
		return DownloadSourceNone
	}
	s.mu.RLock()
	result := s.detectionResult
	s.mu.RUnlock()
	if result != nil {
		return result.Source
	}
	cached, err := s.loadCachedResult()
	if err != nil || cached == nil {
		return DownloadSourceNone
	}
	return cached.Source
}

// Reset 重置选择器（用于测试）
func (s *DownloadSourceSelector) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.once = sync.Once{}
	s.detectionResult = nil
	s.cachedResult = nil
}
