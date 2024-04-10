package main
//version 1
import (
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

type Config struct {
	TimeoutCount int
	IntervalSec int
	MailTo       string
	CallExchange bool
}

func init() {
	// 初始化配置
	viper.SetConfigName("env")  // 配置文件名称（无文件扩展名）
	viper.SetConfigType("yaml") // 配置文件类型
	viper.AddConfigPath("conf") // 配置文件路径
	err := viper.ReadInConfig() // 读取配置数据
	if err != nil {
		log.Fatalf("error reading config file, %s", err)
	}
}

func main() {
	webPort := viper.GetString("webPort")
	interval := viper.GetInt("intervalSec")

	cfg := Config{
		TimeoutCount: viper.GetInt("timeoutCount"),
		MailTo:       viper.GetString("mailTo"),
		CallExchange: viper.GetBool("callExchange"),
		IntervalSec:      viper.GetInt("intervalSec"),
	}

	log.Printf("Start env.yaml %v", cfg)

	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop() 
		for range ticker.C {
			log.Println("Running UpdateThumbnails...")
			UpdateThumbnails(cfg)
			log.Println("UpdateThumbnails completed.")
		}
	}()	
	// 创建一个不使用默认中间件的路由器
	router := gin.New()
	//router := gin.Default()

	//添加自定义的日志中间件
	router.Use(CustomLogger())

	router.Static("/thumbnails", "./thumbnails")
	router.Static("/static", "./static")

	router.LoadHTMLGlob("templates/*")

	router.GET("/", func(c *gin.Context) {
		thumbnailMutex.Lock()
		defer thumbnailMutex.Unlock()
		c.HTML(http.StatusOK, "index.tmpl", gin.H{
			"Thumbnails": thumbnails,
		})
	})

	// 新增一個路由來處理 Ajax 請求
	router.GET("/thumbnails-data", func(c *gin.Context) {
		thumbnailMutex.Lock()
		defer thumbnailMutex.Unlock()

		// 將映射轉換為切片
		var thumbsSlice []Thumbnail
		for _, thumb := range thumbnails {
			thumbsSlice = append(thumbsSlice, thumb)
		}

		// 按 CH 排序 thumbsSlice
		sort.Slice(thumbsSlice, func(i, j int) bool {
			return thumbsSlice[i].Ch < thumbsSlice[j].Ch
		})

		c.JSON(http.StatusOK, thumbsSlice)
	})

	router.Run(webPort)
}

func CustomLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 檢查是否為要忽略的路徑
		if strings.HasPrefix(c.Request.URL.Path, "/thumbnails/") {
			// 不調用下一個中間件，直接返回，這樣就不會記錄日誌
			c.Next()
			return
		}
		
		// 調用下一個中間件（例如 Gin 的日誌中間件）
		c.Next()

		// 這裡可以添加其他日誌處理邏輯，如果需要
	}
}
