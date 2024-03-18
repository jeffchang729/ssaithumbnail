package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	globalWatcherList []watcherList
	thumbnailMutex    sync.Mutex                                        // 保護 thumbnails 變量
	thumbnails        map[string]Thumbnail = make(map[string]Thumbnail) // 使用映射存儲 Thumbnail.
)

func init() {
	jsonData, err := os.ReadFile("conf/watcherList.json")
	if err != nil {
		log.Fatalf("Error reading JSON file: %v", err)
	}

	err = json.Unmarshal(jsonData, &globalWatcherList)
	if err != nil {
		log.Fatalf("Error decoding JSON: %v", err)
	}
}

type watcherList struct {
	CH      string `json:"ch"`
	CHName  string `json:"chname"`
	URL     string `json:"url"`
	Product string `json:"product"`
	CdnCode string `json:"cdnCode"`
	Name    string `json:"name"`
}

type Thumbnail struct {
	Ch           string `json:"ch"`
	Name         string `json:"name"`
	CHName       string `json:"chname"`
	Path         string `json:"path"`	
	URL          string `json:"url"`
	GenTime      string `json:"genTime"`      // 儲存生成時間
	M3U8Url      string `json:"M3U8Url"`      // 儲存 M3U8 網址
	EqualCounter int    `json:"equalCounter"` // 紀錄相同 M3U8Url 的次數
	AlarmFlag    bool   `json:"alarmFlag"`    // 判斷要不要通知actionExchange
}

func GetFirstSegmentURL(url string) (string, int, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", 404, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, err
	}

	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "https://") {
			return strings.TrimSpace(line), resp.StatusCode, nil
		}
	}

	return "", resp.StatusCode, fmt.Errorf("no valid segment URL found in the M3U8 content")
}

func GenerateThumbnail(segmentURL, thumbnailPath string) (string, error) {
	// 修改命令來首先跳轉到指定的時間，"-q:v" 質量為 1-31（最低） ，並直接將輸出保存到文件
	cmd := exec.Command("ffmpeg", "-i", segmentURL, "-ss", "00:00:01", "-vframes", "1", "-q:v", "2", "-s", fmt.Sprintf("%dx%d", 200, 150), "-y", thumbnailPath)
 
	// 啟用偵錯模式
    //cmd.Stdout = os.Stdout
    //cmd.Stderr = os.Stderr

	// 執行命令
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to generate thumbnail: %v", err)
	}

	// 獲取當前時間作為生成時間
	genTime := time.Now().Format("15:04:05") // 修改時間格式為標準格式

	return genTime, nil
}

func UpdateThumbnails(cfg Config) {

	beginErrTime := time.Now()
	notifyErrTime := time.Now()

	var wg sync.WaitGroup

	for _, wList := range globalWatcherList {
		wg.Add(1)
		// 為每個頻道獲取專屬的日誌記錄器
		channelLogger := getChannelLogger(wList.Name)

		go func(wList watcherList) {
			defer wg.Done()

			firstSegmentURL, httpStatusCode, err := GetFirstSegmentURL(wList.URL)
			if err != nil {
				channelLogger.Println("Error fetching and extracting segment URL:", err)
				return
			}

			thumbnailPath := "thumbnails/" + wList.Name + ".jpg"
			genTime, err := GenerateThumbnail(firstSegmentURL, thumbnailPath)
			if err != nil {
				channelLogger.Println("Error generating thumbnail:", err)
				return
			}
			channelLogger.Printf("[%v][%v][%v] GenerateThumbnail %s\n", httpStatusCode, wList.CH, wList.Name, firstSegmentURL)

			thumbnailMutex.Lock()
			defer thumbnailMutex.Unlock()

			// 預設 EqualCounter 為 0，表示新的或已變化的 URL
			newEqualCounter := 0

			thumb, exists := thumbnails[wList.CH]

			// 檢查此 Ch 是否已存在，且 URL 未變化 , http Status Code 4xx,5xx也要記錄
			if (exists && thumb.M3U8Url == firstSegmentURL) || (httpStatusCode >= 400 && httpStatusCode <= 599) {
				// 如果 M3U8Url 相同，保留原有計數並加 1
				newEqualCounter = thumb.EqualCounter + 1
				//記錄文件發生錯誤時間
				if newEqualCounter == 2 {
					beginErrTime = time.Now()
				}
				channelLogger.Printf("httpStatusCode[%v] EqualCounter: %v callExchange: %v Alarm: %v  M3U8Url: %v",
					httpStatusCode, newEqualCounter, cfg.CallExchange, thumb.AlarmFlag, thumb.M3U8Url)

			} else {
				//內容不同就再啟動 actionExchange 功能
				thumb.AlarmFlag = false
			}
			//channelLogger.Printf("----newEqualCounter[%v]  TimeoutCount[%v]  AlarmFlag[%v]", newEqualCounter, cfg.TimeoutCount, thumb.AlarmFlag)

			if (newEqualCounter >= cfg.TimeoutCount) && !thumb.AlarmFlag {
				thumb.AlarmFlag = true

				if cfg.CallExchange {
					//記錄通知時間
					notifyErrTime = time.Now()

					channelLogger.Printf("----sendMail[%v] ", wList.Name)
					err = sendMail(wList.URL, wList.Product,wList.CdnCode, wList.Name, cfg.MailTo, beginErrTime, notifyErrTime)
					if err != nil {
						channelLogger.Printf("sendMail Error: %+v", err)
					}

					channelLogger.Printf("----actionExchange[%v][%v] ", wList.Product, wList.CdnCode)
					err = actionExchange(wList.Product, wList.CdnCode)
					if err != nil {
						channelLogger.Printf("actionExchange Error: %+v", err)
					}
				}
			}

			// 更新或新增 Thumbnail 結構體
			thumbnails[wList.CH] = Thumbnail{
				Ch:           wList.CH,
				CHName:       wList.CHName,
				Name:         wList.Name,
				Path:         thumbnailPath,
				URL:          wList.URL,
				GenTime:      genTime,
				M3U8Url:      firstSegmentURL,
				EqualCounter: newEqualCounter,
				AlarmFlag:    thumb.AlarmFlag,
			}

		}(wList)
	}

	wg.Wait()

}

