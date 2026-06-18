package library

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/net/html/charset"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// charsetPatternInDOMStr meta[http-equiv]元素, content属性中charset截取的正则模式.
// 如<meta http-equiv="content-type" content="text/html; charset=utf-8">
var charsetPatternInDOMStr = `charset\s*=\s*(\S*)\s*;?`

// charsetPattern 普通的MatchString可直接接受模式字符串, 无需Compile,
// 但是只能作为判断是否匹配, 无法从中获取其他信息.
var charsetPattern = regexp.MustCompile(charsetPatternInDOMStr)

type RequestData struct {
	Header     http.Header
	Request    *http.Request
	Body       string
	Status     string
	StatusCode int
	Domain     string
	Scheme     string
	IP         string
	Server     string
}

type Options struct {
	Timeout     time.Duration
	Debug       bool
	Method      string
	Type        string
	Query       interface{}
	Data        interface{}
	Header      map[string]string
	Proxy       string
	Cookies     []*http.Cookie
	UserAgent   string
	IsMobile    bool
	DialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Request
// 请求网络页面，并自动检测页面内容的编码，转换成utf-8
func Request(urlPath string, options *Options) (*RequestData, error) {
	if options == nil {
		options = &Options{
			Method:    "GET",
			Timeout:   10,
			UserAgent: GetUserAgent(false),
		}
	}
	if options.Timeout == 0 {
		options.Timeout = 10
	}
	if options.Method == "" {
		options.Method = "GET"
	}
	options.Method = strings.ToUpper(options.Method)

	// 构建 HTTP 请求
	var bodyReader io.Reader
	if options.Data != nil {
		switch data := options.Data.(type) {
		case string:
			bodyReader = strings.NewReader(data)
		case []byte:
			bodyReader = bytes.NewReader(data)
		default:
			jsonBytes, err := json.Marshal(options.Data)
			if err == nil {
				bodyReader = bytes.NewReader(jsonBytes)
			}
		}
	}

	req, err := http.NewRequest(options.Method, urlPath, bodyReader)
	if err != nil {
		return nil, err
	}

	// 设置默认 Referer
	parsedUrl, err := url.Parse(urlPath)
	if err != nil {
		return nil, err
	}
	refererUrl := *parsedUrl
	refererUrl.Path = ""
	refererUrl.RawQuery = ""
	refererUrl.Fragment = ""
	req.Header.Set("Referer", refererUrl.String())

	// 设置 User-Agent
	if options.UserAgent == "" {
		options.UserAgent = GetUserAgent(options.IsMobile)
	}
	req.Header.Set("User-Agent", options.UserAgent)

	// 设置 Content-Type
	if options.Type != "" {
		req.Header.Set("Content-Type", options.Type)
	}

	// 设置自定义 Header
	if options.Header != nil {
		for k, v := range options.Header {
			req.Header.Set(k, v)
		}
	}

	// 设置 Cookie
	if options.Cookies != nil {
		for _, c := range options.Cookies {
			req.AddCookie(c)
		}
	}

	// 设置 Query 参数
	if options.Query != nil {
		q := req.URL.Query()
		switch query := options.Query.(type) {
		case string:
			// 解析并设置
			if queryParts := strings.Split(query, "&"); len(queryParts) > 0 {
				for _, part := range queryParts {
					if kv := strings.SplitN(part, "=", 2); len(kv) == 2 {
						q.Set(kv[0], kv[1])
					}
				}
			}
		case map[string]string:
			for k, v := range query {
				q.Set(k, v)
			}
		}
		req.URL.RawQuery = q.Encode()
	}

	// 构建 HTTP Client
	transport := &http.Transport{}
	if options.DialContext != nil {
		transport.DialContext = options.DialContext
	}
	if options.Proxy != "" {
		proxyUrl, err := url.Parse(options.Proxy)
		if err == nil {
			transport.Proxy = http.ProxyURL(proxyUrl)
		}
	}

	client := &http.Client{
		Timeout:   options.Timeout * time.Second,
		Transport: transport,
		// 禁止自动重定向，与 gorequest 默认一致
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		// 如果是 https 错误，尝试回退到 http
		if strings.HasPrefix(urlPath, "https") {
			urlPath = strings.Replace(urlPath, "https://", "http://", 1)
			return Request(urlPath, options)
		}
		return &RequestData{}, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return &RequestData{}, err
	}
	body := string(bodyBytes)

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "html") {
		charsetName, err := getPageCharset(body, contentType)
		if err != nil {
			log.Println("获取页面编码失败: ", err.Error())
		}
		charsetName = strings.ToLower(charsetName)
		charSet, _ := CharsetMap[charsetName]
		if charSet != nil {
			utf8Content, err := DecodeToUTF8([]byte(body), charSet)
			if err != nil {
				log.Println("页面解码失败: ", err.Error())
			} else {
				body = string(utf8Content)
			}
		}
	}

	requestData := RequestData{
		Header:     resp.Header,
		Request:    resp.Request,
		Body:       body,
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Domain:     resp.Request.Host,
		Scheme:     resp.Request.URL.Scheme,
		Server:     resp.Header.Get("Server"),
	}

	return &requestData, nil
}

// getPageCharset 解析页面, 从中获取页面编码信息
func getPageCharset(content, contentType string) (charSet string, err error) {
	//log.Println("服务器返回编码：", contentType)
	if contentType != "" {
		matchedArray := charsetPattern.FindStringSubmatch(strings.ToLower(contentType))
		if len(matchedArray) > 1 {
			for _, matchedItem := range matchedArray[1:] {
				if strings.ToLower(matchedItem) != "utf-8" {
					charSet = matchedItem
					return
				}
			}
		}
	}
	//log.Println("继续查找编码1")
	var checkType string
	reg := regexp.MustCompile(`(?is)<title[^>]*>(.*?)<\/title>`)
	match := reg.FindStringSubmatch(content)
	if len(match) > 1 {
		_, checkType, _ = charset.DetermineEncoding([]byte(match[1]), "")
		//log.Println("Title解析编码：", checkType)
		if checkType == "utf-8" {
			charSet = checkType
			return
		}
	}
	//log.Println("继续查找编码2")
	reg = regexp.MustCompile(`(?is)<meta[^>]*charset\s*=["']?\s*([\w\d\-]+)`)
	match = reg.FindStringSubmatch(content)
	if len(match) > 1 {
		charSet = match[1]
		return
	}
	//log.Println("找不到编码")
	charSet = "utf-8"
	return
}

func GetUserAgent(isMobile bool) string {
	if isMobile {
		return "Mozilla/5.0 (iPhone; CPU iPhone OS 10_3_1 like Mac OS X) AppleWebKit/603.1.30 (KHTML, like Gecko) Version/10.0 Mobile/14E304 Safari/602.1"
	}

	return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/97.0.4692.71 Safari/537.36"
}

func GetURLData(url, refer string, timeout int) (*RequestData, error) {
	//log.Println(url)
	client := &http.Client{}
	if timeout > 0 {
		client.Timeout = time.Duration(timeout) * time.Second
	} else if timeout < 0 {
		client.Timeout = 10 * time.Second
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", GetUserAgent(false))
	req.Header.Set("Referer", refer)

	resp, err := client.Do(req)
	if err != nil {
		return &RequestData{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "html") {
		// 编码处理
		charsetName, err := getPageCharset(string(body), contentType)
		if err != nil {
			log.Println("获取页面编码失败: ", err.Error())
		}
		charsetName = strings.ToLower(charsetName)
		//log.Println("当前页面编码:", charsetName)
		charSet, exist := CharsetMap[charsetName]
		if !exist {
			log.Println("未找到匹配的编码")
		}
		if charSet != nil {
			utf8Coutent, err := DecodeToUTF8(body, charSet)
			if err != nil {
				log.Println("页面解码失败: ", err.Error())
			} else {
				body = utf8Coutent
			}
		}
	}

	requestData := RequestData{
		Header:     resp.Header,
		Request:    resp.Request,
		Body:       string(body),
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Domain:     resp.Request.Host,
		Scheme:     resp.Request.URL.Scheme,
		Server:     resp.Header.Get("Server"),
	}

	return &requestData, nil
}