func sendMail(inputURL string, product string,cdnCode string, chName string, mailTo string, beginErrTime time.Time, notifyErrTime time.Time)error {

	nowTime := time.Now().Format("2006-01-02")
	beginTime := beginErrTime.Format("2006-01-02 15:04:05")
	notifyTime := notifyErrTime.Format("2006-01-02 15:04:05")

	subject := fmt.Sprintf("!!! SSAIThumbnail 頻道監控異常 [%s] [%s][%s]", nowTime, product, cdnCode)

	body := "開始發生錯誤時間：" + beginTime + "\r\n" +
		"通知錯誤時間：" + notifyTime + "\r\n" +
		"頻 道 名 稱：" + chName + "\r\n" +
		"CDN URL：" + inputURL + "\r\n\r\n" +
		"Error Messages：\r\n" +
		"---------------------------------\r\n" +
		"上述 error 以時間 ascending. \r\n" +
		"playlist parsing error\r\n" +
		"playlist content\r\n\r\n" +
		getUrl(inputURL) + "\r\n" +
		"---------------------------------\r\n\r\n" +
		"！！！請儘速查明原因！！！\r\n\r\n" +
		"SSAI Group\r\n" +
		"\r\n"

	msg := "Subject: " + subject + "\r\n\r\n" + body

	err := smtp.SendMail(
		"localhost:25",
		nil,
		"SSAI-Watcher@tgc-taiwan.com.tw",
		strings.Split(mailTo, ","),
		[]byte(msg),
	)

    if err != nil {
        return fmt.Errorf("failed to send email for channel %s, error: %v", chName, err)
    }
	return nil
}

func getUrl(url string) string {
	// 发起 HTTP 请求
	response, err := http.Get(url)
	if err != nil {
		return fmt.Sprintf("Error fetching URL: %s, %v", url, err)
	}
	defer response.Body.Close()

	// 使用字符串构建响应体
	var bodyBuilder strings.Builder
	_, err = io.Copy(&bodyBuilder, response.Body)
	if err != nil {
		return fmt.Sprintf("Error copying response body: %s, %v", url, err)
	}

	return bodyBuilder.String()
}
func actionExchange(product, cdnCode string) error {

	urlString := fmt.Sprintf("http://172.21.102.117/api/closeSsai/%s/%s", product, cdnCode)
	resp, err := http.Get(urlString)

	if err != nil {
		return fmt.Errorf("actionExchange for channel %s, error: %v", cdnCode, err)
	}
	defer resp.Body.Close()

	respBody, err2 := io.ReadAll(resp.Body)
	if err2 != nil {
		return fmt.Errorf("actionExchange for channel %s, error: %v", cdnCode, err)
	}

	log.Printf("Exchanger Response: %d %s", resp.StatusCode, string(respBody))
	return nil
}
func getChannelLogger(name string) *log.Logger {
	logDir := "logs" // 日誌檔案存放的目錄
	if _, err := os.Stat(logDir); os.IsNotExist(err) {
		os.Mkdir(logDir, os.ModePerm) // 如果 logs 目錄不存在，則創建它
	}

	logFilePath := filepath.Join(logDir, name+".log")
	logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatalf("Error opening log file: %v", err)
	}

	return log.New(logFile, "", log.Ldate|log.Ltime|log.Lshortfile)
}

